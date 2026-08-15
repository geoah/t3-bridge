package config

import (
	"os"
	"time"
)

// Watcher reports when a config file changes on disk so the daemon can
// reload it without a restart. It compares the file's modification time and
// size, which is cheap enough to poll once per tick and needs no extra
// dependency. Editors that rewrite the file in place change the size or the
// modification time; editors that write a temp file and rename it produce a
// new file with its own modification time.
type Watcher struct {
	path string
	mod  time.Time
	size int64
}

// NewWatcher starts watching path, treating the file as it is now as already
// loaded, so the first Reload call is quiet.
func NewWatcher(path string) *Watcher {
	w := &Watcher{path: expandHome(path)}
	w.mod, w.size = stat(w.path)
	return w
}

// Path is the file being watched, with ~ already expanded.
func (w *Watcher) Path() string { return w.path }

// Reload returns the new config if the file changed since the last call, and
// nil if it did not. The caller keeps using its current config on error.
//
// A file that changed is recorded as seen before it is parsed, so a config
// with a typo is reported once rather than on every poll until it is fixed.
func (w *Watcher) Reload() (*Config, error) {
	mod, size := stat(w.path)
	if mod.Equal(w.mod) && size == w.size {
		return nil, nil
	}
	w.mod, w.size = mod, size
	return Load(w.path)
}

// stat returns the file's modification time and size, or zero values if it
// cannot be read. A missing file is therefore one distinct stamp: it is
// reported once, stays quiet while it is still missing, and reloads when it
// comes back.
func stat(path string) (time.Time, int64) {
	fi, err := os.Stat(path)
	if err != nil {
		return time.Time{}, 0
	}
	return fi.ModTime(), fi.Size()
}

// RestartRequired lists the settings that differ between two configs but only
// take effect at startup. The daemon builds the t3 client, loads the state
// file and starts the ui server once, so a reload cannot apply these.
func RestartRequired(old, next *Config) []string {
	var changed []string
	for _, s := range []struct {
		name     string
		old, new string
	}{
		{"t3.baseUrl", old.T3.BaseURL, next.T3.BaseURL},
		{"t3.tokenFile", old.T3.TokenFile, next.T3.TokenFile},
		{"state.file", old.State.File, next.State.File},
		{"ui.listen", old.UI.Listen, next.UI.Listen},
	} {
		if s.old != s.new {
			changed = append(changed, s.name)
		}
	}
	return changed
}
