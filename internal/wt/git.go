package wt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// safeConfig switches off repo config that would make the commands this
// package runs start other programs. Repos found by a scan may be untrusted
// (an extracted archive, say), and git reads each repo's own config:
//   - core.fsmonitor runs on every `git status`. An empty value disables it
//     on both old and new git.
//   - core.hooksPath at an empty location skips the repo's hooks, such as
//     reference-transaction during `git branch -d`.
//   - protocol.allow=never stops a "partial clone" repo from fetching a
//     missing object, which would run its core.sshCommand or remote helper.
//
// Filters are per-repo, so gitFor handles those. Signature checks are
// switched off per command (`log --no-show-signature`).
var safeConfig = []string{
	"-c", "core.fsmonitor=",
	"-c", "core.hooksPath=" + os.DevNull,
	"-c", "protocol.allow=never",
}

// gitEnv returns the environment for git: ours, minus variables that point
// git at another repo (set when worktreekill runs from a git hook, say).
func gitEnv() []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		switch name {
		case "GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_COMMON_DIR", "GIT_OBJECT_DIRECTORY",
			"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE", "GIT_PREFIX":
			return true
		}

		return false
	})

	// GIT_OPTIONAL_LOCKS=0 keeps read-only commands like `status` from taking
	// the index lock. GIT_NO_LAZY_FETCH backs up protocol.allow on git 2.45+.
	return append(env, "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1")
}

// git runs a git command with safeConfig. Commands that read files in a
// worktree go through gitFor instead, which also switches off its filters.
func git(ctx context.Context, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append(slices.Clone(safeConfig), args...)...)
	cmd.Env = gitEnv()
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
	out, err := git(ctx, "-C", w.Path, "config", "-z", "--show-scope", "--get-regexp", `^filter\.`)
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		// No filters configured anywhere.
		out, err = "", nil
	}

	if err != nil {
		return nil, fmt.Errorf("reading repo config: %w", err)
	}

	// With -z, entries are "<scope>\0<key>\n<value>\0", so values can't
	// smuggle in fake entries.
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var overrides []string
	seen := map[string]bool{}
	for i := 0; i+1 < len(fields); i += 2 {
		scope := fields[i]
		key, _, _ := strings.Cut(fields[i+1], "\n")
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
	// Changes inside submodules aren't counted.
	Changes int
	// Unpushed counts commits on HEAD that are on no remote branch and not on
	// the repo's main branch: work that exists only in this worktree.
	Unpushed   int
	LastCommit time.Time
	// Submodules is set when the worktree has a .gitmodules file.
	Submodules bool
	// Ignored lists ignored files and folders that removal deletes and that
	// can't simply be rebuilt (like .env), with dependency and build output
	// folders such as node_modules left out.
	Ignored []string
	// fingerprint identifies the exact set of changes, for SameChanges.
	fingerprint [sha256.Size]byte
}

