package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

const validConfig = `repos:
  - repo: o/n
    assignee: geoah
    workspaceRoot: /tmp/n
`

// write replaces the file and moves its modification time forward, because a
// test can easily rewrite a file twice within one filesystem timestamp tick.
func write(t *testing.T, path, body string, age time.Duration) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(age)
	if err := os.Chtimes(path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
}

func TestWatcherQuietUntilFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, validConfig, 0)

	w := NewWatcher(path)
	cfg, err := w.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatal("reloaded an unchanged file")
	}

	write(t, path, validConfig+"    branchPrefix: custom/\n", time.Second)
	cfg, err = w.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("did not reload a changed file")
	}
	if got := cfg.Repos[0].BranchPrefix; got != "custom/" {
		t.Fatalf("branchPrefix = %q, want custom/", got)
	}

	if cfg, err = w.Reload(); err != nil || cfg != nil {
		t.Fatalf("reload after no change: cfg=%v err=%v", cfg, err)
	}
}

func TestWatcherReportsBadConfigOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, validConfig, 0)
	w := NewWatcher(path)

	write(t, path, "repos:\n  - repo: o/n\n    nosuchfield: 1\n", time.Second)
	if _, err := w.Reload(); err == nil {
		t.Fatal("want an error for an unknown field")
	}
	// The daemon polls every tick; a file that stays broken must not log
	// the same error forever.
	if cfg, err := w.Reload(); err != nil || cfg != nil {
		t.Fatalf("broken file reported twice: cfg=%v err=%v", cfg, err)
	}

	write(t, path, validConfig, 2*time.Second)
	cfg, err := w.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("did not reload after the file was fixed")
	}
}

func TestWatcherHandlesMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, validConfig, 0)
	w := NewWatcher(path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Reload(); err == nil {
		t.Fatal("want an error for a missing file")
	}
	if cfg, err := w.Reload(); err != nil || cfg != nil {
		t.Fatalf("missing file reported twice: cfg=%v err=%v", cfg, err)
	}

	write(t, path, validConfig, time.Second)
	if cfg, err := w.Reload(); err != nil || cfg == nil {
		t.Fatalf("did not reload after the file came back: cfg=%v err=%v", cfg, err)
	}
}

func TestRestartRequiredNamesStartupOnlySettings(t *testing.T) {
	old := &Config{}
	old.T3.BaseURL = "http://127.0.0.1:3773"
	old.UI.Listen = "127.0.0.1:3775"

	same := *old
	if got := RestartRequired(old, &same); got != nil {
		t.Fatalf("RestartRequired on identical configs = %v", got)
	}

	next := *old
	next.UI.Listen = "127.0.0.1:9999"
	got := RestartRequired(old, &next)
	if len(got) != 1 || got[0] != "ui.listen" {
		t.Fatalf("RestartRequired = %v, want [ui.listen]", got)
	}
}

func TestRestartRequiredIgnoresHotReloadableSettings(t *testing.T) {
	old := &Config{Repos: []RepoConfig{{Repo: "o/n"}}}
	next := &Config{Repos: []RepoConfig{{Repo: "o/n", Model: &ModelConfig{Model: "claude-opus-5"}}}}
	next.Poll.IntervalSeconds = 5
	if got := RestartRequired(old, next); got != nil {
		t.Fatalf("RestartRequired = %v, want nil: model and poll interval apply on reload", got)
	}
}
