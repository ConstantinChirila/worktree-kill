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
		if err != nil {
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
