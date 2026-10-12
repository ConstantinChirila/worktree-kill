package wt

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// git runs a git command. Repos found by a scan may be untrusted (an extracted
// archive, say), and a repo's own config can name programs for git to run.
// core.fsmonitor is the one that `git status` runs on every call, so it is
// switched off. An empty value disables it on both old and new git. Commands
// that read files in a worktree go through gitFor, which also switches off
// the repo's filters.
func git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-c", "core.fsmonitor="}, args...)...)
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

// gitFor returns a git runner for a worktree that switches off the filter
// drivers its repo's own config defines (`git status` runs a filter's clean
// command on files whose timestamps changed). Filters from your global or
// system config, like git-lfs, keep working. A repo that sets one up only in
// its own config may show files as changed that aren't, which errs towards
// warning. Needs git 2.26+ for --show-scope.
func gitFor(ctx context.Context, w Worktree) (func(args ...string) (string, error), error) {
	out, err := git(ctx, "-C", w.Path, "config", "--show-scope", "--get-regexp", `^filter\.`)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// No filters configured anywhere.
		out, err = "", nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading repo config: %w", err)
	}

	var overrides []string
	seen := map[string]bool{}
	for line := range strings.Lines(out) {
		// Lines look like "local\tfilter.<driver>.clean <command>".
		scope, entry, _ := strings.Cut(strings.TrimSpace(line), "\t")
		key, _, _ := strings.Cut(entry, " ")
		if scope == "global" || scope == "system" || scope == "command" {
			continue
		}

		// Driver names may contain dots, so cut at the last one.
		driver := strings.TrimPrefix(key, "filter.")
		if i := strings.LastIndex(driver, "."); i > 0 {
			driver = driver[:i]
		}

		if strings.Contains(driver, "=") {
			// `-c` splits at the first "=", so this one can't be overridden.
			return nil, fmt.Errorf("repo config defines a filter git can't be told to skip: %q", driver)
		}

		if seen[driver] {
			continue
		}

		seen[driver] = true
		for _, setting := range []string{"clean=", "smudge=", "process=", "required=false"} {
			overrides = append(overrides, "-c", "filter."+driver+"."+setting)
		}
	}

	return func(args ...string) (string, error) {
		return git(ctx, append(slices.Clone(overrides), args...)...)
	}, nil
}

// GitInfo is a worktree's git status.
type GitInfo struct {
	// Changes counts entries in `git status --porcelain` (modified + untracked).
	Changes int
	// Unpushed counts commits on HEAD that are on no remote branch and not on
	// the repo's main branch: work that exists only in this worktree.
	Unpushed   int
	LastCommit time.Time
}

// InspectGit gathers status for a worktree whose directory and repo exist.
func InspectGit(ctx context.Context, w Worktree) (GitInfo, error) {
	if w.State != StateOK {
		return GitInfo{}, nil
	}

	var info GitInfo
	run, err := gitFor(ctx, w)
	if err != nil {
		return info, err
	}

	status, err := run("-C", w.Path, "status", "--porcelain")
	if err != nil {
		return info, err
	}

	for line := range strings.Lines(status) {
		if strings.TrimSpace(line) != "" {
			info.Changes++
		}
	}

	// log.showSignature in the repo's config would run its gpg.program.
	if out, err := run("-C", w.Path, "log", "-1", "--no-show-signature", "--format=%ct"); err == nil {
		if secs, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64); err == nil {
			info.LastCommit = time.Unix(secs, 0)
		}
	}

	args := append([]string{"-C", w.Path, "rev-list", "--count", "HEAD", "--not", "--remotes"}, mainBranches(run, w)...)
	if out, err := run(args...); err == nil {
		info.Unpushed, _ = strconv.Atoi(strings.TrimSpace(out))
	}

	return info, nil
}

// mainBranches returns the repo's local main and/or master branch refs. If it
// has neither, it falls back to the branch checked out in the main worktree
// (or a bare repo's default branch).
func mainBranches(run func(args ...string) (string, error), w Worktree) []string {
	out, _ := run("-C", w.Path, "for-each-ref", "--format=%(refname)", "refs/heads/main", "refs/heads/master")
	var refs []string
	for line := range strings.Lines(out) {
		// Patterns also match below a slash (refs/heads/main/x), so check exactly.
		if ref := strings.TrimSpace(line); ref == "refs/heads/main" || ref == "refs/heads/master" {
			refs = append(refs, ref)
		}
	}

	if len(refs) > 0 {
		return refs
	}

	if head, err := os.ReadFile(filepath.Join(w.CommonDir, "HEAD")); err == nil {
		if ref, ok := branchRef(head); ok {
			return []string{ref}
		}
	}

	return nil
}

