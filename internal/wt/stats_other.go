//go:build !unix

package wt

import "io/fs"

type fileID struct{}

// diskUsage falls back to apparent size where block counts are unavailable.
func diskUsage(info fs.FileInfo) (bytes int64, id fileID, linked bool) {
	return info.Size(), fileID{}, false
}
