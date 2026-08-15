// Package bridge reconciles GitHub issues and PR reviews with t3 sessions.
//
// Each tick is a full reconcile pass driven by observed state on both sides,
// so restarts and missed ticks are safe:
//
//	open issue assigned to X, no session yet  -> bootstrap a t3 thread in a
//	                                             fresh worktree and start a
//	                                             turn that ends in a draft PR
//	session turn finished, no PR recorded     -> adopt the PR by head branch,
//	                                             or nudge the session once
//	new "changes requested" review on the PR  -> start a follow-up turn on
//	                                             the same thread
//	PR merged or closed                       -> archive the thread
package bridge

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/geoah/t3-bridge/internal/config"
	"github.com/geoah/t3-bridge/internal/gh"
	"github.com/geoah/t3-bridge/internal/state"
	"github.com/geoah/t3-bridge/internal/t3"
)

const maxNudges = 1

var fallbackModel = t3.ModelSelection{InstanceID: "claudeAgent", Model: "claude-fable-5"}

type Bridge struct {
	Cfg *config.Config
	T3  *t3.Client
	GH  *gh.Client
	St  *state.State
	Log *slog.Logger

	baseBranch map[string]string
	retryAfter map[string]time.Time
}

func New(cfg *config.Config, t3c *t3.Client, ghc *gh.Client, st *state.State, log *slog.Logger) *Bridge {
	return &Bridge{
		Cfg: cfg, T3: t3c, GH: ghc, St: st, Log: log,
		baseBranch: map[string]string{},
		retryAfter: map[string]time.Time{},
	}
}

// Tick runs one reconcile pass. Errors on individual items are logged, not
// returned, so one bad issue cannot stall the rest.
func (b *Bridge) Tick(ctx context.Context) error {
	shell, err := b.T3.Shell(ctx)
	if err != nil {
		return fmt.Errorf("fetch t3 shell snapshot: %w", err)
	}
	for i := range b.Cfg.Repos {
		b.reconcileRepo(ctx, &b.Cfg.Repos[i], shell)
	}
	b.reconcileItems(ctx, shell)
	if err := b.St.Save(); err != nil {
		return fmt.Errorf("save state: %w", err)
	}
	return nil
}

func (b *Bridge) repoConfig(repo string) *config.RepoConfig {
	for i := range b.Cfg.Repos {
		if b.Cfg.Repos[i].Repo == repo {
			return &b.Cfg.Repos[i]
		}
	}
	return nil
}

func (b *Bridge) project(shell *t3.ShellSnapshot, rc *config.RepoConfig) *t3.Project {
	if rc.ProjectID != "" {
		return shell.ProjectByID(rc.ProjectID)
	}
	return shell.ProjectByWorkspaceRoot(rc.WorkspaceRoot)
}

// reconcileRepo picks up newly assigned issues.
func (b *Bridge) reconcileRepo(ctx context.Context, rc *config.RepoConfig, shell *t3.ShellSnapshot) {
	log := b.Log.With("repo", rc.Repo)
	project := b.project(shell, rc)
	if project == nil {
		log.Warn("no matching t3 project; add one in t3 first", "workspaceRoot", rc.WorkspaceRoot, "projectId", rc.ProjectID)
		return
	}
	issues, err := b.GH.ListOpenAssignedIssues(ctx, rc.Repo, rc.Assignee)
	if err != nil {
		log.Error("list assigned issues", "err", err)
		return
	}
	for i := range issues {
		issue := &issues[i]
		if b.St.Get(rc.Repo, issue.Number) != nil {
			continue
		}
		key := state.Key(rc.Repo, issue.Number)
		if until, ok := b.retryAfter[key]; ok && time.Now().Before(until) {
			continue
		}
		branch := fmt.Sprintf("%sissue-%d", rc.BranchPrefix, issue.Number)
		// Crash recovery: a thread for this branch may already exist.
		if th := shell.ThreadByBranch(project.ID, branch); th != nil {
			log.Info("adopting existing thread", "issue", issue.Number, "thread", th.ID, "branch", branch)
			b.St.Put(&state.Item{
				Repo: rc.Repo, IssueNumber: issue.Number, IssueTitle: issue.Title,
				ThreadID: th.ID, Branch: branch,
			})
			continue
		}
		if err := b.startIssueThread(ctx, rc, project, issue, branch); err != nil {
			log.Error("start session for issue", "issue", issue.Number, "err", err)
			b.retryAfter[key] = time.Now().Add(10 * time.Minute)
		}
	}
}

func (b *Bridge) modelFor(rc *config.RepoConfig, project *t3.Project) t3.ModelSelection {
	if rc.Model != nil {
		return rc.Model.Selection()
	}
	if project.DefaultModelSelection != nil {
		return *project.DefaultModelSelection
	}
	return fallbackModel
}