// RemoveOptions controls how Remove deletes a worktree.
type RemoveOptions struct {
	// Force discards uncommitted changes.
	Force bool
	// DeleteBranch also runs `git branch -d`, which refuses unmerged branches.
	DeleteBranch bool
	// Protected paths are never deleted outright, and neither is any folder
	// containing one. See ScanOptions.Protected.
	Protected []string
}

// RemoveResult reports what happened to the branch. Removal errors are
// returned separately; a kept branch is not an error.
type RemoveResult struct {
	BranchDeleted bool
	// BranchError explains why the branch was kept, if deletion was requested.
	BranchError error
}

// Remove deletes a worktree. Each state is checked again first, since the
// worktree may have changed since it was scanned.
func Remove(ctx context.Context, w Worktree, opts RemoveOptions) (RemoveResult, error) {
	switch w.State {
	case StateOrphaned:
		if err := checkStillOrphaned(w, opts.Protected); err != nil {
			return RemoveResult{}, err
		}

		// No repo left to ask, so git cannot remove it. Delete the directory.
		if err := os.RemoveAll(w.Path); err != nil {
			return RemoveResult{}, err
		}

		return RemoveResult{}, nil
	case StateMissing:
		if w.Locked {
			// Git locks worktrees on removable drives so they survive pruning.
			return RemoveResult{}, errors.New("locked and its folder is missing, maybe on a drive that isn't mounted; run git worktree unlock to allow this")
		}

		if _, err := os.Lstat(w.Path); !errors.Is(err, os.ErrNotExist) {
			return RemoveResult{}, errors.New("its folder is back; scan again")
		}

		// Drop only this worktree's stale entry (`prune` would drop every one).
		if _, err := git(ctx, "--git-dir", w.CommonDir, "worktree", "remove", "--force", "--", w.Path); err != nil {
			return RemoveResult{}, err
		}
	default:
		if err := removeCheckout(ctx, w, opts.Force); err != nil {
			return RemoveResult{}, err
		}
	}

	if !opts.DeleteBranch || w.Branch == "" {
		return RemoveResult{}, nil
	}

	if _, err := git(ctx, "--git-dir", w.CommonDir, "branch", "-d", "--", w.Branch); err != nil {
		return RemoveResult{BranchError: fmt.Errorf("branch kept: %w", err)}, nil
	}

	return RemoveResult{BranchDeleted: true}, nil
}

// removeCheckout runs `git worktree remove`, which checks for changes the way
// `git status` does, so it goes through gitFor. A locked worktree is unlocked
// first instead of being removed with a double --force, which would also skip
// git's check for uncommitted changes. If removal fails, the lock is restored.
func removeCheckout(ctx context.Context, w Worktree, force bool) error {
	run, err := gitFor(ctx, w)
	if err != nil {
		return err
	}

	if w.Locked {
		if _, err := run("--git-dir", w.CommonDir, "worktree", "unlock", "--", w.Path); err != nil {
			return err
		}
	}

	args := []string{"--git-dir", w.CommonDir, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}

	_, err = run(append(args, "--", w.Path)...)
	if err != nil && w.Locked {
		relock := []string{"--git-dir", w.CommonDir, "worktree", "lock"}
		if w.LockReason != "" {
			relock = append(relock, "--reason", w.LockReason)
		}

		if _, lockErr := run(append(relock, "--", w.Path)...); lockErr != nil {
			return fmt.Errorf("%w (and it is no longer locked: %v)", err, lockErr)
		}
	}

	return err
}

// checkStillOrphaned re-reads an orphan's .git file right before deletion, and
// refuses to delete a folder that contains a protected path.
func checkStillOrphaned(w Worktree, protected []string) error {
	if containsProtected(w.Path, protected) {
		return fmt.Errorf("refusing to delete %s: it is or contains a scanned folder or your home folder", w.Path)
	}

	current, err := fromDotGitFile(w.Path)
	if err != nil || current.State != StateOrphaned || current.GitDir != w.GitDir {
		return errors.New("no longer looks orphaned; scan again")
	}

	return nil
}
