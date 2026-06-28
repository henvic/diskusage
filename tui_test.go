//go:build unix

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func sel(e *explorer) int { return e.sel[len(e.sel)-1] }

// TestExplorerNav drives the explorer's key handling without a terminal,
// covering single keys, arrow escape sequences, and batched input.
func TestExplorerNav(t *testing.T) {
	sub := &Node{Name: "sub", IsDir: true, Size: 300, Children: []*Node{
		{Name: "x", Size: 200},
		{Name: "y", Size: 100},
	}}
	root := &Node{Name: "root", IsDir: true, Size: 600, Children: []*Node{
		sub,
		{Name: "a", Size: 200},
		{Name: "b", Size: 100},
	}}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}}

	// j moves down; two presses in one read land on the last row.
	e.handle([]byte("jj"))
	if sel(e) != 2 {
		t.Fatalf("after jj sel=%d, want 2", sel(e))
	}
	// Movement clamps at the bottom.
	e.handle([]byte("j"))
	if sel(e) != 2 {
		t.Fatalf("after clamp sel=%d, want 2", sel(e))
	}
	// g jumps to the top.
	e.handle([]byte("g"))
	if sel(e) != 0 {
		t.Fatalf("after g sel=%d, want 0", sel(e))
	}

	// l opens the selected directory (sub is largest, so index 0).
	e.handle([]byte("l"))
	if len(e.stack) != 2 || e.cur().Name != "sub" {
		t.Fatalf("descend failed: stack=%d cur=%s", len(e.stack), e.cur().Name)
	}

	// Down-arrow escape sequence moves within the child directory.
	e.handle([]byte{27, '[', 'B'})
	if sel(e) != 1 {
		t.Fatalf("after down-arrow sel=%d, want 1", sel(e))
	}

	// h returns to the parent, restoring its remembered selection.
	e.handle([]byte("h"))
	if len(e.stack) != 1 || sel(e) != 0 {
		t.Fatalf("ascend failed: stack=%d sel=%d", len(e.stack), sel(e))
	}

	// A batch of keys ending in q must still quit.
	if !e.handle([]byte("jjq")) {
		t.Fatal("batched input ending in q should quit")
	}
	// Descending onto a plain file is a no-op.
	e.handle([]byte("g")) // back to top (sub), open it
	e.handle([]byte("l"))
	e.handle([]byte("l")) // x is a file; should not descend
	if len(e.stack) != 2 {
		t.Fatalf("descending onto a file changed depth: stack=%d", len(e.stack))
	}
}

// TestExplorerEsc checks the Esc handling: it backs out one level and never
// quits, and is a harmless no-op at the root.
func TestExplorerEsc(t *testing.T) {
	sub := &Node{Name: "sub", IsDir: true, Size: 100, Children: []*Node{
		{Name: "x", Size: 50},
	}}
	root := &Node{Name: "root", IsDir: true, Size: 100, Children: []*Node{sub}}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}}

	e.handle([]byte("l")) // descend into sub
	if len(e.stack) != 2 {
		t.Fatalf("expected to descend, stack=%d", len(e.stack))
	}
	if e.handle([]byte{27}) { // lone Esc must not quit
		t.Fatal("Esc should not quit")
	}
	if len(e.stack) != 1 {
		t.Fatalf("Esc should back out to root, stack=%d", len(e.stack))
	}
	if e.handle([]byte{27}) { // Esc at the root: no-op, still must not quit
		t.Fatal("Esc at root should not quit")
	}
	if len(e.stack) != 1 {
		t.Fatalf("Esc at root changed depth: stack=%d", len(e.stack))
	}
}

// TestExplorerRescan verifies that 'r' picks up on-disk changes and propagates
// the new totals up to the ancestors.
func TestExplorerRescan(t *testing.T) {
	dir, err := os.MkdirTemp(".", "dutest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sub, "a.txt"), 1000)

	sc := &scanner{apparent: true}
	root, err := scan(dir, sc)
	if err != nil {
		t.Fatal(err)
	}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}, sc: sc}
	rootFiles := root.Files

	// Descend into sub, create a file on disk there, then rescan the subtree.
	e.handle([]byte("l"))
	if e.cur().Name != "sub" {
		t.Fatalf("expected to be in sub, got %q", e.cur().Name)
	}
	writeFile(t, filepath.Join(sub, "b.txt"), 2000)
	e.handle([]byte("r"))

	if got := e.cur().Files; got != 2 {
		t.Fatalf("sub files after rescan = %d, want 2", got)
	}
	// The root total (an ancestor) must reflect the new file too.
	if got := e.stack[0].Files; got != rootFiles+1 {
		t.Fatalf("root files after subtree rescan = %d, want %d", got, rootFiles+1)
	}
}

// TestExplorerStale checks the shallow stale indicator: a current-level change
// is detected, an unchanged directory is not flagged, and a rescan clears it.
func TestExplorerStale(t *testing.T) {
	dir, err := os.MkdirTemp(".", "dutest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	writeFile(t, filepath.Join(dir, "a.txt"), 100)

	sc := &scanner{apparent: true}
	root, err := scan(dir, sc)
	if err != nil {
		t.Fatal(err)
	}
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}, sc: sc}

	e.ensureBaseline()
	if e.stale {
		t.Fatal("a freshly scanned directory should not be stale")
	}
	if e.pollStale() || e.stale {
		t.Fatal("an unchanged directory must not be flagged stale")
	}

	// A new file at this level must be detected.
	writeFile(t, filepath.Join(dir, "b.txt"), 200)
	e.pollStale()
	if !e.stale {
		t.Fatal("added file was not detected as a change")
	}

	// Rescanning clears the indicator.
	e.handle([]byte("r"))
	e.ensureBaseline()
	if e.stale {
		t.Fatal("rescan should clear the stale flag")
	}
}

