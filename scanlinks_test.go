//go:build unix

package main

import (
	"os"
	"path/filepath"
	"testing"
)

// TestHardlinksCountedOnce checks that the contents of a hardlinked file count
// once toward the totals: both paths are listed, but only one carries the size.
func TestHardlinksCountedOnce(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin")
	writeFile(t, a, 1000)
	if err := os.Link(a, filepath.Join(dir, "b.bin")); err != nil {
		t.Skipf("hardlinks unsupported here: %v", err)
	}

	root, err := scan(dir, &scanner{apparent: true})
	if err != nil {
		t.Fatal(err)
	}
	if root.Files != 2 {
		t.Errorf("files = %d, want 2 (both links listed)", root.Files)
	}
	var sum int64
	files := 0
	walk(root, func(n *Node) {
		if !n.IsDir {
			sum += n.Size
			files++
		}
	})
	if files != 2 {
		t.Fatalf("listed files = %d, want 2", files)
	}
	if sum != 1000 {
		t.Errorf("hardlinked contents counted as %d bytes, want 1000 (once)", sum)
	}
}

// TestSymlinkListed checks that a symlink shows up as an entry of its own,
// sized as the link itself rather than its target.
func TestSymlinkListed(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "target.txt"), 100)
	if err := os.Symlink("target.txt", filepath.Join(dir, "ln")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	root, err := scan(dir, &scanner{apparent: true})
	if err != nil {
		t.Fatal(err)
	}
	var ln *Node
	for _, c := range root.Children {
		if c.Name == "ln" {
			ln = c
		}
	}
	if ln == nil {
		t.Fatal("the symlink should be listed as an entry")
	}
	if ln.Size >= 100 {
		t.Errorf("symlink size = %d, want the link itself, not the 100-byte target", ln.Size)
	}
	if root.Files != 2 {
		t.Errorf("files = %d, want 2 (target + link)", root.Files)
	}
}

// TestFollowCycleScansOnce checks that -L survives a symlink pointing back at
// an ancestor: the cycle is skipped instead of recursing forever.
func TestFollowCycleScansOnce(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "f"), 100)
	if err := os.Symlink(dir, filepath.Join(sub, "back")); err != nil {
		t.Skipf("symlinks unsupported here: %v", err)
	}

	root, err := scan(dir, &scanner{apparent: true, follow: true})
	if err != nil {
		t.Fatal(err)
	}
	// sub plus the empty placeholder listed for the already-walked target.
	if root.Dirs != 2 {
		t.Errorf("dirs = %d, want 2 (sub + the cycle placeholder)", root.Dirs)
	}
	if root.Files != 1 {
		t.Errorf("files = %d, want 1 (f counted once)", root.Files)
	}
	var back *Node
	walk(root, func(n *Node) {
		if n.Name == "back" {
			back = n
		}
	})
	if back == nil {
		t.Fatal("the already-walked link should still be listed")
	}
	if back.Size != 0 || len(back.Children) != 0 {
		t.Errorf("the cycle placeholder must be empty, got size %d with %d children", back.Size, len(back.Children))
	}
}

// TestFollowRepeatedFileTargetOnce checks that with -L two symlinks resolving
// to the same regular file count its contents once.
func TestFollowRepeatedFileTargetOnce(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "big.bin"), 1000)
	for _, ln := range []string{"ln1", "ln2"} {
		if err := os.Symlink("big.bin", filepath.Join(dir, ln)); err != nil {
			t.Skipf("symlinks unsupported here: %v", err)
		}
	}

	root, err := scan(dir, &scanner{apparent: true, follow: true})
	if err != nil {
		t.Fatal(err)
	}
	if root.Files != 3 {
		t.Errorf("files = %d, want 3 (all three paths listed)", root.Files)
	}
	var sum int64
	walk(root, func(n *Node) {
		if !n.IsDir {
			sum += n.Size
		}
	})
	if sum != 1000 {
		t.Errorf("repeated target counted as %d bytes, want 1000 (once)", sum)
	}
}

// TestRescanKeepsHardlinkDedup checks that a subtree rescan does not re-count a
// hardlink whose size-carrying path lies outside the rescanned directory.
func TestRescanKeepsHardlinkDedup(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.bin") // scanned before sub, so it claims the inode
	writeFile(t, a, 1000)
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(a, filepath.Join(sub, "b.bin")); err != nil {
		t.Skipf("hardlinks unsupported here: %v", err)
	}

	sc := &scanner{apparent: true}
	root, err := scan(dir, sc)
	if err != nil {
		t.Fatal(err)
	}
	sortChildren(root)
	before := root.Size

	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}, sc: sc}
	e.handle([]byte("j")) // a.bin (1000) sorts first; sub second
	e.handle([]byte("l"))
	if e.cur().Name != "sub" {
		t.Fatalf("setup: expected to be in sub, got %q", e.cur().Name)
	}
	e.handle([]byte("r"))
	if got := e.stack[0].Size; got != before {
		t.Errorf("root size after subtree rescan = %d, want %d (hardlink must stay counted once)", got, before)
	}
}
