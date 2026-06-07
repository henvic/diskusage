//go:build !unix

package main

import "io/fs"

// diskUsage falls back to the apparent size on platforms without Unix stat
// data (e.g. Windows). The device is unknown, so --one-filesystem is a no-op.
func diskUsage(info fs.FileInfo) (bytes int64, dev uint64, ok bool) {
	return info.Size(), 0, false
}