// SameChanges reports whether two inspections saw exactly the same changes.
func (g GitInfo) SameChanges(other GitInfo) bool { return g.fingerprint == other.fingerprint }

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

	// Submodules are skipped: `status` would run git inside each one, with
	// that submodule's own (unchecked) config.
	// --ignored lists ignored folders collapsed ("!! node_modules/").
	status, err := run("-C", w.Path, "status", "--porcelain", "--ignore-submodules=all", "--ignored")
	if err != nil {
		return info, err
	}

	info.fingerprint = sha256.Sum256([]byte(status))
	for line := range strings.Lines(status) {
		line = strings.TrimRight(line, "\n")
		if path, ok := strings.CutPrefix(line, "!! "); ok {
			if !rebuildable(path) {
				info.Ignored = append(info.Ignored, path)
			}
		} else if strings.TrimSpace(line) != "" {
			info.Changes++
		}
	}

	if _, err := os.Lstat(filepath.Join(w.Path, ".gitmodules")); err == nil {
		info.Submodules = true
	}

	// One for-each-ref finds main/master and the branch's last commit time.
	patterns := []string{"refs/heads/main", "refs/heads/master"}
	branch := ""
	if w.Branch != "" {
		branch = "refs/heads/" + w.Branch
		patterns = append(patterns, branch)
	}

	out, _ := run(append([]string{"-C", w.Path, "for-each-ref", "--format=%(refname) %(committerdate:unix)"}, patterns...)...)
	var mainRefs []string
	for line := range strings.Lines(out) {
		ref, secs, _ := strings.Cut(strings.TrimSpace(line), " ")
		// Patterns also match below a slash (refs/heads/main/x), so check exactly.
		if ref == "refs/heads/main" || ref == "refs/heads/master" {
			mainRefs = append(mainRefs, ref)
		}

		if ref == branch {
			info.LastCommit = parseUnix(secs)
		}
	}

	if branch == "" {
		// Detached HEAD. log.showSignature in the repo's config would run
		// its gpg.program.
		if out, err := run("-C", w.Path, "log", "-1", "--no-show-signature", "--format=%ct"); err == nil {
			info.LastCommit = parseUnix(strings.TrimSpace(out))
		}
	}

	if len(mainRefs) == 0 {
		// No main or master: use the main worktree's branch (or a bare
		// repo's default branch).
		if head, err := os.ReadFile(filepath.Join(w.CommonDir, "HEAD")); err == nil {
			if ref, ok := branchRef(head); ok {
				mainRefs = []string{ref}
			}
		}
	}

	if out, err := run(append([]string{"-C", w.Path, "rev-list", "--count", "HEAD", "--not", "--remotes"}, mainRefs...)...); err == nil {
		info.Unpushed, _ = strconv.Atoi(strings.TrimSpace(out))
	}

	return info, nil
}

// rebuildableNames are ignored files and folders that tools regenerate, so
// deleting them loses nothing worth a warning.
var rebuildableNames = map[string]bool{
	"node_modules": true, "bower_components": true, "vendor": true, ".pnpm-store": true,
	"dist": true, "build": true, "out": true, "target": true, "coverage": true,
	".next": true, ".nuxt": true, ".svelte-kit": true, ".turbo": true, ".cache": true,
	".parcel-cache": true, ".vite": true, ".expo": true, ".gradle": true, "DerivedData": true, "Pods": true,
	"__pycache__": true, ".venv": true, "venv": true, ".pytest_cache": true, ".mypy_cache": true,
	".ruff_cache": true, ".tox": true, ".DS_Store": true,
}

// rebuildable reports whether an ignored path from `git status` is, or is
// inside, something tools regenerate.
func rebuildable(path string) bool {
	path = strings.TrimSuffix(strings.Trim(path, `"`), "/")
	for part := range strings.SplitSeq(path, "/") {
		if rebuildableNames[part] {
			return true
		}
	}

	switch filepath.Ext(path) {
	case ".log", ".pyc", ".tsbuildinfo":
		return true
	}

	return false
}

func parseUnix(secs string) time.Time {
	n, err := strconv.ParseInt(secs, 10, 64)
	if err != nil {
		return time.Time{}
	}

	return time.Unix(n, 0)
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
	// Home is the home folder. Its direct children (like ~/Downloads) are
	// never deleted outright.
	Home string
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

// removeOrphan deletes an orphan's folder after checking, right before, that
// it is still safe to. The tool may have been open for hours since the scan.
//
// The parent folder is opened once, and the checks on the folder's contents
// and the delete all go through that handle, which never follows symlinks out
// of it. So swapping the folder, or anything in it, for a symlink can't make
// the checks look at one place and the delete happen in another.
func removeOrphan(w Worktree, opts RemoveOptions) error {
	if containsProtected(w.Path, opts.Protected) {
		return fmt.Errorf("refusing to delete %s: it is or contains a scanned folder or your home folder", w.Path)
	}

	if opts.Home != "" && samePath(filepath.Dir(w.Path), CanonicalPath(opts.Home)) {
		return fmt.Errorf("refusing to delete %s: it sits directly in your home folder", w.Path)
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
