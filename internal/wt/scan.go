package wt

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

	// macOS home folders that are large and never hold repos.
	if parent == home {
		switch name {
		case "Library", "Applications", "Movies", "Music", "Pictures":
			return true
		}
	}

	return false
}

// ScanOptions configures a Scanner. Paths should be canonical (see CanonicalPath).
type ScanOptions struct {
	Roots []string
	Home  string
}

// Protected returns the paths that must never be deleted outright, nor any
// folder containing them: the scan roots and the home folder.
func (o ScanOptions) Protected() []string {
	protected := slices.Clone(o.Roots)
	if o.Home != "" {
		protected = append(protected, o.Home)
	}

	return protected
}

// Scanner walks folders looking for worktrees, reading many folders at once.
type Scanner struct {
	opts      ScanOptions
	protected []string
	found     func(Worktree)
	workers   int
	visited   atomic.Int64

	mu      sync.Mutex
	seen    map[string]bool
	repos   map[string]bool
	stack   []string
	pending int
	cond    *sync.Cond
}

// NewScanner returns a Scanner that calls found (from multiple goroutines)
// once for each distinct worktree.
func NewScanner(opts ScanOptions, found func(Worktree)) *Scanner {
	s := &Scanner{
		opts:      opts,
		protected: opts.Protected(),
		found:     found,
		workers:   runtime.NumCPU() * 4,
		seen:      map[string]bool{},
		repos:     map[string]bool{},
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Visited is the number of directories read so far. Safe to call concurrently.
func (s *Scanner) Visited() int64 { return s.visited.Load() }

// Run walks the roots. It returns when the walk finishes or ctx is cancelled.
func (s *Scanner) Run(ctx context.Context) {
	// The stack is LIFO, so push in reverse to read the first root first.
	for i := len(s.opts.Roots) - 1; i >= 0; i-- {
		s.push(filepath.Clean(s.opts.Roots[i]))
	}

	var wg sync.WaitGroup
	for range s.workers {
		wg.Go(func() {
			for {
				dir, ok := s.pop(ctx)
				if !ok {
					return
				}

				s.visit(dir)
				s.done()
			}
		})
	}

	stop := context.AfterFunc(ctx, func() {
		s.mu.Lock()
		s.cond.Broadcast()
		s.mu.Unlock()
	})
	defer stop()
	wg.Wait()
}

func (s *Scanner) push(dir string) {
	s.mu.Lock()
	s.stack = append(s.stack, dir)
	s.pending++
	s.mu.Unlock()
	s.cond.Signal()
}

func (s *Scanner) pop(ctx context.Context) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(s.stack) == 0 {
		if s.pending == 0 || ctx.Err() != nil {
			return "", false
		}

		s.cond.Wait()
	}

	if ctx.Err() != nil {
		return "", false
	}

	dir := s.stack[len(s.stack)-1]
	s.stack = s.stack[:len(s.stack)-1]
	return dir, true
}

func (s *Scanner) done() {
	s.mu.Lock()
	s.pending--
	finished := s.pending == 0
	s.mu.Unlock()
	if finished {
		s.cond.Broadcast()
	}
}

func (s *Scanner) visit(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	s.visited.Add(1)
	for _, entry := range entries {
		if entry.Name() != ".git" {
			continue
		}

		if entry.IsDir() {
			s.reportRepo(filepath.Join(dir, ".git"))
		} else if w, err := fromDotGitFile(dir); err == nil {
			s.report(w)
			if w.CommonDir != "" {
				s.reportRepo(w.CommonDir)
			}
		}

		// Repos and worktrees are leaves: worktrees nested inside them are
		// registered in the repo's metadata. Tool folders are still walked,
		// to find orphans there that git no longer knows about.
		for _, sub := range repoToolDirs() {
			if info, err := os.Lstat(filepath.Join(dir, sub)); err == nil && info.IsDir() {
				s.push(filepath.Join(dir, sub))
			}
		}

		return
	}

	for _, entry := range entries {
		// DirEntry.IsDir is false for symlinks, so links are never followed.
		if entry.IsDir() && !skipDir(dir, entry.Name(), s.opts.Home) {
			s.push(filepath.Join(dir, entry.Name()))
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
	if w.State == StateOrphaned && containsProtected(w.Path, s.protected) {
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

// containsProtected reports whether deleting dir would delete a protected
// path, or dir is a filesystem root.
func containsProtected(dir string, protected []string) bool {
	if filepath.Dir(dir) == dir {
		return true
	}

	for _, p := range protected {
		if within(CanonicalPath(p), dir) {
			return true
		}
	}

	return false
}
