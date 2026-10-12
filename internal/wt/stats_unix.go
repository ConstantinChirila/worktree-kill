//go:build unix

package wt

import (
	"io/fs"
	"syscall"
)

type fileID struct{ dev, ino uint64 }

// diskUsage returns allocated bytes and, for files with several hard links,
// an identity so they are counted once.
func diskUsage(info fs.FileInfo) (bytes int64, id fileID, linked bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.Size(), fileID{}, false
	}

	return int64(stat.Blocks) * 512, fileID{uint64(stat.Dev), uint64(stat.Ino)}, stat.Nlink > 1
}