// TestSelected covers the cursor target used by copy: the highlighted entry,
// or nil for an empty directory.
func TestSelected(t *testing.T) {
	empty := &Node{Name: "empty", IsDir: true}
	e := &explorer{stack: []*Node{empty}, sel: []int{0}, off: []int{0}}
	if e.selected() != nil {
		t.Error("an empty directory should have no selection")
	}

	a := &Node{Name: "a", Size: 200}
	b := &Node{Name: "b", Size: 100}
	dir := &Node{Name: "dir", IsDir: true, Children: []*Node{a, b}}
	e = &explorer{stack: []*Node{dir}, sel: []int{1}, off: []int{0}}
	if got := e.selected(); got != b {
		t.Errorf("selected() = %v, want b", got)
	}
}

// TestDisplayPath covers the absolute vs. relative rendering used by c and C.
func TestDisplayPath(t *testing.T) {
	wd := "/home/user/proj"
	cases := []struct {
		raw      string
		absolute bool
		want     string
	}{
		{"a/b.txt", true, "/home/user/proj/a/b.txt"}, // relative entry → absolute
		{"a/b.txt", false, "a/b.txt"},                // relative entry → relative
		{"/home/user/proj/a/b.txt", true, "/home/user/proj/a/b.txt"},
		{"/home/user/proj/a/b.txt", false, "a/b.txt"}, // absolute entry → relative
		{"/etc/hosts", false, "../../../etc/hosts"},   // outside wd → climbs out
		{".", true, "/home/user/proj"},                // current dir → absolute
		{".", false, "."},
	}
	for _, c := range cases {
		if got := displayPath(c.raw, wd, c.absolute); got != c.want {
			t.Errorf("displayPath(%q, %q, %v) = %q, want %q", c.raw, wd, c.absolute, got, c.want)
		}
	}
}

// TestExplorerHelp checks the help overlay toggles open and any key dismisses
// it, while q still quits.
func TestExplorerHelp(t *testing.T) {
	root := &Node{Name: "root", IsDir: true, Children: []*Node{{Name: "a", Size: 1}}}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}}

	e.handle([]byte("?"))
	if !e.help {
		t.Fatal("? should open the help overlay")
	}
	if e.handle([]byte("j")) {
		t.Fatal("a key should not quit while help is open")
	}
	if e.help {
		t.Fatal("any key should dismiss the help overlay")
	}
	e.handle([]byte("?"))
	if !e.help {
		t.Fatal("? should reopen the help overlay")
	}
	if !e.handle([]byte("q")) {
		t.Fatal("q should quit even from the help overlay")
	}
}

// TestHeadLabel checks that the changed-on-disk marker shows only when stale.
func TestHeadLabel(t *testing.T) {
	n := &Node{Path: "/x", IsDir: true, Size: 2048, Files: 3, Dirs: 1}
	if strings.Contains(headLabel(n, false), "●") {
		t.Error("a clean directory header should not carry the marker")
	}
	if !strings.Contains(headLabel(n, true), "●") {
		t.Error("a stale directory header should carry the marker")
	}
}

// TestExplorerHidden covers the '.' dotfile filter: it hides dot entries, keeps
// the cursor on the same entry (clamping when that entry becomes hidden), and
// never mutates the scanned tree or its totals.
func TestExplorerHidden(t *testing.T) {
	root := &Node{Name: "root", IsDir: true, Size: 300, Children: []*Node{
		{Name: "visible", Size: 200},
		{Name: ".hidden", Size: 100},
	}}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}}

	if got := len(e.children()); got != 2 {
		t.Fatalf("showing all: %d entries, want 2", got)
	}

	// Cursor on the visible entry; hiding keeps it there.
	e.set(0) // 'visible' (200) sorts first
	if e.selected().Name != "visible" {
		t.Fatalf("setup: cursor on %q, want visible", e.selected().Name)
	}
	e.handle([]byte("."))
	if !e.hideDot {
		t.Fatal(". should enable the dotfile filter")
	}
	if got := e.children(); len(got) != 1 || got[0].Name != "visible" {
		t.Fatalf("hidden: %d entries, want 1 (visible)", len(got))
	}
	if got := e.hiddenSize(); got != 100 {
		t.Fatalf("hiddenSize = %d, want 100 (the .hidden entry)", got)
	}
	if e.selected() == nil || e.selected().Name != "visible" {
		t.Fatalf("cursor should stay on 'visible', got %v", e.selected())
	}
	if root.Size != 300 || len(root.Children) != 2 {
		t.Fatal("hiding must not mutate the tree or its totals")
	}

	// Toggle back, move onto the dot entry, then hide: the cursor must clamp to a
	// visible entry rather than dangle out of range.
	e.handle([]byte("."))
	if e.hideDot || len(e.children()) != 2 {
		t.Fatal(". should toggle back to showing all")
	}
	if e.hiddenSize() != 0 {
		t.Fatalf("hiddenSize with the filter off = %d, want 0", e.hiddenSize())
	}
	e.last() // '.hidden' (100) sorts last
	if e.selected().Name != ".hidden" {
		t.Fatalf("setup: cursor on %q, want .hidden", e.selected().Name)
	}
	e.handle([]byte("."))
	if e.selected() == nil || e.selected().Name != "visible" {
		t.Fatalf("hiding the selected dot entry should clamp to a visible one, got %v", e.selected())
	}
}
