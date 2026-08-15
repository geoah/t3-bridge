package bridge

import (
	"strings"
	"testing"

	"github.com/geoah/t3-bridge/internal/config"
	"github.com/geoah/t3-bridge/internal/gh"
)

func TestPromptSuffixOrdering(t *testing.T) {
	cfg := &config.Config{Prompt: config.PromptConfig{Suffix: "GLOBAL RULE"}}
	rc := &config.RepoConfig{Repo: "o/n", PromptSuffix: "REPO RULE"}
	got := withSuffix(issuePrompt("o/n", &gh.Issue{Number: 7, Title: "t"}, "t3/issue-7"), cfg.PromptSuffixFor(rc))
	if !strings.Contains(got, "Additional instructions:") {
		t.Fatal("missing suffix header")
	}
	gi, ri := strings.Index(got, "GLOBAL RULE"), strings.Index(got, "REPO RULE")
	if gi < 0 || ri < 0 || gi > ri {
		t.Fatalf("suffixes wrong: global=%d repo=%d", gi, ri)
	}
	if strings.Index(got, "Implement GitHub issue") > gi {
		t.Fatal("suffix must come after the body")
	}
}

func TestNoSuffixIsUnchanged(t *testing.T) {
	cfg := &config.Config{}
	rc := &config.RepoConfig{Repo: "o/n"}
	base := issuePrompt("o/n", &gh.Issue{Number: 7, Title: "t"}, "t3/issue-7")
	if got := withSuffix(base, cfg.PromptSuffixFor(rc)); got != base {
		t.Fatal("prompt changed with no suffix configured")
	}
}
