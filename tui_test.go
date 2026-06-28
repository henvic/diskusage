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

// TestExplorerMarkToggle covers 'm': add/remove a mark, insertion order, and the
// no-op on an empty directory.
func TestExplorerMarkToggle(t *testing.T) {
	root := &Node{Name: "root", IsDir: true, Size: 300, Children: []*Node{
		{Name: "a", Path: "root/a", Size: 200},
		{Name: "b", Path: "root/b", Size: 100},
	}}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}}

	first := e.children()[0]
	e.handle([]byte("m"))
	if !e.marked[first.Path] || len(e.marks) != 1 || e.marks[0] != first.Path {
		t.Fatalf("after m: marked=%v marks=%v", e.marked, e.marks)
	}
	e.handle([]byte("m")) // toggle off
	if e.marked[first.Path] || len(e.marks) != 0 {
		t.Fatalf("second m should unmark: marked=%v marks=%v", e.marked, e.marks)
	}
	// Mark both; order follows insertion.
	e.set(0)
	e.handle([]byte("m"))
	e.set(1)
	e.handle([]byte("m"))
	if len(e.marks) != 2 || e.marks[0] != e.children()[0].Path || e.marks[1] != e.children()[1].Path {
		t.Fatalf("marks order wrong: %v", e.marks)
	}

	empty := &Node{Name: "empty", IsDir: true}
	ee := &explorer{stack: []*Node{empty}, sel: []int{0}, off: []int{0}}
	ee.handle([]byte("m"))
	if len(ee.marks) != 0 || ee.marked != nil {
		t.Fatal("marking in an empty directory must be a no-op")
	}
	if ee.status != "nothing to mark" {
		t.Fatalf("status = %q, want 'nothing to mark'", ee.status)
	}
}

// TestMarkIndicator checks the leading gutter span rowSpans adds for marked rows.
func TestMarkIndicator(t *testing.T) {
	n := &Node{Name: "x", Size: 10}
	if got := rowSpans(n, 100, true)[0].text; got != "* " {
		t.Errorf("marked gutter = %q, want %q", got, "* ")
	}
	if got := rowSpans(n, 100, false)[0].text; got != "  " {
		t.Errorf("unmarked gutter = %q, want %q", got, "  ")
	}
	if got := rowSpans(n, 100, false)[1].text; !strings.Contains(got, "10 B") {
		t.Errorf("size span should still follow the gutter, got %q", got)
	}
}

// TestJumpToLocation checks that jumping selects a mark inside its parent (for
// files and directories), and fails gracefully on a missing path.
func TestJumpToLocation(t *testing.T) {
	x := &Node{Name: "x", Path: "root/sub/x", Size: 50}
	sub := &Node{Name: "sub", Path: "root/sub", IsDir: true, Size: 50, Children: []*Node{x}}
	root := &Node{Name: "root", Path: "root", IsDir: true, Size: 50, Children: []*Node{sub}}
	sortChildren(root)
	sortChildren(sub)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}}

	e.handle([]byte("l")) // into sub
	e.handle([]byte("m")) // mark x
	e.handle([]byte("h")) // back to root
	if !e.jumpTo("root/sub/x") {
		t.Fatal("jumpTo should find root/sub/x")
	}
	if len(e.stack) != 2 || e.cur().Name != "sub" || e.selected() == nil || e.selected().Name != "x" {
		t.Fatalf("jump should select x in sub, got depth %d cur %q sel %v", len(e.stack), e.cur().Name, e.selected())
	}
	if e.sel[0] != 0 {
		t.Fatalf("root cursor = %d, want 0 (sub)", e.sel[0])
	}

	// A directory target is selected in its parent, not opened.
	if !e.jumpTo("root/sub") {
		t.Fatal("jumpTo should find root/sub")
	}
	if len(e.stack) != 1 || e.cur().Name != "root" || e.selected().Name != "sub" {
		t.Fatalf("jump to a dir should select it in root, got depth %d cur %q sel %v", len(e.stack), e.cur().Name, e.selected())
	}

	// Missing path: false, status set, stack unchanged.
	before := len(e.stack)
	if e.jumpTo("root/nope") {
		t.Fatal("jumpTo on a missing path should fail")
	}
	if len(e.stack) != before || e.status == "" {
		t.Fatal("a failed jump must leave the stack and set a status")
	}
}

