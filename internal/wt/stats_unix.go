//go:build unix

package wt

import (
	"io/fs"
	"syscall"
)

// diskUsage returns allocated bytes, and whether the file has other hard links.
func diskUsage(info fs.FileInfo) (bytes int64, linked bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return info.Size(), false
	}

	return int64(stat.Blocks) * 512, stat.Nlink > 1
}
