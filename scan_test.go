package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// TestScan builds a small tree inside the worktree and checks the aggregated
// counts and sizes (asserting on apparent sizes, which are exact on any
// filesystem).
func TestScan(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "a.txt"), 1000)
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(dir, "sub", "b.txt"), 2000)
	writeFile(t, filepath.Join(dir, "sub", "c.txt"), 500)

	root, err := scan(dir, &scanner{})
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
			fileBytes += n.Apparent
		}
	})
	if fileBytes != 3500 {
		t.Errorf("file bytes = %d, want 3500", fileBytes)
	}

	// The directory total must include at least the file contents.
	if root.Apparent < 3500 {
		t.Errorf("root apparent size = %d, want >= 3500", root.Apparent)
	}
}

// TestExclude verifies that --exclude globs drop matching entries.
func TestExclude(t *testing.T) {
	dir := t.TempDir()

	writeFile(t, filepath.Join(dir, "keep.txt"), 100)
	writeFile(t, filepath.Join(dir, "skip.log"), 100)

	root, err := scan(dir, &scanner{excludes: []string{"*.log"}})
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

// TestSnapshotWhileScanning hammers snapshot against a live scan: every copy
// must be internally consistent (Size = Self + the children's sizes at every
// node), and the finished tree must carry the exact totals.
func TestSnapshotWhileScanning(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 20; i++ {
		sub := filepath.Join(dir, fmt.Sprintf("d%02d", i))
		if err := os.Mkdir(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		for j := 0; j < 20; j++ {
			writeFile(t, filepath.Join(sub, fmt.Sprintf("f%02d", j)), 10)
		}
	}

	sc := &scanner{}
	root, done, err := scanStart(dir, sc)
	if err != nil {
		t.Fatal(err)
	}
	for {
		select {
		case <-done:
			if root.Files != 400 || root.Dirs != 20 {
				t.Errorf("finished tree has %d files, %d dirs, want 400 and 20", root.Files, root.Dirs)
			}
			var sum int64
			walk(root, func(n *Node) {
				if !n.IsDir {
					sum += n.Apparent
				}
			})
			if sum != 4000 {
				t.Errorf("file bytes = %d, want 4000", sum)
			}
			return
		default:
			snap := sc.snapshot(root)
			walk(snap, func(n *Node) {
				var kids, kidsApparent int64
				for _, c := range n.Children {
					kids += c.Size
					kidsApparent += c.Apparent
				}
				if n.Size != n.Self+kids {
					t.Fatalf("inconsistent snapshot at %s: size %d != self %d + children %d",
						n.Path, n.Size, n.Self, kids)
				}
				if n.Apparent != n.ApparentSelf+kidsApparent {
					t.Fatalf("inconsistent snapshot at %s: apparent %d != self %d + children %d",
						n.Path, n.Apparent, n.ApparentSelf, kidsApparent)
				}
			})
		}
	}
}

// TestSnapshotIsolated checks that a snapshot is a deep copy: mutating the
// original afterwards must not show through.
func TestSnapshotIsolated(t *testing.T) {
	root := &Node{Name: "r", Path: "r", IsDir: true, Size: 30, Self: 10, Children: []*Node{
		{Name: "a", Path: "r/a", Size: 20, Files: 1},
	}}
	snap := (&scanner{}).snapshot(root)

	root.Size = 999
	root.Children[0].Name = "changed"
	root.Children = append(root.Children, &Node{Name: "b"})

	if snap.Size != 30 || len(snap.Children) != 1 || snap.Children[0].Name != "a" {
		t.Errorf("snapshot must be isolated from later mutations, got size %d, children %v",
			snap.Size, snap.Children)
	}
}

func writeFile(t *testing.T, path string, n int) {
	t.Helper()
	if err := os.WriteFile(path, make([]byte, n), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestBothSizes checks every node carries both measures: the apparent size is
// the exact byte count, and the on-disk size comes in whole 512-byte blocks
// where the platform reports them (Unix), or equals the apparent size where it
// does not (Windows).
func TestBothSizes(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f")
	writeFile(t, path, 1000)
	root, err := scan(dir, &scanner{})
	if err != nil {
		t.Fatal(err)
	}
	f := root.Children[0]
	if f.Apparent != 1000 || f.ApparentSelf != 1000 {
		t.Errorf("apparent = %d/%d, want 1000", f.Apparent, f.ApparentSelf)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, blocks := diskUsage(info); blocks {
		if f.Size%512 != 0 || f.Size != f.Self {
			t.Errorf("on-disk size = %d/%d, want whole blocks", f.Size, f.Self)
		}
	} else if f.Size != f.Apparent || f.Self != f.ApparentSelf {
		t.Errorf("without block counts, on-disk size = %d/%d, want the apparent %d", f.Size, f.Self, f.Apparent)
	}
	if root.Apparent != root.ApparentSelf+1000 {
		t.Errorf("root apparent = %d, want self %d + 1000", root.Apparent, root.ApparentSelf)
	}
	if f.bytes(true) != f.Apparent || f.bytes(false) != f.Size {
		t.Error("bytes should pick the measure")
	}
}