func (b *Bridge) resolveBaseBranch(ctx context.Context, rc *config.RepoConfig) (string, error) {
	if rc.BaseBranch != "" {
		return rc.BaseBranch, nil
	}
	if cached, ok := b.baseBranch[rc.Repo]; ok {
		return cached, nil
	}
	repo, err := b.GH.GetRepo(ctx, rc.Repo)
	if err != nil {
		return "", err
	}
	b.baseBranch[rc.Repo] = repo.DefaultBranch
	return repo.DefaultBranch, nil
}

func (b *Bridge) startIssueThread(ctx context.Context, rc *config.RepoConfig, project *t3.Project, issue *gh.Issue, branch string) error {
	baseBranch, err := b.resolveBaseBranch(ctx, rc)
	if err != nil {
		return fmt.Errorf("resolve base branch: %w", err)
	}
	// The server's one-shot bootstrap (worktree + thread + turn) only
	// exists on the WebSocket RPC path, so do the same steps ourselves:
	// worktree locally, then thread.create, thread.meta.update, and a
	// plain thread.turn.start over HTTP dispatch.
	worktreePath, createdWorktree, err := ensureWorktree(ctx, project.WorkspaceRoot, b.Cfg.T3.WorktreesDir, baseBranch, branch)
	if err != nil {
		return fmt.Errorf("prepare worktree: %w", err)
	}
	threadID := t3.NewID()
	title := fmt.Sprintf("%s#%d: %s", rc.Repo, issue.Number, issue.Title)
	cleanup := func(deleteThread bool) {
		if deleteThread {
			delCmd := t3.ThreadRefCommand{Type: "thread.delete", CommandID: t3.NewID(), ThreadID: threadID}
			if _, err := b.T3.Dispatch(ctx, delCmd); err != nil {
				b.Log.Warn("cleanup: delete thread", "thread", threadID, "err", err)
			}
		}
		if createdWorktree {
			removeWorktree(ctx, project.WorkspaceRoot, worktreePath, branch)
		}
	}

	createCmd := t3.ThreadCreateCommand{
		Type:            "thread.create",
		CommandID:       t3.NewID(),
		ThreadID:        threadID,
		ProjectID:       project.ID,
		Title:           title,
		ModelSelection:  b.modelFor(rc, project),
		RuntimeMode:     t3.RuntimeModeFullAccess,
		InteractionMode: t3.InteractionModeDefault,
		Branch:          nil,
		WorktreePath:    nil,
		CreatedAt:       t3.Now(),
	}
	if _, err := b.T3.Dispatch(ctx, createCmd); err != nil {
		cleanup(false)
		return fmt.Errorf("dispatch thread.create: %w", err)
	}
	metaCmd := t3.ThreadMetaUpdateCommand{
		Type:         "thread.meta.update",
		CommandID:    t3.NewID(),
		ThreadID:     threadID,
		Branch:       &branch,
		WorktreePath: &worktreePath,
	}
	if _, err := b.T3.Dispatch(ctx, metaCmd); err != nil {
		cleanup(true)
		return fmt.Errorf("dispatch thread.meta.update: %w", err)
	}
	turnCmd := t3.TurnStartCommand{
		Type:      "thread.turn.start",
		CommandID: t3.NewID(),
		ThreadID:  threadID,
		Message: t3.UserMessage{
			MessageID:   t3.NewID(),
			Role:        "user",
			Text:        withSuffix(issuePrompt(rc.Repo, issue, branch), b.Cfg.PromptSuffixFor(rc)),
			Attachments: []any{},
		},
		RuntimeMode:     t3.RuntimeModeFullAccess,
		InteractionMode: t3.InteractionModeDefault,
		CreatedAt:       t3.Now(),
	}
	if _, err := b.T3.Dispatch(ctx, turnCmd); err != nil {
		cleanup(true)
		return fmt.Errorf("dispatch thread.turn.start: %w", err)
	}
	b.Log.Info("started session", "repo", rc.Repo, "issue", issue.Number, "thread", threadID, "branch", branch)
	b.St.Put(&state.Item{
		Repo: rc.Repo, IssueNumber: issue.Number, IssueTitle: issue.Title,
		ThreadID: threadID, Branch: branch,
	})
	comment := fmt.Sprintf("Working on this in `%s`.", branch)
	if err := b.GH.CommentOnIssue(ctx, rc.Repo, issue.Number, comment); err != nil {
		b.Log.Warn("comment on issue", "repo", rc.Repo, "issue", issue.Number, "err", err)
	}
	return nil
}

