//go:build unix

package main

import (
	"io/fs"
	"syscall"
)

// diskUsage returns the bytes actually allocated on disk for info (number of
// 512-byte blocks reported by stat), together with the device the file lives
// on. ok is false when the platform stat data is unavailable, in which case
// callers should fall back to the apparent size.
func diskUsage(info fs.FileInfo) (bytes int64, dev uint64, ok bool) {
	st, k := info.Sys().(*syscall.Stat_t)
	if !k {
		return info.Size(), 0, false
	}
	// st_blocks is always in 512-byte units, independent of st_blksize.
	return int64(st.Blocks) * 512, uint64(st.Dev), true
}
