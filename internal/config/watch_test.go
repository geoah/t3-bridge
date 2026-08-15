package config

import (
	"os"
	"path/filepath"
	"testing"
)

const validConfig = `repos:
  - repo: o/n
    assignee: geoah
    workspaceRoot: /tmp/n
`

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// watch is Watch with the error handling every test would repeat.
func watch(t *testing.T, path string) *Watcher {
	t.Helper()
	_, w, err := Watch(path)
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWatcherQuietUntilFileChanges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, validConfig)

	w := watch(t, path)
	cfg, err := w.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if cfg != nil {
		t.Fatal("reloaded an unchanged file")
	}

	write(t, path, validConfig+"    branchPrefix: custom/\n")
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

// A same-length edit written in the same clock tick is exactly what a
// timestamp-and-size stamp misses, so the watcher compares content.
func TestWatcherCatchesSameSizeEdit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, validConfig+"    branchPrefix: aaaaaa/\n")
	w := watch(t, path)

	write(t, path, validConfig+"    branchPrefix: bbbbbb/\n")
	cfg, err := w.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("missed an edit that kept the file the same length")
	}
	if got := cfg.Repos[0].BranchPrefix; got != "bbbbbb/" {
		t.Fatalf("branchPrefix = %q, want bbbbbb/", got)
	}
}

// Watch parses the same bytes it remembers, so an edit landing between the
// daemon's initial load and its first poll still gets picked up.
func TestWatchDoesNotMissEditDuringStartup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, validConfig)

	booted, w, err := Watch(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := booted.Repos[0].BranchPrefix; got != "t3/" {
		t.Fatalf("booted branchPrefix = %q, want the t3/ default", got)
	}

	write(t, path, validConfig+"    branchPrefix: edited/\n")
	cfg, err := w.Reload()
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("edit made just after startup was never reloaded")
	}
	if got := cfg.Repos[0].BranchPrefix; got != "edited/" {
		t.Fatalf("branchPrefix = %q, want edited/", got)
	}
}

func TestWatcherReportsBadConfigOnce(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	write(t, path, validConfig)
	w := watch(t, path)

	write(t, path, "repos:\n  - repo: o/n\n    nosuchfield: 1\n")
	if _, err := w.Reload(); err == nil {
		t.Fatal("want an error for an unknown field")
	}
	// The daemon polls every tick; a file that stays broken must not log
	// the same error forever.
	if cfg, err := w.Reload(); err != nil || cfg != nil {
		t.Fatalf("broken file reported twice: cfg=%v err=%v", cfg, err)
	}

	write(t, path, validConfig)
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
	write(t, path, validConfig)
	w := watch(t, path)

	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Reload(); err == nil {
		t.Fatal("want an error for a missing file")
	}
	if cfg, err := w.Reload(); err != nil || cfg != nil {
		t.Fatalf("missing file reported twice: cfg=%v err=%v", cfg, err)
	}

	write(t, path, validConfig)
	if cfg, err := w.Reload(); err != nil || cfg == nil {
		t.Fatalf("did not reload after the file came back: cfg=%v err=%v", cfg, err)
	}
}

func TestRestartRequiredNamesStartupOnlySettings(t *testing.T) {
	booted := &Config{}
	booted.T3.BaseURL = "http://127.0.0.1:3773"
	booted.UI.Listen = "127.0.0.1:3775"

	same := *booted
	if got := RestartRequired(booted, &same); got != nil {
		t.Fatalf("RestartRequired on identical configs = %v", got)
	}

	next := *booted
	next.UI.Listen = "127.0.0.1:9999"
	got := RestartRequired(booted, &next)
	if len(got) != 1 || got[0] != "ui.listen" {
		t.Fatalf("RestartRequired = %v, want [ui.listen]", got)
	}
}

// Comparing against the booted config, not the previously loaded one, means
// changing a startup-only setting and changing it back stops warning.
func TestRestartRequiredClearsWhenSettingIsPutBack(t *testing.T) {
	booted := &Config{}
	booted.UI.Listen = "127.0.0.1:3775"

	edited := *booted
	edited.UI.Listen = "127.0.0.1:9999"
	if got := RestartRequired(booted, &edited); len(got) != 1 {
		t.Fatalf("RestartRequired after the edit = %v, want [ui.listen]", got)
	}

	restored := *booted
	if got := RestartRequired(booted, &restored); got != nil {
		t.Fatalf("RestartRequired after putting the value back = %v, want nil", got)
	}
}

func TestRestartRequiredIgnoresHotReloadableSettings(t *testing.T) {
	booted := &Config{Repos: []RepoConfig{{Repo: "o/n"}}}
	next := &Config{Repos: []RepoConfig{{Repo: "o/n", Model: &ModelConfig{Model: "claude-opus-5"}}}}
	next.Poll.IntervalSeconds = 5
	if got := RestartRequired(booted, next); got != nil {
		t.Fatalf("RestartRequired = %v, want nil: model and poll interval apply on reload", got)
	}
}
