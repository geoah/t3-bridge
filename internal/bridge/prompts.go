package bridge

import (
	"fmt"
	"strings"

	"github.com/geoah/t3-bridge/internal/gh"
)

const promptPreamble = "You are running autonomously; no human is available to answer questions, so make reasonable decisions yourself and record them in the PR description. Never ask for confirmation. Anything you write on GitHub is read by the repository's maintainers: write as the change's author and do not mention this prompt, your tooling, or that you are automated."

func issuePrompt(repo string, issue *gh.Issue, branch string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", promptPreamble)
	fmt.Fprintf(&b, "Implement GitHub issue %s#%d: %s\n", repo, issue.Number, issue.Title)
	fmt.Fprintf(&b, "Issue URL: %s\n\n", issue.HTMLURL)
	body := strings.TrimSpace(issue.Body)
	if body == "" {
		body = "(no description)"
	}
	fmt.Fprintf(&b, "Issue description:\n---\n%s\n---\n\n", body)
	fmt.Fprintf(&b, `Instructions:
1. You are in a dedicated git worktree on branch %[1]s. Do all work here.
2. Check the issue for new comments first: gh issue view %[2]d --repo %[3]s --comments
3. Implement the change following the repository's existing conventions. Add or update tests where it makes sense and run the relevant test suite.
4. Commit with clear messages and push the branch: git push -u origin %[1]s
5. Open a draft pull request: gh pr create --repo %[3]s --draft --head %[1]s
   The PR body MUST contain the line "Fixes #%[2]d" and should summarize the change and any decisions you made on your own.
   Leave it as a draft and do not request reviewers; it is marked ready for review automatically once your turn ends.
6. If the issue cannot reasonably be implemented (contradictory, already done, not actionable), do NOT open a PR; instead explain why in a comment: gh issue comment %[2]d --repo %[3]s
`, branch, issue.Number, repo)
	return b.String()
}

func reviewPrompt(repo string, prNumber int, branch string, reviews []gh.Review, comments []gh.ReviewComment) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n\n", promptPreamble)
	fmt.Fprintf(&b, "Your PR #%d in %s received review feedback that requests changes.\n\n", prNumber, repo)
	for _, rv := range reviews {
		fmt.Fprintf(&b, "Review by @%s (%s, %s): %s\n", rv.User.Login, rv.State, rv.SubmittedAt, rv.HTMLURL)
		if body := strings.TrimSpace(rv.Body); body != "" {
			fmt.Fprintf(&b, "---\n%s\n---\n", body)
		}
		for _, cm := range comments {
			if cm.PullRequestReviewID != rv.ID {
				continue
			}
			line := 0
			if cm.Line != nil {
				line = *cm.Line
			} else if cm.OriginalLine != nil {
				line = *cm.OriginalLine
			}
			fmt.Fprintf(&b, "\nInline comment on %s:%d (%s):\n%s\n", cm.Path, line, cm.HTMLURL, strings.TrimSpace(cm.Body))
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, `Instructions:
1. Pull the latest state of your branch first: git pull origin %[1]s
   You can inspect the full review thread with: gh pr view %[2]d --repo %[3]s --comments
2. Address every point above. If you disagree with a point, explain why in your reply instead of silently skipping it.
3. Run the relevant tests, commit, and push to the same branch (%[1]s).
4. Post a PR comment summarizing how each point was addressed: gh pr comment %[2]d --repo %[3]s
5. Do not merge the PR and do not request reviewers; a fresh review is requested automatically once your turn ends. Merging is the owner's decision.
`, branch, prNumber, repo)
	return b.String()
}

func nudgePrompt(repo string, issueNumber int, branch string) string {
	return fmt.Sprintf(`%s

Your previous turn finished, but no pull request exists for branch %[2]s in %[3]s.

If you intentionally decided issue #%[4]d is not actionable and already commented on the issue explaining why, reply with a one-line confirmation and stop.

Otherwise finish the job now: commit any pending work, push the branch (git push -u origin %[2]s), and open the draft PR (gh pr create --repo %[3]s --draft --head %[2]s) with "Fixes #%[4]d" in the body.`,
		promptPreamble, branch, repo, issueNumber)
}
