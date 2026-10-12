package wt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	// Keep read-only commands like `status` from taking the index lock.
	cmd.Env = append(os.Environ(), "GIT_OPTIONAL_LOCKS=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			return "", err
		}

		// Git prefixes errors with "fatal:" or "error:"; keep the first line only.
		msg, _, _ = strings.Cut(msg, "\n")
		msg = strings.TrimPrefix(strings.TrimPrefix(msg, "fatal: "), "error: ")
		return "", errors.New(msg)
	}

	return stdout.String(), nil
}

type GitInfo struct {
	// Changes counts entries in `git status --porcelain` (modified + untracked).
	Changes int
	// Unpushed counts commits on HEAD that are on neither the main branch nor
	// any remote branch: work that exists only in this worktree.
	Unpushed   int
	LastCommit time.Time
}

// InspectGit gathers status for a worktree whose directory and repo exist.
func InspectGit(ctx context.Context, w Worktree) (GitInfo, error) {
	if w.State != StateOK {
		return GitInfo{}, nil
	}

	var info GitInfo
	status, err := git(ctx, "-C", w.Path, "status", "--porcelain")
	if err != nil {
		return info, err
	}

	for line := range strings.Lines(status) {
		if strings.TrimSpace(line) != "" {
			info.Changes++
		}
	}

	if out, err := git(ctx, "-C", w.Path, "log", "-1", "--format=%ct"); err == nil {
		if secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			info.LastCommit = time.Unix(secs, 0)
		}
	}

	args := []string{"-C", w.Path, "rev-list", "--count", "HEAD", "--not", "--remotes"}
	if mainRef := mainBranchRef(w.CommonDir); mainRef != "" {
		args = append(args, mainRef)
	}

	if out, err := git(ctx, args...); err == nil {
		info.Unpushed, _ = strconv.Atoi(strings.TrimSpace(out))
	}

	return info, nil
}

// mainBranchRef is the branch checked out in the main worktree (or a bare
// repo's default branch), read from the shared HEAD file.
func mainBranchRef(commonDir string) string {
	head, err := os.ReadFile(filepath.Join(commonDir, "HEAD"))
	if err != nil {
		return ""
	}

	ref, ok := strings.CutPrefix(strings.TrimSpace(string(head)), "ref: ")
	if !ok {
		return ""
	}

	return ref
}

type RemoveOptions struct {
	// Force discards uncommitted changes and removes locked worktrees.
	Force bool
	// DeleteBranch also runs `git branch -d`, which refuses unmerged branches.
	DeleteBranch bool
}

type RemoveResult struct {
	BranchDeleted bool
	// BranchError explains why the branch was kept, if deletion was requested.
	BranchError error
}

func Remove(ctx context.Context, w Worktree, opts RemoveOptions) (RemoveResult, error) {
	switch w.State {
	case StateOrphaned:
		// No repo left to ask, so git cannot remove it. Delete the directory.
		if err := os.RemoveAll(w.Path); err != nil {
			return RemoveResult{}, err
		}

		return RemoveResult{}, nil
	case StateMissing:
		// Directory already gone: drop git's stale bookkeeping.
		if _, err := git(ctx, "--git-dir", w.CommonDir, "worktree", "prune"); err != nil {
			return RemoveResult{}, err
		}
	default:
		args := []string{"--git-dir", w.CommonDir, "worktree", "remove"}
		if opts.Force || w.Locked {
			args = append(args, "--force")
		}

		// A locked worktree needs --force twice.
		if w.Locked {
			args = append(args, "--force")
		}

		if _, err := git(ctx, append(args, w.Path)...); err != nil {
			return RemoveResult{}, err
		}
	}

	if !opts.DeleteBranch || w.Branch == "" {
		return RemoveResult{}, nil
	}

	if _, err := git(ctx, "--git-dir", w.CommonDir, "branch", "-d", w.Branch); err != nil {
		return RemoveResult{BranchError: fmt.Errorf("branch kept: %w", err)}, nil
	}

	return RemoveResult{BranchDeleted: true}, nil
}
