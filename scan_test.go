package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestScan builds a small tree inside the worktree and checks the aggregated
// counts and apparent sizes.
func TestScan(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "a.txt"), 1000)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "b.txt"), 2000)
	writeFile(t, filepath.Join(dir, "sub", "c.txt"), 500)

	// Apparent mode so file sizes are exact and independent of the filesystem
	// block size.
	root, err := scan(dir, &scanner{apparent: true})
	if err != nil {
		t.Fatal(err)
	}

	if root.Files != 3 {
		t.Errorf("files = %d, want 3", root.Files)
	}
	if root.Dirs != 1 {
		t.Errorf("dirs = %d, want 1", root.Dirs)
	}

	var fileBytes int64
	walk(root, func(n *Node) {
		if !n.IsDir {
			fileBytes += n.Size
		}
	})
	if fileBytes != 3500 {
		t.Errorf("file bytes = %d, want 3500", fileBytes)
	}

	// The directory total must include at least the file contents.
	if root.Size < 3500 {
		t.Errorf("root size = %d, want >= 3500", root.Size)
	}
}

// TestExclude verifies that --exclude globs drop matching entries.
func TestExclude(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "keep.txt"), 100)
	writeFile(t, filepath.Join(dir, "skip.log"), 100)

	root, err := scan(dir, &scanner{apparent: true, excludes: []string{"*.log"}})
	if err != nil {
		t.Fatal(err)
	}
	if root.Files != 1 {
		t.Errorf("files = %d, want 1 after excluding *.log", root.Files)
	}
}

func TestParseSize(t *testing.T) {
	cases := map[string]int64{
		"":     0,
		"512":  512,
		"1k":   1024,
		"1KiB": 1024,
		"10M":  10 << 20,
		"1.5G": 1<<30 + 1<<29,
		"2tb":  2 << 40,
	}
	for in, want := range cases {
		got, err := parseSize(in)
		if err != nil {
			t.Errorf("parseSize(%q) error: %v", in, err)
			continue
		}
		if got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
	if _, err := parseSize("10X"); err == nil {
		t.Error("parseSize(10X) should fail on a bad unit")
	}
	if _, err := parseSize("9999999999P"); err == nil {
		t.Error("parseSize should reject a size that overflows int64")
	}
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}