func (b *Bridge) reconcileItems(ctx context.Context, shell *t3.ShellSnapshot) {
	keys := make([]string, 0, len(b.St.Items))
	for k := range b.St.Items {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		item := b.St.Items[k]
		if item.Done {
			continue
		}
		rc := b.repoConfig(item.Repo)
		if rc == nil {
			continue
		}
		if err := b.reconcileItem(ctx, rc, item, shell); err != nil {
			b.Log.Error("reconcile item", "item", k, "err", err)
		}
	}
}

func (b *Bridge) reconcileItem(ctx context.Context, rc *config.RepoConfig, item *state.Item, shell *t3.ShellSnapshot) error {
	log := b.Log.With("repo", item.Repo, "issue", item.IssueNumber, "thread", item.ThreadID)
	th := shell.Thread(item.ThreadID)
	if th == nil {
		// Bootstrap failures delete the thread they created; give the
		// server a little time before concluding it is gone for good.
		if time.Since(item.CreatedAt) > 5*time.Minute {
			b.finish(item, "thread missing (bootstrap failed or deleted manually)")
			log.Warn("thread missing; giving up on this issue", "hint", "unassign and reassign the issue to retry")
		}
		return nil
	}
	if th.LatestTurn != nil && th.LatestTurn.State == t3.TurnStateRunning {
		return nil
	}
	if th.HasPendingApprovals || th.HasPendingUserInput {
		log.Warn("session is waiting for a human in the t3 UI; not intervening")
		return nil
	}

	if item.PRNumber == 0 {
		pr, err := b.GH.FindPRByHead(ctx, item.Repo, rc.Owner(), item.Branch)
		if err != nil {
			return fmt.Errorf("find PR by head: %w", err)
		}
		if pr == nil {
			return b.handleMissingPR(ctx, rc, item, th, log)
		}
		item.PRNumber = pr.Number
		b.St.Put(item)
		log.Info("draft PR discovered", "pr", pr.Number, "url", pr.HTMLURL)
	}

	pr, err := b.GH.GetPR(ctx, item.Repo, item.PRNumber)
	if err != nil {
		return fmt.Errorf("get PR %d: %w", item.PRNumber, err)
	}
	if pr.State == "closed" {
		reason := "PR closed without merge"
		if pr.Merged {
			reason = "PR merged"
		}
		b.archiveThread(ctx, item, log)
		b.finish(item, reason)
		log.Info("issue flow finished", "reason", reason, "pr", pr.Number)
		return nil
	}
	b.markReady(ctx, item, th, pr, log)
	return b.forwardNewReviews(ctx, rc, item, th, log)
}

// markReady promotes the PR out of draft once per round of session work.
//
// The daemon does not request a reviewer on GitHub. Review happens locally
// inside the session before it pushes, driven by prompt.suffix; a GitHub
// review request would only invite a second, redundant pass.
func (b *Bridge) markReady(ctx context.Context, item *state.Item, th *t3.ThreadShell, pr *gh.PullRequest, log *slog.Logger) {
	if th.LatestTurn == nil || th.LatestTurn.State != t3.TurnStateCompleted {
		return
	}
	if item.ReadyMarkedTurnID == th.LatestTurn.TurnID {
		return
	}
	if pr.Draft {
		if err := b.GH.MarkPRReady(ctx, item.Repo, pr.Number); err != nil {
			log.Warn("mark PR ready for review", "pr", pr.Number, "err", err)
			return
		}
		log.Info("marked PR ready for review", "pr", pr.Number)
	}
	item.ReadyMarkedTurnID = th.LatestTurn.TurnID
	b.St.Put(item)
}

// handleMissingPR deals with an idle session that has not produced a PR.
func (b *Bridge) handleMissingPR(ctx context.Context, rc *config.RepoConfig, item *state.Item, th *t3.ThreadShell, log *slog.Logger) error {
	if th.LatestTurn == nil || th.LatestTurn.State != t3.TurnStateCompleted {
		// Still bootstrapping, or the turn errored/was interrupted. An
		// interrupt usually means a human took over in the t3 UI.
		if th.LatestTurn != nil && th.LatestTurn.State == t3.TurnStateError {
			b.finish(item, "session turn errored before opening a PR")
			log.Error("session turn errored; inspect the thread in t3", "hint", "unassign and reassign the issue to retry")
		}
		return nil
	}
	issue, err := b.GH.GetIssue(ctx, item.Repo, item.IssueNumber)
	if err != nil {
		return fmt.Errorf("get issue: %w", err)
	}
	if issue.State != "open" || !isAssignedTo(issue, rc.Assignee) {
		b.archiveThread(ctx, item, log)
		b.finish(item, "issue closed or unassigned before a PR was opened")
		log.Info("issue no longer actionable; archived thread")
		return nil
	}
	if item.NudgeCount >= maxNudges {
		b.finish(item, "session finished without opening a PR (it likely commented on the issue instead)")
		log.Warn("no PR after nudge; leaving thread as-is for inspection")
		return nil
	}
	item.NudgeCount++
	b.St.Put(item)
	log.Info("turn completed without a PR; nudging session once")
	return b.startFollowUpTurn(ctx, item, th, withSuffix(nudgePrompt(item.Repo, item.IssueNumber, item.Branch), b.Cfg.PromptSuffixFor(rc)))
}

