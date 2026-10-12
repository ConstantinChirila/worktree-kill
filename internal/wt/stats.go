package wt

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// Usage is a worktree's disk usage and most recent file change.
type Usage struct {
	// Bytes is roughly the disk space deleting the worktree frees. Files with
	// several hard links (pnpm links packages from its store) are left out,
	// since deleting one link frees nothing.
	Bytes int64
	// LatestChange is the newest mtime of any file or directory in the tree.
	LatestChange time.Time
}

func (u *Usage) add(info os.FileInfo) {
	if info.ModTime().After(u.LatestChange) {
		u.LatestChange = info.ModTime()
	}

	// Directories always have several links (one per subdirectory).
	if bytes, linked := diskUsage(info); !linked || info.IsDir() {
		u.Bytes += bytes
	}
}

// MeasureConcurrency is how many MeasureUsage calls are worth running at once.
// Each already reads several folders in parallel.
const MeasureConcurrency = 2

// MeasureUsage walks a worktree without following symlinks, reading several
// directories at once. Unreadable directories and files are skipped.
func MeasureUsage(ctx context.Context, root string) (Usage, error) {
	info, err := os.Lstat(root)
	if err != nil {
		return Usage{}, err
	}

	var mu sync.Mutex
	var total Usage
	total.add(info)
	if !info.IsDir() {
		return total, nil
	}

	walk(ctx, []string{root}, func(dir string, push func(string)) {
		entries, err := os.ReadDir(dir)
		if err != nil || (dir != root && holdsWorktree(dir, entries)) {
			return
		}

		// Sum locally and merge once per directory, not once per file.
		var usage Usage
		for _, entry := range entries {
			info, err := entry.Info()
			if err != nil {
				continue
			}

			usage.add(info)
			if entry.IsDir() {
				push(filepath.Join(dir, entry.Name()))
			}
		}

		mu.Lock()
		total.Bytes += usage.Bytes
		if usage.LatestChange.After(total.LatestChange) {
			total.LatestChange = usage.LatestChange
		}
		mu.Unlock()
	})

	return total, ctx.Err()
}

// holdsWorktree reports whether dir is a linked worktree (or an orphaned
// one). Those are listed with their own size, so a worktree nested inside
// another isn't counted twice. Nested plain repos and submodules aren't
// listed, so they still count.
func holdsWorktree(dir string, entries []os.DirEntry) bool {
	for _, entry := range entries {
		if entry.Name() == ".git" && !entry.IsDir() {
			_, err := fromDotGitFile(dir)
			return err == nil
		}
	}

	return false
}
