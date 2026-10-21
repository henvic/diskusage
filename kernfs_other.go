//go:build !linux

package main

// isKernFS is Linux-only: other systems have no /proc or /sys to walk into
// (macOS's devfs and Windows have nothing comparable worth skipping).
func isKernFS(path string) bool { return false }
