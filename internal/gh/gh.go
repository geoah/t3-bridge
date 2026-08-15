// Package gh wraps the GitHub CLI (`gh api`), reusing its existing
// authentication instead of managing tokens.
package gh

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

type Client struct {
	// Bin is the gh executable, default "gh".
	Bin string
}

func New() *Client { return &Client{Bin: "gh"} }

func (c *Client) api(ctx context.Context, out any, args ...string) error {
	bin := c.Bin
	if bin == "" {
		bin = "gh"
	}
	cmd := exec.CommandContext(ctx, bin, append([]string{"api"}, args...)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 400 {
			msg = msg[:400]
		}
		return fmt.Errorf("gh api %s: %w: %s", args[0], err, msg)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("gh api %s: decode: %w", args[0], err)
	}
	return nil
}

type User struct {
	Login string `json:"login"`
}

type Label struct {
	Name string `json:"name"`
}

type Issue struct {
	Number      int     `json:"number"`
	Title       string  `json:"title"`
	Body        string  `json:"body"`
	State       string  `json:"state"`
	HTMLURL     string  `json:"html_url"`
	User        User    `json:"user"`
	Assignees   []User  `json:"assignees"`
	Labels      []Label `json:"labels"`
	PullRequest *struct {
		URL string `json:"url"`
	} `json:"pull_request"`
}

type PullRequest struct {
	Number  int    `json:"number"`
	State   string `json:"state"` // open, closed
	Merged  bool   `json:"merged"`
	Draft   bool   `json:"draft"`
	Title   string `json:"title"`
	HTMLURL string `json:"html_url"`
	User    User   `json:"user"`
	Head    struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

type Review struct {
	ID          int64  `json:"id"`
	User        User   `json:"user"`
	Body        string `json:"body"`
	State       string `json:"state"` // APPROVED, CHANGES_REQUESTED, COMMENTED, DISMISSED, PENDING
	SubmittedAt string `json:"submitted_at"`
	HTMLURL     string `json:"html_url"`
}

type ReviewComment struct {
	ID                  int64  `json:"id"`
	PullRequestReviewID int64  `json:"pull_request_review_id"`
	InReplyToID         *int64 `json:"in_reply_to_id"`
	User                User   `json:"user"`
	Body                string `json:"body"`
	Path                string `json:"path"`
	Line                *int   `json:"line"`
	OriginalLine        *int   `json:"original_line"`
	DiffHunk            string `json:"diff_hunk"`
	HTMLURL             string `json:"html_url"`
}

type Repo struct {
	DefaultBranch string `json:"default_branch"`
}

// ListOpenAssignedIssues returns open issues (never PRs) assigned to login.
func (c *Client) ListOpenAssignedIssues(ctx context.Context, repo, login string) ([]Issue, error) {
	var issues []Issue
	err := c.api(ctx, &issues, fmt.Sprintf("repos/%s/issues", repo),
		"--paginate",
		"-X", "GET",
		"-f", "state=open",
		"-f", "assignee="+login,
		"-f", "per_page=100",
	)
	if err != nil {
		return nil, err
	}
	out := issues[:0]
	for _, is := range issues {
		if is.PullRequest == nil {
			out = append(out, is)
		}
	}
	return out, nil
}

func (c *Client) GetIssue(ctx context.Context, repo string, number int) (*Issue, error) {
	var is Issue
	if err := c.api(ctx, &is, fmt.Sprintf("repos/%s/issues/%d", repo, number)); err != nil {
		return nil, err
	}
	return &is, nil
}

func (c *Client) CommentOnIssue(ctx context.Context, repo string, number int, body string) error {
	return c.api(ctx, nil, fmt.Sprintf("repos/%s/issues/%d/comments", repo, number),
		"-X", "POST", "-f", "body="+body)
}

func (c *Client) GetRepo(ctx context.Context, repo string) (*Repo, error) {
	var r Repo
	if err := c.api(ctx, &r, "repos/"+repo); err != nil {
		return nil, err
	}
	return &r, nil
}

// FindPRByHead returns the most recent PR (any state) whose head is branch.
func (c *Client) FindPRByHead(ctx context.Context, repo, owner, branch string) (*PullRequest, error) {
	var prs []PullRequest
	err := c.api(ctx, &prs, fmt.Sprintf("repos/%s/pulls", repo),
		"-X", "GET",
		"-f", "state=all",
		"-f", fmt.Sprintf("head=%s:%s", owner, branch),
		"-f", "sort=created",
		"-f", "direction=desc",
	)
	if err != nil {
		return nil, err
	}
	if len(prs) == 0 {
		return nil, nil
	}
	return &prs[0], nil
}

func (c *Client) GetPR(ctx context.Context, repo string, number int) (*PullRequest, error) {
	var pr PullRequest
	if err := c.api(ctx, &pr, fmt.Sprintf("repos/%s/pulls/%d", repo, number)); err != nil {
		return nil, err
	}
	return &pr, nil
}

func (c *Client) ListReviews(ctx context.Context, repo string, number int) ([]Review, error) {
	var reviews []Review
	err := c.api(ctx, &reviews, fmt.Sprintf("repos/%s/pulls/%d/reviews", repo, number),
		"--paginate", "-X", "GET", "-f", "per_page=100")
	return reviews, err
}

func (c *Client) ListReviewComments(ctx context.Context, repo string, number int) ([]ReviewComment, error) {
	var comments []ReviewComment
	err := c.api(ctx, &comments, fmt.Sprintf("repos/%s/pulls/%d/comments", repo, number),
		"--paginate", "-X", "GET", "-f", "per_page=100")
	return comments, err
}

// MarkPRReady takes a draft PR out of draft. Toggling draft state is
// GraphQL-only on GitHub's API, so this goes through `gh pr ready`.
func (c *Client) MarkPRReady(ctx context.Context, repo string, number int) error {
	bin := c.Bin
	if bin == "" {
		bin = "gh"
	}
	cmd := exec.CommandContext(ctx, bin, "pr", "ready", strconv.Itoa(number), "--repo", repo)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("gh pr ready: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

// Viewer returns the authenticated login, as a doctor check.
func (c *Client) Viewer(ctx context.Context) (string, error) {
	var u User
	if err := c.api(ctx, &u, "user"); err != nil {
		return "", err
	}
	return u.Login, nil
}
