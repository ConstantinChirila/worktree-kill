package wt

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// RemoveOptions controls how Remove deletes a worktree.
type RemoveOptions struct {
	// Force discards uncommitted changes.
	Force bool
	// DeleteBranch also runs `git branch -d`, which refuses unmerged branches.
	DeleteBranch bool
	// Protect names folders an orphan's deletion must leave alone. See
	// ScanOptions.Protection.
	Protect Protection
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
		// No repo left to ask, so git cannot remove it. Delete the directory.
		if err := removeOrphan(w, opts); err != nil {
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

		if err := removeStaleEntry(ctx, w); err != nil {
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

// removeStaleEntry drops git's entry for a worktree whose folder is missing,
// and only that one (`git worktree prune` would drop every stale entry). No
// --force: if the folder comes back after Remove checked, git checks it for
// changes itself and refuses instead of discarding them.
func removeStaleEntry(ctx context.Context, w Worktree) error {
	_, err := git(ctx, "--git-dir", w.CommonDir, "worktree", "remove", "--", w.Path)
	return err
}

// removeCheckout runs `git worktree remove`, which checks for changes the way
// `git status` does, so it goes through gitFor. A locked worktree is unlocked
// first instead of being removed with a double --force, which would also skip
// git's check for uncommitted changes. If removal fails, the lock is restored.
func removeCheckout(ctx context.Context, w Worktree, force bool) error {
	g, err := gitFor(ctx, w)
	if err != nil {
		return err
	}

	if w.Locked {
		if _, err := g.run("--git-dir", w.CommonDir, "worktree", "unlock", "--", w.Path); err != nil {
			return err
		}
	}

	args := []string{"--git-dir", w.CommonDir, "worktree", "remove"}
	if force {
		args = append(args, "--force")
	}

	_, err = g.run(append(args, "--", w.Path)...)
	if err != nil && w.Locked {
		relock := []string{"--git-dir", w.CommonDir, "worktree", "lock"}
		if w.LockReason != "" {
			relock = append(relock, "--reason", w.LockReason)
		}

		if _, lockErr := g.run(append(relock, "--", w.Path)...); lockErr != nil {
			return fmt.Errorf("%w (and it is no longer locked: %v)", err, lockErr)
		}
	}

	return err
}

// removeOrphan deletes an orphan's folder after checking, right before, that
// it is still safe to. The tool may have been open for hours since the scan.
//
// The parent folder is opened once, and the checks on the folder's contents
// and the delete all go through that handle, which never follows symlinks out
// of it. So swapping the folder, or anything in it, for a symlink can't make
// the checks look at one place and the delete happen in another.
func removeOrphan(w Worktree, opts RemoveOptions) error {
	if err := opts.Protect.check(w.Path); err != nil {
		return err
	}

	root, err := os.OpenRoot(filepath.Dir(w.Path))
	if err != nil {
		return err
	}

	defer root.Close()
	name := filepath.Base(w.Path)
	notOrphaned := errors.New("no longer looks orphaned; scan again")
	if info, err := root.Lstat(name); err != nil || !info.IsDir() {
		return notOrphaned
	}

	content, err := root.ReadFile(filepath.Join(name, ".git"))
	if err != nil {
		return notOrphaned
	}

	gitDir, ok := parseDotGitFile(content, w.Path)
	if _, statErr := os.Stat(gitDir); !ok || gitDir != w.GitDir || !errors.Is(statErr, os.ErrNotExist) || !repoDeleted(w.Path, gitDir) {
		return notOrphaned
	}

	if containsGit(root.FS(), name) {
		return errors.New("it contains another repo or worktree, or a folder that can't be read; delete it by hand")
	}

	return root.RemoveAll(name)
}

// containsGit reports whether any folder below dir in fsys holds a .git entry
// (a repo or worktree that deleting dir would take with it), or can't be read.
func containsGit(fsys fs.FS, dir string) bool {
	found := false
	_ = fs.WalkDir(fsys, dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || (entry.Name() == ".git" && filepath.Dir(path) != dir) {
			found = true
			return fs.SkipAll
		}

		return nil
	})

	return found
}
