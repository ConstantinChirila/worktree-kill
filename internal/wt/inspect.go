package wt

import (
	"bytes"
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GitInfo is a worktree's git status.
type GitInfo struct {
	// Changes counts entries in `git status --porcelain` (modified + untracked).
	// Changes inside submodules aren't counted.
	Changes int
	// Unpushed counts commits on HEAD that are on no remote branch and not on
	// the repo's main branch: work that exists only in this worktree.
	Unpushed   int
	LastCommit time.Time
	// Submodules counts populated submodules and embedded repos (gitlinks).
	// `git worktree remove` refuses those without --force, and --force
	// deletes them with their changes and unpushed commits.
	Submodules int
	// Ignored lists ignored files and folders that removal deletes and that
	// can't simply be rebuilt (like .env), with dependency and build output
	// folders such as node_modules left out.
	Ignored []string
	// fingerprint identifies the exact set of changes, for SameChanges.
	fingerprint [sha256.Size]byte
}

// SameChanges reports whether two inspections saw exactly the same changes.
func (g GitInfo) SameChanges(other GitInfo) bool { return g.fingerprint == other.fingerprint }

// InspectConcurrency is how many InspectGit calls are worth running at once.
// Each runs a few short git processes one after another.
const InspectConcurrency = 8

// InspectGit gathers status for a worktree whose directory and repo exist.
func InspectGit(ctx context.Context, w Worktree) (GitInfo, error) {
	if w.State != StateOK {
		return GitInfo{}, nil
	}

	var info GitInfo
	g, err := gitFor(ctx, w)
	if err != nil {
		return info, err
	}

	// Submodules are skipped: `status` would run git inside each one, with
	// that submodule's own (unchecked) config.
	// --ignored=matching lists what the ignore rules match: a matched folder
	// as one entry ("!! node_modules/") without reading inside it, and files
	// matched by name ("!! logs/a.log") rather than their unmatched folder.
	status, err := g.run("-C", w.Path, "status", "--porcelain", "--ignore-submodules=all", "--ignored=matching")
	if err != nil {
		return info, err
	}

	if info.Submodules, err = populatedSubmodules(g, w); err != nil {
		return info, err
	}

	info.fingerprint = sha256.Sum256([]byte(status + "\x00" + strconv.Itoa(info.Submodules)))
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

	// One for-each-ref finds main/master and the branch's last commit time.
	patterns := []string{"refs/heads/main", "refs/heads/master"}
	branch := ""
	if w.Branch != "" {
		branch = "refs/heads/" + w.Branch
		patterns = append(patterns, branch)
	}

	out, _ := g.run(append([]string{"-C", w.Path, "for-each-ref", "--format=%(refname) %(committerdate:unix)"}, patterns...)...)
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
		if out, err := g.run("-C", w.Path, "log", "-1", "--no-show-signature", "--format=%ct"); err == nil {
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

	if out, err := g.run(append([]string{"-C", w.Path, "rev-list", "--count", "HEAD", "--not", "--remotes"}, mainRefs...)...); err == nil {
		info.Unpushed, _ = strconv.Atoi(strings.TrimSpace(out))
	}

	return info, nil
}

// populatedSubmodules counts gitlinks (mode 160000 index entries: submodules,
// or repos committed inside the repo without .gitmodules) whose folder holds a
// checkout, which is what makes `git worktree remove` refuse. Gitlinks start
// out empty in a new worktree, so most worktrees have none. Like git, a
// modules folder in the worktree's admin dir counts too.
func populatedSubmodules(g repoGit, w Worktree) (int, error) {
	count := 0
	err := g.each(func(record []byte) {
		// Records look like "160000 <hash> <stage>\t<path>".
		meta, path, ok := bytes.Cut(record, []byte("\t"))
		if !ok || !bytes.HasPrefix(meta, []byte("160000 ")) {
			return
		}

		if _, err := os.Lstat(filepath.Join(w.Path, filepath.FromSlash(string(path)), ".git")); err == nil {
			count++
		}
	}, "-C", w.Path, "ls-files", "--stage", "-z")
	if err != nil {
		return 0, err
	}

	if info, err := os.Stat(filepath.Join(w.GitDir, "modules")); count == 0 && err == nil && info.IsDir() {
		count = 1
	}

	return count, nil
}

// NeedsForce reports whether removing the worktree needs --force: it has
// uncommitted changes or populated submodules.
func (g GitInfo) NeedsForce() bool { return g.Changes > 0 || g.Submodules > 0 }

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
