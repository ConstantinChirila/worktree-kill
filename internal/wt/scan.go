package wt

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
)

// skipDir reports whether the walk should not descend into a directory.
// Hidden directories are skipped: repo-internal locations like .claude/worktrees
// are found through the repo itself, and tool locations via KnownDirs.
func skipDir(parent, name, home string) bool {
	if strings.HasPrefix(name, ".") {
		return true
	}

	switch name {
	case "node_modules", "bower_components", "Pods", "DerivedData":
		return true
	}

	// Home folders that are large and never hold repos: macOS media and app
	// folders, VM disks, and Python distributions.
	if parent == home {
		switch name {
		case "Library", "Applications", "Movies", "Music", "Pictures",
			"OrbStack", "miniconda3", "anaconda3", "miniforge3":
			return true
		}
	}

	// Go's module cache (~/go/pkg); ~/go/src can still hold repos.
	return name == "pkg" && home != "" && parent == filepath.Join(home, "go")
}

// ScanOptions configures a Scanner. Paths should be canonical (see CanonicalPath).
type ScanOptions struct {
	Roots []string
	Home  string
}

// Scanner walks folders looking for worktrees, reading several at once.
type Scanner struct {
	opts    ScanOptions
	protect Protection
	found   func(Worktree)
	visited atomic.Int64
	// roots is set by Run before the walk starts and only read after.
	roots map[string]bool

	mu    sync.Mutex
	seen  map[string]bool
	repos map[string]bool
}

// NewScanner returns a Scanner that calls found (from multiple goroutines)
// once for each distinct worktree.
func NewScanner(opts ScanOptions, found func(Worktree)) *Scanner {
	return &Scanner{
		opts:    opts,
		protect: opts.Protection(),
		found:   found,
		seen:    map[string]bool{},
		repos:   map[string]bool{},
	}
}

// Visited is the number of directories read so far. Safe to call concurrently.
func (s *Scanner) Visited() int64 { return s.visited.Load() }

// Run walks the roots. It returns when the walk finishes or ctx is cancelled.
func (s *Scanner) Run(ctx context.Context) {
	roots := make([]string, len(s.opts.Roots))
	s.roots = map[string]bool{}
	for i, root := range s.opts.Roots {
		roots[i] = filepath.Clean(root)
		s.roots[roots[i]] = true
	}

	walk(ctx, roots, s.visit)
}

func (s *Scanner) visit(dir string, push func(string)) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	s.visited.Add(1)
	if slices.ContainsFunc(entries, func(e os.DirEntry) bool { return e.Name() == ".git" }) {
		s.visitRepo(dir, push)
		// Repos and worktrees are leaves: worktrees nested inside them are
		// registered in the repo's metadata. A scan root is still walked,
		// though: a home folder that is a dotfiles repo holds everything.
		if !s.roots[dir] {
			return
		}
	}

	for _, entry := range entries {
		// DirEntry.IsDir is false for symlinks, so links are never followed.
		if entry.IsDir() && !skipDir(dir, entry.Name(), s.opts.Home) {
			push(filepath.Join(dir, entry.Name()))
		}
	}
}

// visitRepo reports the repo or worktree in dir, and walks its tool folders
// (like .claude/worktrees) to find orphans there that git no longer knows.
func (s *Scanner) visitRepo(dir string, push func(string)) {
	dotGit := filepath.Join(dir, ".git")
	if info, err := os.Lstat(dotGit); err == nil && info.IsDir() {
		s.reportRepo(dotGit)
	} else if w, err := fromDotGitFile(dir); err == nil {
		s.report(w)
		if w.CommonDir != "" {
			s.reportRepo(w.CommonDir)
		}
	}

	for _, sub := range repoToolDirs {
		if info, err := os.Lstat(filepath.Join(dir, sub)); err == nil && info.IsDir() {
			push(filepath.Join(dir, sub))
		}
	}
}

// reportRepo lists a repo's registered worktrees, once per repo.
func (s *Scanner) reportRepo(commonDir string) {
	s.mu.Lock()
	if s.repos[commonDir] {
		s.mu.Unlock()
		return
	}

	s.repos[commonDir] = true
	s.mu.Unlock()

	for _, w := range worktreesOfRepo(commonDir) {
		s.report(w)
	}
}

func (s *Scanner) report(w Worktree) {
	// Report one spelling per directory (macOS /tmp is a symlink to /private/tmp).
	w.Path = CanonicalPath(w.Path)
	// An orphan that could never be deleted isn't worth listing.
	if w.State == StateOrphaned && s.protect.check(w.Path) != nil {
		return
	}

	w.Tool = ToolFor(w.Path, s.opts.Home)

	s.mu.Lock()
	if s.seen[w.Path] {
		s.mu.Unlock()
		return
	}

	s.seen[w.Path] = true
	s.mu.Unlock()
	s.found(w)
}
