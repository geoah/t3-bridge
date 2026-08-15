package config

import (
	"bytes"
	"os"
)

// Watcher reports when a config file changes on disk so the daemon can
// reload it without a restart. It compares the file's bytes, which costs
// nothing at this size and, unlike a timestamp, cannot miss an edit that
// lands within the same clock tick or leaves the file the same length.
type Watcher struct {
	path string
	last []byte
	// gone records that the file was missing or unreadable at the last
	// check, so the problem is reported once rather than every poll.
	gone bool
}

// Watch loads a config file and returns a watcher primed with the exact
// bytes that produced the returned config. Reading once for both closes the
// window where an edit landing during startup would be mistaken for the
// config already in memory and never reloaded.
func Watch(path string) (*Config, *Watcher, error) {
	path = expandHome(path)
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	cfg, err := parse(data, path)
	if err != nil {
		return nil, nil, err
	}
	return cfg, &Watcher{path: path, last: data}, nil
}

// Path is the file being watched, with ~ already expanded.
func (w *Watcher) Path() string { return w.path }

// Reload returns the new config if the file changed since the last call, and
// nil if it did not. The caller keeps using its current config on error.
//
// Changed content is recorded before it is parsed, so a config with a typo
// is reported once instead of on every poll until it is fixed.
func (w *Watcher) Reload() (*Config, error) {
	data, err := os.ReadFile(w.path)
	if err != nil {
		if w.gone {
			return nil, nil
		}
		w.gone = true
		return nil, err
	}
	if !w.gone && bytes.Equal(data, w.last) {
		return nil, nil
	}
	w.gone, w.last = false, data
	return parse(data, w.path)
}

// RestartRequired lists the settings that differ between the config the
// daemon started with and a newly loaded one, but only take effect at
// startup. The daemon builds the t3 client, loads the state file and starts
// the ui server once, so a reload cannot apply these.
//
// The comparison is against the config the process booted with, not the
// previously loaded one, so editing a setting and putting it back does not
// ask for a restart that is not needed.
func RestartRequired(booted, next *Config) []string {
	var changed []string
	for _, s := range []struct {
		name         string
		booted, next string
	}{
		{"t3.baseUrl", booted.T3.BaseURL, next.T3.BaseURL},
		{"t3.tokenFile", booted.T3.TokenFile, next.T3.TokenFile},
		{"state.file", booted.State.File, next.State.File},
		{"ui.listen", booted.UI.Listen, next.UI.Listen},
	} {
		if s.booted != s.next {
			changed = append(changed, s.name)
		}
	}
	return changed
}
