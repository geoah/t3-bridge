// Package state persists the issue -> thread -> PR mapping so the daemon is
// idempotent across restarts. Writes are atomic (temp file + rename).
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type Item struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issueNumber"`
	IssueTitle  string `json:"issueTitle,omitempty"`
	ThreadID    string `json:"threadId"`
	Branch      string `json:"branch"`
	PRNumber    int    `json:"prNumber,omitempty"`
	// HandledReviewIDs are GitHub review ids already forwarded to the session.
	HandledReviewIDs []int64 `json:"handledReviewIds,omitempty"`
	// NudgeCount tracks reminders sent when a completed turn left no PR.
	NudgeCount int       `json:"nudgeCount,omitempty"`
	Done       bool      `json:"done,omitempty"`
	DoneReason string    `json:"doneReason,omitempty"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

func (it *Item) Key() string {
	return Key(it.Repo, it.IssueNumber)
}

func Key(repo string, issue int) string {
	return fmt.Sprintf("%s#%d", repo, issue)
}

func (it *Item) ReviewHandled(id int64) bool {
	return slices.Contains(it.HandledReviewIDs, id)
}

type State struct {
	path  string
	Items map[string]*Item `json:"items"`
}

func Load(path string) (*State, error) {
	st := &State{path: path, Items: map[string]*Item{}}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return st, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, st); err != nil {
		return nil, fmt.Errorf("state %s: %w", path, err)
	}
	if st.Items == nil {
		st.Items = map[string]*Item{}
	}
	return st, nil
}

func (s *State) Save() error {
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *State) Get(repo string, issue int) *Item {
	return s.Items[Key(repo, issue)]
}

func (s *State) Put(it *Item) {
	it.UpdatedAt = time.Now().UTC()
	if it.CreatedAt.IsZero() {
		it.CreatedAt = it.UpdatedAt
	}
	s.Items[it.Key()] = it
}
