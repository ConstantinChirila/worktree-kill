//go:build !unix

package wt

import "io/fs"

// diskUsage falls back to apparent size where block counts are unavailable.
func diskUsage(info fs.FileInfo) (bytes int64, linked bool) {
	return info.Size(), false
}