// TestJumpRevealsDotfile checks that jumping to a mark hidden by the dotfile
// filter clears the filter so the target is reachable.
func TestJumpRevealsDotfile(t *testing.T) {
	x := &Node{Name: "x", Path: "root/.hidden/x", Size: 10}
	hid := &Node{Name: ".hidden", Path: "root/.hidden", IsDir: true, Size: 10, Children: []*Node{x}}
	vis := &Node{Name: "vis", Path: "root/vis", IsDir: true, Size: 20}
	root := &Node{Name: "root", Path: "root", IsDir: true, Size: 30, Children: []*Node{vis, hid}}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}, hideDot: true}
	e.marked = map[string]bool{"root/.hidden/x": true}
	e.marks = []string{"root/.hidden/x"}

	if !e.jumpTo("root/.hidden/x") {
		t.Fatal("jumpTo should find the dotfile target")
	}
	if e.hideDot {
		t.Fatal("jumping into a dot path should reveal it (clear hideDot)")
	}
	if e.cur().Name != ".hidden" || e.selected() == nil || e.selected().Name != "x" {
		t.Fatalf("expected x selected in .hidden, got cur %q sel %v", e.cur().Name, e.selected())
	}
}

// TestUnmarkFromPanel checks that 'm' inside the panel removes the highlighted
// mark and re-clamps the cursor.
func TestUnmarkFromPanel(t *testing.T) {
	root := &Node{Name: "root", IsDir: true, Children: []*Node{
		{Name: "a", Path: "root/a", Size: 2},
		{Name: "b", Path: "root/b", Size: 1},
	}}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}}
	e.set(0)
	e.handle([]byte("m"))
	e.set(1)
	e.handle([]byte("m"))
	e.handle([]byte("M")) // open panel
	if !e.marksView {
		t.Fatal("M should open the marks panel")
	}
	e.marksSel = 1
	e.handle([]byte("m")) // unmark within the panel
	if len(e.marks) != 1 || e.marked["root/b"] || e.marks[0] != "root/a" {
		t.Fatalf("panel m should unmark b: marks=%v", e.marks)
	}
	if e.marksSel != 0 {
		t.Fatalf("marksSel = %d, want clamped to 0", e.marksSel)
	}
}

// TestMarksPathFormatToggle checks 'p' flips the panel's path format.
func TestMarksPathFormatToggle(t *testing.T) {
	root := &Node{Name: "root", IsDir: true, Children: []*Node{{Name: "a", Path: "root/a", Size: 1}}}
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}}
	e.handle([]byte("m"))
	e.handle([]byte("M"))
	if e.marksRel {
		t.Fatal("panel should start showing absolute paths")
	}
	e.handle([]byte("a"))
	if !e.marksRel {
		t.Fatal("a should toggle to relative")
	}
	e.handle([]byte("a"))
	if e.marksRel {
		t.Fatal("a should toggle back to absolute")
	}
}

// TestMarksSurviveRescan checks that path-keyed marks persist across 'r' and the
// fresh node re-binds to the star.
func TestMarksSurviveRescan(t *testing.T) {
	dir, err := os.MkdirTemp(".", "dutest")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	writeFile(t, filepath.Join(dir, "a.txt"), 1000)

	sc := &scanner{apparent: true}
	root, err := scan(dir, sc)
	if err != nil {
		t.Fatal(err)
	}
	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}, sc: sc}

	marked := e.selected()
	e.handle([]byte("m"))
	if !e.marked[marked.Path] {
		t.Fatal("a.txt should be marked")
	}

	writeFile(t, filepath.Join(dir, "b.txt"), 2000)
	e.handle([]byte("r"))
	if !e.marked[marked.Path] {
		t.Fatal("mark should survive a rescan (keyed by path)")
	}
	var fresh *Node
	for _, c := range e.cur().Children {
		if c.Path == marked.Path {
			fresh = c
		}
	}
	if fresh == nil {
		t.Fatalf("fresh node for %q not found after rescan", marked.Path)
	}
	if rowSpans(fresh, e.cur().Size, e.marked[fresh.Path])[0].text != "* " {
		t.Fatal("the rescanned node should render the mark gutter")
	}
}

// TestMarkPaths covers the pure on-quit list: absolute conversion in order, and
// nil for no marks.
func TestMarkPaths(t *testing.T) {
	got := markPaths([]string{"a/b.txt", "/etc/hosts"}, "/home/u/proj")
	want := []string{"/home/u/proj/a/b.txt", "/etc/hosts"}
	if len(got) != len(want) {
		t.Fatalf("markPaths len %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("markPaths[%d] = %q, want %q", i, got[i], want[i])
		}
	}
	if markPaths(nil, "/home/u") != nil {
		t.Error("empty marks should yield nil")
	}
}
