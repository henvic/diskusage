//go:build linux

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIsKernFS checks the pseudo-filesystem detection against /proc and an
// ordinary directory.
func TestIsKernFS(t *testing.T) {
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("no /proc mounted here")
	}
	if !isKernFS("/proc") {
		t.Error("/proc should be detected as a kernel filesystem")
	}
	if isKernFS(t.TempDir()) {
		t.Error("a temp dir should not be a kernel filesystem")
	}
}

// TestScanSkipsKernFS reaches /proc through a followed symlink (a mount point
// below the scan root) and checks it is skipped, while scanning /proc itself
// as the root still works.
func TestScanSkipsKernFS(t *testing.T) {
	if _, err := os.Stat("/proc/self"); err != nil {
		t.Skip("no /proc mounted here")
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "f"), 10)
	if err := os.Symlink("/proc", filepath.Join(dir, "p")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}
	root, err := scan(dir, &scanner{apparent: true, follow: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range root.Children {
		if c.Name == "p" {
			t.Errorf("the /proc mount should be skipped, got %d files under it", c.Files)
		}
	}

	proc, err := scan("/proc/self", &scanner{apparent: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(proc.Children) == 0 {
		t.Error("a scan rooted inside /proc should still list its entries")
	}
}
