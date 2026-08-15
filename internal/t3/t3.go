// Package t3 is a minimal HTTP client for the T3 Code environment server's
// orchestration API. Types mirror the wire schemas in t3's
// packages/contracts/src/orchestration.ts (v0.0.34).
package t3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	RuntimeModeFullAccess  = "full-access"
	InteractionModeDefault = "default"
	TurnStateRunning       = "running"
	TurnStateCompleted     = "completed"
	TurnStateInterrupted   = "interrupted"
	TurnStateError         = "error"
	DefaultBaseURL         = "http://127.0.0.1:3773"
)

type Client struct {
	BaseURL string
	// TokenSource returns the bearer token; it is called per request so a
	// re-minted token file takes effect without a restart. Nil means
	// unauthenticated (only /.well-known works).
	TokenSource func() (string, error)
	HTTP        *http.Client
}

func NewClient(baseURL string, tokenSource func() (string, error)) *Client {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Client{
		BaseURL:     baseURL,
		TokenSource: tokenSource,
		HTTP:        &http.Client{Timeout: 5 * time.Minute},
	}
}

// APIError is the server's structured error envelope.
type APIError struct {
	StatusCode int
	Tag        string `json:"_tag"`
	Code       string `json:"code"`
	Reason     string `json:"reason"`
	TraceID    string `json:"traceId"`
}

func (e *APIError) Error() string {
	return fmt.Sprintf("t3 api: http %d code=%s reason=%s", e.StatusCode, e.Code, e.Reason)
}

// ModelSelection mirrors the ModelSelection wire schema. The yaml tags are
// for the daemon's config file, which can embed a model override.
type ModelSelection struct {
	InstanceID string        `json:"instanceId" yaml:"instanceId"`
	Model      string        `json:"model" yaml:"model"`
	Options    []ModelOption `json:"options,omitempty" yaml:"options,omitempty"`
}

type ModelOption struct {
	ID    string `json:"id" yaml:"id"`
	Value any    `json:"value" yaml:"value"`
}

// Shell snapshot types (GET /api/orchestration/shell). Only the fields the
// bridge consumes are declared; unknown fields are ignored by encoding/json.

type ShellSnapshot struct {
	SnapshotSequence int64         `json:"snapshotSequence"`
	Projects         []Project     `json:"projects"`
	Threads          []ThreadShell `json:"threads"`
	UpdatedAt        string        `json:"updatedAt"`
}

type Project struct {
	ID                    string          `json:"id"`
	Title                 string          `json:"title"`
	WorkspaceRoot         string          `json:"workspaceRoot"`
	DefaultModelSelection *ModelSelection `json:"defaultModelSelection"`
	DeletedAt             *string         `json:"deletedAt"`
}

type ThreadShell struct {
	ID                  string          `json:"id"`
	ProjectID           string          `json:"projectId"`
	Title               string          `json:"title"`
	ModelSelection      *ModelSelection `json:"modelSelection"`
	RuntimeMode         string          `json:"runtimeMode"`
	InteractionMode     string          `json:"interactionMode"`
	Branch              *string         `json:"branch"`
	WorktreePath        *string         `json:"worktreePath"`
	LatestTurn          *LatestTurn     `json:"latestTurn"`
	Session             *Session        `json:"session"`
	ArchivedAt          *string         `json:"archivedAt"`
	HasPendingApprovals bool            `json:"hasPendingApprovals"`
	HasPendingUserInput bool            `json:"hasPendingUserInput"`
	CreatedAt           string          `json:"createdAt"`
	UpdatedAt           string          `json:"updatedAt"`
}

type LatestTurn struct {
	TurnID      string  `json:"turnId"`
	State       string  `json:"state"`
	RequestedAt string  `json:"requestedAt"`
	StartedAt   *string `json:"startedAt"`
	CompletedAt *string `json:"completedAt"`
}

type Session struct {
	Status       string  `json:"status"`
	ProviderName string  `json:"providerName"`
	ActiveTurnID *string `json:"activeTurnId"`
}

// Dispatch command payloads (POST /api/orchestration/dispatch).

type UserMessage struct {
	MessageID   string `json:"messageId"`
	Role        string `json:"role"`
	Text        string `json:"text"`
	Attachments []any  `json:"attachments"`
}

