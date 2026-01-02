package worktree

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Create prepares git worktrees rooted at repoPath pointing to HEAD.
func Create(ctx context.Context, repoPath string, worktreePaths []string) error {
	return CreateFromRef(ctx, repoPath, worktreePaths, "HEAD")
}

// CreateFromRef prepares git worktrees rooted at repoPath pointing to the given ref.
func CreateFromRef(ctx context.Context, repoPath string, worktreePaths []string, ref string) error {
	repoPath, _ = filepath.Abs(repoPath)

	if err := runGit(ctx, repoPath, "worktree", "prune"); err != nil {
		return err
	}

	for _, wt := range worktreePaths {
		if _, err := os.Stat(wt); err == nil {
			_ = Remove(ctx, repoPath, wt)
		}
		if err := runGit(ctx, repoPath, "worktree", "add", "--detach", wt, ref); err != nil {
			return fmt.Errorf("create worktree %s: %w", wt, err)
		}
	}

	return nil
}

// Remove deletes a git worktree and its directory.
func Remove(ctx context.Context, repoPath, worktreePath string) error {
	_ = runGit(ctx, repoPath, "worktree", "remove", "--force", worktreePath)
	if err := os.RemoveAll(worktreePath); err != nil {
		return fmt.Errorf("remove worktree directory: %w", err)
	}
	return nil
}

func runGit(ctx context.Context, dir string, args ...string) error {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, stderr.String())
	}
	return nil
}
