package bridge

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// ensureWorktree creates (or adopts) a git worktree for branch, mirroring
// t3's own conventions: the worktree lives at
// <worktreesDir>/<repoName>/<branch with "/" replaced by "-"> and the branch
// records its merge base in git config, which t3's PR view uses.
func ensureWorktree(ctx context.Context, projectCwd, worktreesDir, baseBranch, branch string) (path string, created bool, err error) {
	// Freshen the base branch; a stale ref is survivable, so fetch
	// failures (offline, auth) only degrade to the local ref.
	fetchErr := runGit(ctx, projectCwd, "fetch", "--quiet", "origin", baseBranch)
	startRef := "origin/" + baseBranch
	if err := runGit(ctx, projectCwd, "rev-parse", "--verify", "--quiet", startRef); err != nil {
		if fetchErr != nil {
			startRef = baseBranch
		} else {
			return "", false, fmt.Errorf("base ref %s not found in %s", startRef, projectCwd)
		}
	}

	path = filepath.Join(worktreesDir, filepath.Base(projectCwd), strings.ReplaceAll(branch, "/", "-"))
	if _, statErr := os.Stat(path); statErr == nil {
		head, err := outGit(ctx, path, "rev-parse", "--abbrev-ref", "HEAD")
		if err == nil && head == branch {
			return path, false, nil
		}
		return "", false, fmt.Errorf("worktree path %s exists but is not on branch %s", path, branch)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", false, err
	}

	branchExists := runGit(ctx, projectCwd, "rev-parse", "--verify", "--quiet", "refs/heads/"+branch) == nil
	if branchExists {
		err = runGit(ctx, projectCwd, "worktree", "add", path, branch)
	} else {
		err = runGit(ctx, projectCwd, "worktree", "add", "-b", branch, path, startRef)
	}
	if err != nil {
		return "", false, fmt.Errorf("git worktree add: %w", err)
	}
	// Best effort, mirrors t3's createWorktree.
	_ = runGit(ctx, projectCwd, "config", "branch."+branch+".gh-merge-base", baseBranch)
	return path, true, nil
}

// removeWorktree undoes a worktree this daemon just created.
func removeWorktree(ctx context.Context, projectCwd, path, branch string) {
	_ = runGit(ctx, projectCwd, "worktree", "remove", "--force", path)
	_ = runGit(ctx, projectCwd, "branch", "-D", branch)
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if len(msg) > 300 {
			msg = msg[:300]
		}
		return fmt.Errorf("git %s: %w: %s", args[0], err, msg)
	}
	return nil
}

func outGit(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