// TurnStartCommand starts a turn on an existing thread. Note the schema's
// optional `bootstrap` block (create thread + prepare worktree in one call)
// is handled only by the WebSocket RPC layer, not HTTP dispatch, so this
// client does not model it; callers create the thread and worktree first.
type TurnStartCommand struct {
	Type            string          `json:"type"`
	CommandID       string          `json:"commandId"`
	ThreadID        string          `json:"threadId"`
	Message         UserMessage     `json:"message"`
	ModelSelection  *ModelSelection `json:"modelSelection,omitempty"`
	TitleSeed       string          `json:"titleSeed,omitempty"`
	RuntimeMode     string          `json:"runtimeMode"`
	InteractionMode string          `json:"interactionMode"`
	CreatedAt       string          `json:"createdAt"`
}

type ThreadCreateCommand struct {
	Type            string         `json:"type"`
	CommandID       string         `json:"commandId"`
	ThreadID        string         `json:"threadId"`
	ProjectID       string         `json:"projectId"`
	Title           string         `json:"title"`
	ModelSelection  ModelSelection `json:"modelSelection"`
	RuntimeMode     string         `json:"runtimeMode"`
	InteractionMode string         `json:"interactionMode"`
	Branch          *string        `json:"branch"`
	WorktreePath    *string        `json:"worktreePath"`
	CreatedAt       string         `json:"createdAt"`
}

type ThreadRefCommand struct {
	Type      string `json:"type"`
	CommandID string `json:"commandId"`
	ThreadID  string `json:"threadId"`
}

type ThreadMetaUpdateCommand struct {
	Type         string  `json:"type"`
	CommandID    string  `json:"commandId"`
	ThreadID     string  `json:"threadId"`
	Branch       *string `json:"branch,omitempty"`
	WorktreePath *string `json:"worktreePath,omitempty"`
}

type DispatchResult struct {
	Sequence int64 `json:"sequence"`
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("t3: marshal request: %w", err)
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.BaseURL+path, reader)
	if err != nil {
		return err
	}
	if c.TokenSource != nil {
		token, err := c.TokenSource()
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return fmt.Errorf("t3: %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return fmt.Errorf("t3: read response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		apiErr := &APIError{StatusCode: resp.StatusCode}
		if json.Unmarshal(data, apiErr) != nil || apiErr.Code == "" {
			apiErr.Code = "unknown"
			apiErr.Reason = string(data[:min(len(data), 300)])
		}
		return apiErr
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("t3: decode %s: %w", path, err)
		}
	}
	return nil
}

func (c *Client) Shell(ctx context.Context) (*ShellSnapshot, error) {
	var snap ShellSnapshot
	if err := c.do(ctx, http.MethodGet, "/api/orchestration/shell", nil, &snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func (c *Client) Dispatch(ctx context.Context, cmd any) (*DispatchResult, error) {
	var res DispatchResult
	if err := c.do(ctx, http.MethodPost, "/api/orchestration/dispatch", cmd, &res); err != nil {
		return nil, err
	}
	return &res, nil
}

// Environment fetches the unauthenticated server descriptor, useful as a
// reachability and version check.
func (c *Client) Environment(ctx context.Context) (map[string]any, error) {
	var out map[string]any
	if err := c.do(ctx, http.MethodGet, "/.well-known/t3/environment", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ShellSnapshot) ProjectByWorkspaceRoot(root string) *Project {
	for i := range s.Projects {
		p := &s.Projects[i]
		if p.DeletedAt == nil && p.WorkspaceRoot == root {
			return p
		}
	}
	return nil
}

func (s *ShellSnapshot) ProjectByID(id string) *Project {
	for i := range s.Projects {
		if s.Projects[i].ID == id && s.Projects[i].DeletedAt == nil {
			return &s.Projects[i]
		}
	}
	return nil
}

func (s *ShellSnapshot) Thread(id string) *ThreadShell {
	for i := range s.Threads {
		if s.Threads[i].ID == id {
			return &s.Threads[i]
		}
	}
	return nil
}

func (s *ShellSnapshot) ThreadByBranch(projectID, branch string) *ThreadShell {
	for i := range s.Threads {
		t := &s.Threads[i]
		if t.ProjectID == projectID && t.Branch != nil && *t.Branch == branch {
			return t
		}
	}
	return nil
}

// NewID returns a random UUIDv4 string. t3 entity ids are branded non-empty
// strings, so any UUID is acceptable.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// Now returns the current time formatted the way t3 clients send createdAt.
func Now() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}