// forwardNewReviews sends unhandled triggering reviews to the session.
func (b *Bridge) forwardNewReviews(ctx context.Context, rc *config.RepoConfig, item *state.Item, th *t3.ThreadShell, log *slog.Logger) error {
	reviews, err := b.GH.ListReviews(ctx, item.Repo, item.PRNumber)
	if err != nil {
		return fmt.Errorf("list reviews: %w", err)
	}
	var comments []gh.ReviewComment
	commentsLoaded := false
	loadComments := func() ([]gh.ReviewComment, error) {
		if !commentsLoaded {
			comments, err = b.GH.ListReviewComments(ctx, item.Repo, item.PRNumber)
			if err != nil {
				return nil, fmt.Errorf("list review comments: %w", err)
			}
			commentsLoaded = true
		}
		return comments, nil
	}

	var triggered []gh.Review
	for _, rv := range reviews {
		if item.ReviewHandled(rv.ID) {
			continue
		}
		switch rv.State {
		case "CHANGES_REQUESTED":
			triggered = append(triggered, rv)
		case "COMMENTED":
			if rc.ReviewTrigger != config.TriggerAnyReview {
				continue
			}
			// Replying to an inline thread makes GitHub synthesize an
			// empty COMMENTED review around the reply. Only count
			// reviews that add substance (a body or a new thread), so
			// the session replying to feedback cannot re-trigger itself.
			cs, err := loadComments()
			if err != nil {
				return err
			}
			if strings.TrimSpace(rv.Body) == "" && !hasThreadStarter(cs, rv.ID) {
				item.HandledReviewIDs = append(item.HandledReviewIDs, rv.ID)
				continue
			}
			triggered = append(triggered, rv)
		}
	}
	if len(triggered) == 0 {
		b.St.Put(item)
		return nil
	}
	cs, err := loadComments()
	if err != nil {
		return err
	}
	text := withSuffix(reviewPrompt(item.Repo, item.PRNumber, item.Branch, triggered, cs), b.Cfg.PromptSuffixFor(rc))
	if err := b.startFollowUpTurn(ctx, item, th, text); err != nil {
		return err
	}
	for _, rv := range triggered {
		item.HandledReviewIDs = append(item.HandledReviewIDs, rv.ID)
	}
	b.St.Put(item)
	log.Info("forwarded review feedback to session", "pr", item.PRNumber, "reviews", len(triggered))
	return nil
}

func hasThreadStarter(comments []gh.ReviewComment, reviewID int64) bool {
	for _, cm := range comments {
		if cm.PullRequestReviewID == reviewID && cm.InReplyToID == nil {
			return true
		}
	}
	return false
}

func isAssignedTo(issue *gh.Issue, login string) bool {
	for _, a := range issue.Assignees {
		if strings.EqualFold(a.Login, login) {
			return true
		}
	}
	return false
}

func (b *Bridge) startFollowUpTurn(ctx context.Context, item *state.Item, th *t3.ThreadShell, text string) error {
	runtimeMode := th.RuntimeMode
	if runtimeMode == "" {
		runtimeMode = t3.RuntimeModeFullAccess
	}
	cmd := t3.TurnStartCommand{
		Type:      "thread.turn.start",
		CommandID: t3.NewID(),
		ThreadID:  item.ThreadID,
		Message: t3.UserMessage{
			MessageID:   t3.NewID(),
			Role:        "user",
			Text:        text,
			Attachments: []any{},
		},
		RuntimeMode:     runtimeMode,
		InteractionMode: t3.InteractionModeDefault,
		CreatedAt:       t3.Now(),
	}
	if _, err := b.T3.Dispatch(ctx, cmd); err != nil {
		return fmt.Errorf("dispatch follow-up turn: %w", err)
	}
	return nil
}

func (b *Bridge) archiveThread(ctx context.Context, item *state.Item, log *slog.Logger) {
	cmd := t3.ThreadRefCommand{Type: "thread.archive", CommandID: t3.NewID(), ThreadID: item.ThreadID}
	if _, err := b.T3.Dispatch(ctx, cmd); err != nil {
		log.Warn("archive thread", "err", err)
	}
}

func (b *Bridge) finish(item *state.Item, reason string) {
	item.Done = true
	item.DoneReason = reason
	b.St.Put(item)
}
