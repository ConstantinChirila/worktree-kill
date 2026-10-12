package wt

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
)

// KnownDirs returns locations where AI coding tools keep worktrees. They are
// scanned first so results appear immediately, and they matter because the
// main walk skips hidden directories.
func KnownDirs(home string) []string {
	dirs := []string{
		filepath.Join(home, ".claude-worktrees"),
		filepath.Join(home, ".cursor", "worktrees"),
		filepath.Join(home, ".codex", "worktrees"),
		filepath.Join(home, "conductor"),
		filepath.Join(home, ".t3", "worktrees"),
	}

	if codexHome := os.Getenv("CODEX_HOME"); codexHome != "" {
		dirs = append(dirs, filepath.Join(codexHome, "worktrees"))
	}

	return dirs
}

// skipDir reports whether the walk should not descend into a directory.
// Hidden directories are skipped: repo-internal locations like .claude/worktrees
// are found through the repo's own metadata, and tool locations via KnownDirs.
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

type ScanOptions struct {
	Roots []string
	Home  string
	// Workers caps concurrent directory reads. Defaults to a multiple of NumCPU.
	Workers int
}

type Scanner struct {
	opts    ScanOptions
	found   func(Worktree)
	visited atomic.Int64

	mu      sync.Mutex
	seen    map[string]bool
	repos   map[string]bool
	stack   []string
	pending int
	cond    *sync.Cond
}

// Scan walks the roots and calls found (from multiple goroutines) for each
// distinct worktree. It returns when the walk finishes or ctx is cancelled.
func Scan(ctx context.Context, opts ScanOptions, found func(Worktree)) *Scanner {
	s := NewScanner(opts, found)
	s.Run(ctx)
	return s
}

func NewScanner(opts ScanOptions, found func(Worktree)) *Scanner {
	if opts.Workers <= 0 {
		opts.Workers = runtime.NumCPU() * 4
	}

	s := &Scanner{
		opts:  opts,
		found: found,
		seen:  map[string]bool{},
		repos: map[string]bool{},
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Visited is the number of directories read so far. Safe to call concurrently.
func (s *Scanner) Visited() int64 { return s.visited.Load() }

func (s *Scanner) Run(ctx context.Context) {
	// The stack is LIFO, so push in reverse to read the first root first.
	for i := len(s.opts.Roots) - 1; i >= 0; i-- {
		s.push(filepath.Clean(s.opts.Roots[i]))
	}

	var wg sync.WaitGroup
	for range s.opts.Workers {
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

		// Repos and worktrees are leaves: anything nested inside a repo that
		// matters (like .claude/worktrees) is registered in its metadata.
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
	if resolved, err := filepath.EvalSymlinks(w.Path); err == nil {
		w.Path = resolved
	}

	key := w.Path

	s.mu.Lock()
	if s.seen[key] {
		s.mu.Unlock()
		return
	}

	s.seen[key] = true
	s.mu.Unlock()
	s.found(w)
}
