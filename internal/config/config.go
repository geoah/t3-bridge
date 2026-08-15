// Package config loads the t3-bridge YAML configuration.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/geoah/t3-bridge/internal/t3"
	"gopkg.in/yaml.v3"
)

type Config struct {
	T3     T3Config     `yaml:"t3"`
	State  StateConfig  `yaml:"state"`
	Poll   PollConfig   `yaml:"poll"`
	UI     UIConfig     `yaml:"ui"`
	Prompt PromptConfig `yaml:"prompt"`
	Repos  []RepoConfig `yaml:"repos"`
}

// PromptConfig holds extra instructions appended to every prompt the daemon
// sends to a session.
type PromptConfig struct {
	Suffix string `yaml:"suffix,omitempty"`
}

type UIConfig struct {
	// Listen is the address of the monitoring UI, default 127.0.0.1:3775.
	// Set to "off" to disable.
	Listen string `yaml:"listen"`
}

type T3Config struct {
	BaseURL   string `yaml:"baseUrl"`
	TokenFile string `yaml:"tokenFile"`
	// WorktreesDir is where session worktrees are created, matching t3's
	// own layout so they show up normally in its UI.
	WorktreesDir string `yaml:"worktreesDir"`
}

type StateConfig struct {
	File string `yaml:"file"`
}

type PollConfig struct {
	IntervalSeconds int `yaml:"intervalSeconds"`
}

// ReviewTrigger selects which PR feedback wakes the session again.
type ReviewTrigger string

const (
	// TriggerChangesRequested reacts only to reviews submitted with
	// "Request changes". Note GitHub forbids these on your own PRs, so
	// single-account setups should use TriggerAnyReview.
	TriggerChangesRequested ReviewTrigger = "changes_requested"
	// TriggerAnyReview additionally reacts to comment-only reviews
	// (state COMMENTED), which GitHub does allow on your own PRs.
	TriggerAnyReview ReviewTrigger = "any_review"
)

type RepoConfig struct {
	// Repo is the GitHub repository as "owner/name".
	Repo string `yaml:"repo"`
	// Assignee is the GitHub login whose assigned issues get picked up.
	Assignee string `yaml:"assignee"`
	// WorkspaceRoot locates the matching t3 project. Either this or
	// ProjectID must be set.
	WorkspaceRoot string `yaml:"workspaceRoot,omitempty"`
	ProjectID     string `yaml:"projectId,omitempty"`
	// BaseBranch for new worktrees; defaults to the repo default branch.
	BaseBranch string `yaml:"baseBranch,omitempty"`
	// Model overrides the project default model for sessions on this repo.
	Model *ModelConfig `yaml:"model,omitempty"`
	// ReviewTrigger defaults to changes_requested.
	ReviewTrigger ReviewTrigger `yaml:"reviewTrigger,omitempty"`
	// BranchPrefix for session branches; defaults to "t3/".
	BranchPrefix string `yaml:"branchPrefix,omitempty"`
	// PromptSuffix is appended after the global prompt.suffix for this repo.
	PromptSuffix string `yaml:"promptSuffix,omitempty"`
}

// PromptSuffixFor returns the extra instructions for a repo: the global
// suffix first, then the repo's own.
func (c *Config) PromptSuffixFor(rc *RepoConfig) string {
	parts := make([]string, 0, 2)
	for _, s := range []string{c.Prompt.Suffix, rc.PromptSuffix} {
		if s = strings.TrimSpace(s); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, "\n\n")
}

// ModelConfig is the friendly config form of a t3 model selection.
type ModelConfig struct {
	// Model is a t3 model id, for example claude-opus-5.
	Model string `yaml:"model"`
	// InstanceID is the provider instance, default claudeAgent.
	InstanceID string `yaml:"instanceId,omitempty"`
	// Effort is the reasoning effort, for example high.
	Effort string `yaml:"effort,omitempty"`
	// ContextWindow, for example 1m.
	ContextWindow string `yaml:"contextWindow,omitempty"`
}

