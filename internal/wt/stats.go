package wt

import (
	"context"
	"io/fs"
	"path/filepath"
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

// MeasureUsage walks a worktree without following symlinks.
func MeasureUsage(ctx context.Context, root string) (Usage, error) {
	var usage Usage
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			// Unreadable subtrees are skipped rather than failing the whole walk.
			if entry != nil && entry.IsDir() {
				return fs.SkipDir
			}

			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}

		if info.ModTime().After(usage.LatestChange) {
			usage.LatestChange = info.ModTime()
		}

		// Directories always have several links (one per subdirectory).
		bytes, linked := diskUsage(info)
		if !linked || entry.IsDir() {
			usage.Bytes += bytes
		}

		return nil
	})

	return usage, err
}
