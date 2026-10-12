package wt

import (
	"context"
	"io/fs"
	"path/filepath"
	"time"
)

type Usage struct {
	// Bytes is disk usage; hard-linked files (common with pnpm) count once.
	Bytes int64
	// LatestChange is the newest mtime of any file or directory in the tree.
	LatestChange time.Time
}

// MeasureUsage walks a worktree without following symlinks.
func MeasureUsage(ctx context.Context, root string) (Usage, error) {
	var usage Usage
	seen := map[fileID]bool{}

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

		bytes, id, linked := diskUsage(info)
		if linked && !entry.IsDir() {
			if seen[id] {
				return nil
			}

			seen[id] = true
		}

		usage.Bytes += bytes
		if info.ModTime().After(usage.LatestChange) {
			usage.LatestChange = info.ModTime()
		}

		return nil
	})

	return usage, err
}