func (m *ModelConfig) Selection() t3.ModelSelection {
	sel := t3.ModelSelection{InstanceID: m.InstanceID, Model: m.Model}
	if sel.InstanceID == "" {
		sel.InstanceID = "claudeAgent"
	}
	if m.Effort != "" {
		sel.Options = append(sel.Options, t3.ModelOption{ID: "effort", Value: m.Effort})
	}
	if m.ContextWindow != "" {
		sel.Options = append(sel.Options, t3.ModelOption{ID: "contextWindow", Value: m.ContextWindow})
	}
	return sel
}

func (r *RepoConfig) Owner() string {
	owner, _, _ := strings.Cut(r.Repo, "/")
	return owner
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, path[2:])
		}
	}
	return path
}

func DefaultPath() string {
	return expandHome("~/.config/t3-bridge/config.yaml")
}

func Load(path string) (*Config, error) {
	path = expandHome(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return parse(data, path)
}

// parse turns already-read config bytes into a Config. Watch reads the file
// itself so it can remember the exact bytes it parsed, so the reading and
// the parsing are kept separate.
func parse(data []byte, path string) (*Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("config %s: %w", path, err)
	}
	if cfg.T3.BaseURL == "" {
		cfg.T3.BaseURL = t3.DefaultBaseURL
	}
	if cfg.T3.TokenFile == "" {
		cfg.T3.TokenFile = "~/.config/t3-bridge/token"
	}
	cfg.T3.TokenFile = expandHome(cfg.T3.TokenFile)
	if cfg.T3.WorktreesDir == "" {
		cfg.T3.WorktreesDir = "~/.t3/worktrees"
	}
	cfg.T3.WorktreesDir = expandHome(cfg.T3.WorktreesDir)
	if cfg.State.File == "" {
		cfg.State.File = "~/.local/state/t3-bridge/state.json"
	}
	cfg.State.File = expandHome(cfg.State.File)
	if cfg.Poll.IntervalSeconds <= 0 {
		cfg.Poll.IntervalSeconds = 60
	}
	if cfg.UI.Listen == "" {
		cfg.UI.Listen = "127.0.0.1:3775"
	}
	if len(cfg.Repos) == 0 {
		return nil, fmt.Errorf("config %s: no repos configured", path)
	}
	for i := range cfg.Repos {
		rc := &cfg.Repos[i]
		if !strings.Contains(rc.Repo, "/") {
			return nil, fmt.Errorf("config: repo %q must be owner/name", rc.Repo)
		}
		if rc.Assignee == "" {
			return nil, fmt.Errorf("config: repo %s: assignee is required", rc.Repo)
		}
		if rc.WorkspaceRoot == "" && rc.ProjectID == "" {
			return nil, fmt.Errorf("config: repo %s: workspaceRoot or projectId is required", rc.Repo)
		}
		rc.WorkspaceRoot = expandHome(rc.WorkspaceRoot)
		if rc.ReviewTrigger == "" {
			rc.ReviewTrigger = TriggerChangesRequested
		}
		if rc.ReviewTrigger != TriggerChangesRequested && rc.ReviewTrigger != TriggerAnyReview {
			return nil, fmt.Errorf("config: repo %s: invalid reviewTrigger %q", rc.Repo, rc.ReviewTrigger)
		}
		if rc.BranchPrefix == "" {
			rc.BranchPrefix = "t3/"
		}
		if rc.Model != nil && rc.Model.Model == "" {
			return nil, fmt.Errorf("config: repo %s: model.model is required when model is set", rc.Repo)
		}
	}
	return &cfg, nil
}

// Token reads the bearer token from the configured token file.
func (c *Config) Token() (string, error) {
	data, err := os.ReadFile(c.T3.TokenFile)
	if err != nil {
		return "", fmt.Errorf("read t3 token (mint one with: t3 auth session issue --ttl 90d --label t3-bridge --token-only > %s): %w", c.T3.TokenFile, err)
	}
	return strings.TrimSpace(string(data)), nil
}
