//go:build unix

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// explorer is the state of the interactive disk browser. The stack is the path
// from the root to the directory currently on screen; sel and off track the
// selected row and scroll offset at each level so they are remembered when you
// go back up.
type explorer struct {
	stack  []*Node
	sel    []int
	off    []int
	tty    *os.File
	sc     *scanner // reused by 'r' to rescan with the same settings
	status string   // transient message shown in the footer until the next key

	stale   bool   // current directory changed on disk since it was scanned
	baseSig uint64 // fingerprint of the current dir at scan/rescan time
	baseDir string // path the baseline fingerprint belongs to
	flash   bool   // briefly highlight the selected row (copy feedback)
	help    bool   // the key reference overlay is showing
	hideDot bool   // hide dot ("hidden") entries from the listing

	// marks is a session-only staging area of entries to revisit, keyed by
	// Node.Path so they survive a rescan (which replaces nodes but reproduces the
	// same paths). marksView is a second overlay, modeled on help.
	marks     []string        // marked paths, in mark order
	marked    map[string]bool // membership set (lazy-init); drives the row star
	marksView bool            // the marks panel is showing
	marksSel  int             // cursor row within the panel
	marksOff  int             // panel scroll offset
	marksRel  bool            // panel shows paths relative to CWD (default: absolute)
}

// runTUI drives the interactive explorer on the controlling terminal. It always
// restores the terminal, even on Ctrl-C.
func runTUI(root *Node, path string, sc *scanner) ([]string, error) {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	defer tty.Close()

	orig, err := sttyState(tty)
	if err != nil {
		return nil, err
	}
	// cbreak: unbuffered, no echo, but signals stay on so Ctrl-C still quits.
	if err := stty(tty, "-echo", "-icanon", "min", "1", "time", "0"); err != nil {
		return nil, err
	}

	restore := func() {
		stty(tty, orig)
		fmt.Fprint(tty, "\x1b[?25h\x1b[?1049l") // show cursor, leave alt screen
	}
	defer restore()

	// External signals (or a closed terminal) still restore the terminal.
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-sig
		restore()
		os.Exit(130)
	}()

	// Window resizes trigger a full repaint.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	fmt.Fprint(tty, "\x1b[?1049h\x1b[?25l") // enter alt screen, hide cursor

	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}, tty: tty, sc: sc}
	wd, _ := os.Getwd() // launch dir; the program never chdirs, so it holds until quit

	// Read keys in a goroutine so the main loop can also wake on a timer to
	// re-check whether the current directory changed on disk.
	done := make(chan struct{})
	defer close(done)
	keys := make(chan []byte, 8)
	go func() {
		b := make([]byte, 16)
		for {
			n, err := tty.Read(b)
			if n > 0 {
				k := make([]byte, n)
				copy(k, b[:n])
				select {
				case keys <- k:
				case <-done:
					return
				}
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()

	ticker := time.NewTicker(750 * time.Millisecond)
	defer ticker.Stop()

	e.ensureBaseline()
	e.render()
	for {
		select {
		case k, ok := <-keys:
			if !ok {
				return markPaths(e.marks, wd), nil // terminal closed
			}
			if e.handle(k) {
				return markPaths(e.marks, wd), nil
			}
			e.ensureBaseline()
			e.render()
		case <-ticker.C:
			if e.pollStale() {
				e.render()
			}
		case <-winch:
			fmt.Fprint(e.tty, "\x1b[2J") // clear stale cells, then repaint at the new size
			e.render()
		}
	}
}

// ensureBaseline records the on-disk fingerprint of the current directory the
// first time it is shown (and after a rescan), so later polls can tell whether
// it has changed since.
func (e *explorer) ensureBaseline() {
	cur := e.cur()
	if cur.Path != e.baseDir {
		e.baseSig = dirSig(cur.Path)
		e.baseDir = cur.Path
		e.stale = false
	}
}

// pollStale re-fingerprints the current directory and updates the stale flag,
// reporting whether it changed so the caller can skip a needless redraw.
// Detection is limited to the current level: additions, removals, and
// size/mtime changes of immediate entries. Changes nested deeper are only seen
// by rescanning ('r').
func (e *explorer) pollStale() bool {
	e.ensureBaseline()
	was := e.stale
	e.stale = dirSig(e.cur().Path) != e.baseSig
	return e.stale != was
}

// dirSig is a cheap fingerprint of a directory's immediate contents — the
// names, sizes, mtimes and kinds of its entries. It changes when an entry is
// added, removed, or modified in place.
func dirSig(path string) uint64 {
	const (
		offset = 14695981039346656037
		prime  = 1099511628211
	)
	h := uint64(offset)
	mixByte := func(b byte) { h ^= uint64(b); h *= prime }
	mixStr := func(s string) {
		for i := 0; i < len(s); i++ {
			mixByte(s[i])
		}
	}
	mixU64 := func(v uint64) {
		for i := 0; i < 8; i++ {
			mixByte(byte(v))
			v >>= 8
		}
	}

	entries, err := os.ReadDir(path)
	if err != nil {
		mixStr("error:" + err.Error())
		return h
	}
	for _, ent := range entries {
		mixStr(ent.Name())
		info, err := ent.Info()
		if err != nil {
			mixByte(0)
			continue
		}
		mixU64(uint64(info.Size()))
		mixU64(uint64(info.ModTime().UnixNano()))
		if info.IsDir() {
			mixByte('d')
		} else {
			mixByte('f')
		}
	}
	return h
}

// cur is the directory currently shown.
func (e *explorer) cur() *Node { return e.stack[len(e.stack)-1] }

// handle applies every key in a read and reports whether to quit. A single
// read may carry several keys (batched or pasted input) and escape sequences
// for the arrow/navigation keys, so the buffer is consumed token by token.
func (e *explorer) handle(b []byte) bool {
	e.status = "" // any key clears the previous status line
	if e.marksView {
		return e.handleMarks(b)
	}
	if e.help {
		// While the help overlay is up, any key dismisses it; q still quits.
		for _, c := range b {
			if c == 'q' || c == 3 || c == 4 {
				return true
			}
		}
		e.help = false
		return false
	}
	for i := 0; i < len(b); i++ {
		c := b[i]

		// CSI escape sequence: ESC [ <final> , optionally with a trailing '~'.
		if c == 27 {
			if i+2 < len(b) && b[i+1] == '[' {
				switch b[i+2] {
				case 'A':
					e.move(-1)
				case 'B':
					e.move(1)
				case 'C':
					e.descend()
				case 'D':
					e.ascend()
				case 'H':
					e.set(0)
				case 'F':
					e.last()
				case '5': // Page Up
					e.move(-10)
				case '6': // Page Down
					e.move(10)
				}
				i += 2
				for i+1 < len(b) && b[i+1] == '~' {
					i++
				}
				continue
			}
			// A lone Esc backs out one level, like k9s — it never quits.
			e.ascend()
			continue
		}

		switch c {
		case 'q', 3, 4: // q, Ctrl-C (if delivered as a byte), Ctrl-D
			return true
		case 'c': // copy the selected absolute path
			e.copyPath(true)
		case 'C': // copy the selected path relative to the working directory
			e.copyPath(false)
		case 'f': // open the current folder in the file manager
			e.openFolder()
		case '?': // show the key reference
			e.help = true
		case 'j':
			e.move(1)
		case 'k':
			e.move(-1)
		case 'l', '\r', '\n', ' ':
			e.descend()
		case 'h':
			e.ascend()
		case 'r':
			e.rescan()
		case '.':
			e.toggleHidden()
		case 'm':
			e.toggleMark()
		case 'M':
			e.openMarks()
		case 'g':
			e.set(0)
		case 'G':
			e.last()
		}
	}
	return false
}

// visible returns n's children filtered for display: all of them, or only the
// non-dot entries when hideDot is set. The scanned tree and every node's totals
// are never mutated — hiding is purely a view concern.
func (e *explorer) visible(n *Node) []*Node {
	if !e.hideDot {
		return n.Children
	}
	out := make([]*Node, 0, len(n.Children))
	for _, c := range n.Children {
		if !strings.HasPrefix(c.Name, ".") {
			out = append(out, c)
		}
	}
	return out
}

// children is the visible entries of the directory currently on screen.
func (e *explorer) children() []*Node { return e.visible(e.cur()) }

// hiddenSize totals the immediate dot entries kept out of the current listing,
// so the header can report how much space the filter is hiding. It is 0 when the
// filter is off or the directory has no dot entries.
func (e *explorer) hiddenSize() int64 {
	if !e.hideDot {
		return 0
	}
	var n int64
	for _, c := range e.cur().Children {
		if strings.HasPrefix(c.Name, ".") {
			n += c.Size
		}
	}
	return n
}

// toggleHidden flips the dotfile filter, keeping each level's cursor on the same
// entry where it stays visible (and clamped into range otherwise).
func (e *explorer) toggleHidden() {
	// Remember the selected entry at every level before the visible set changes.
	target := make([]*Node, len(e.stack))
	for d := range e.stack {
		if kids := e.visible(e.stack[d]); e.sel[d] >= 0 && e.sel[d] < len(kids) {
			target[d] = kids[e.sel[d]]
		}
	}
	e.hideDot = !e.hideDot
	for d := range e.stack {
		kids := e.visible(e.stack[d])
		sel := 0
		for i, c := range kids {
			if c == target[d] {
				sel = i
				break
			}
		}
		if sel >= len(kids) {
			sel = len(kids) - 1
		}
		if sel < 0 {
			sel = 0
		}
		e.sel[d] = sel
	}
	if e.hideDot {
		e.status = "hiding dotfiles"
	} else {
		e.status = "showing all entries"
	}
}

// toggleMark adds or removes the selected entry from the staging area. Marks are
// keyed by path so they survive a rescan; the listing stars marked rows and the
// whole set prints on quit.
func (e *explorer) toggleMark() {
	n := e.selected()
	if n == nil {
		e.status = "nothing to mark"
		return
	}
	if e.marked[n.Path] {
		e.unmarkPath(n.Path)
		e.status = "unmarked " + n.Name
		return
	}
	if e.marked == nil {
		e.marked = map[string]bool{}
	}
	e.marked[n.Path] = true
	e.marks = append(e.marks, n.Path)
	e.status = fmt.Sprintf("marked %s  (%d marked)", n.Name, len(e.marks))
}

// unmarkPath removes a path from the staging area, preserving the order of the
// rest.
func (e *explorer) unmarkPath(path string) {
	delete(e.marked, path)
	for i, p := range e.marks {
		if p == path {
			e.marks = append(e.marks[:i], e.marks[i+1:]...)
			break
		}
	}
}

// markPaths renders the staged paths as absolute strings in mark order, for
// printing to stdout on quit. It is pure so it is testable without a terminal.
func markPaths(marks []string, wd string) []string {
	if len(marks) == 0 {
		return nil
	}
	out := make([]string, len(marks))
	for i, p := range marks {
		out[i] = displayPath(p, wd, true)
	}
	return out
}

// openMarks shows the marks panel with its cursor clamped into range.
func (e *explorer) openMarks() {
	e.marksView = true
	e.marksSet(e.marksSel)
}

// selected returns the entry the cursor is on, or nil when the listing is empty.
func (e *explorer) selected() *Node {
	kids := e.children()
	if len(kids) == 0 {
		return nil
	}
	i := e.sel[len(e.sel)-1]
	if i >= len(kids) {
		i = len(kids) - 1
	}
	return kids[i]
}

// copyPath puts the selected entry's path (or the current directory's, if the
// listing is empty) on the clipboard and blinks the row as confirmation. With
// absolute it copies the full path; otherwise the path relative to the working
// directory.
func (e *explorer) copyPath(absolute bool) {
	target := e.selected()
	raw := e.cur().Path
	if target != nil {
		raw = target.Path
	}
	wd, _ := os.Getwd()
	path := displayPath(raw, wd, absolute)
	if err := copyToClipboard(path); err != nil {
		e.status = "copy failed: " + err.Error()
		return
	}
	e.status = "copied " + path
	if target != nil {
		e.blink()
	}
}

// displayPath renders raw as either an absolute path or one relative to wd.
// raw may already be absolute or relative to wd (as stored in the tree).
func displayPath(raw, wd string, absolute bool) string {
	abs := raw
	if !filepath.IsAbs(abs) {
		abs = filepath.Join(wd, raw)
	}
	abs = filepath.Clean(abs)
	if absolute {
		return abs
	}
	if rel, err := filepath.Rel(wd, abs); err == nil {
		return rel
	}
	return abs
}

// openFolder reveals the directory currently on screen in the OS file manager.
func (e *explorer) openFolder() {
	path := e.cur().Path
	if err := openInFileManager(path); err != nil {
		e.status = "open failed: " + err.Error()
		return
	}
	e.status = "opened " + path
}

// blink briefly flashes the selected row to confirm an action.
func (e *explorer) blink() {
	for i := 0; i < 2; i++ {
		e.flash = true
		e.render()
		time.Sleep(70 * time.Millisecond)
		e.flash = false
		e.render()
		time.Sleep(70 * time.Millisecond)
	}
}

// copyToClipboard writes text to the system clipboard, picking the right tool
// for the platform.
func copyToClipboard(text string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		for _, c := range [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		} {
			if _, err := exec.LookPath(c[0]); err == nil {
				cmd = exec.Command(c[0], c[1:]...)
				break
			}
		}
	}
	if cmd == nil {
		return errors.New("no clipboard tool found")
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run()
}

// openInFileManager opens a path in the desktop file manager (Finder on macOS).
func openInFileManager(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Run()
	case "linux":
		return exec.Command("xdg-open", path).Run()
	default:
		return errors.New("unsupported platform")
	}
}

// rescan re-reads the directory currently on screen from disk and folds the
// size changes back into its ancestors, so edits made since the initial scan
// are reflected. At the root this is a full refresh; deeper it only re-walks
// the current subtree.
func (e *explorer) rescan() {
	cur := e.cur()
	if !cur.IsDir {
		return
	}
	fresh, err := scan(cur.Path, e.sc)
	if err != nil {
		e.status = "rescan failed: " + err.Error()
		return
	}
	sortChildren(fresh)

	depth := len(e.stack) - 1
	if depth > 0 {
		parent := e.stack[depth-1]
		for i, c := range parent.Children {
			if c == cur {
				parent.Children[i] = fresh
				break
			}
		}
		// Every ancestor's totals include this subtree, so shift them all by
		// the same delta.
		dSize := fresh.Size - cur.Size
		dFiles := fresh.Files - cur.Files
		dDirs := fresh.Dirs - cur.Dirs
		for i := 0; i < depth; i++ {
			e.stack[i].Size += dSize
			e.stack[i].Files += dFiles
			e.stack[i].Dirs += dDirs
		}
		// Re-sort the parent and keep its cursor on the refreshed directory.
		sortChildren(parent)
		for i, c := range parent.Children {
			if c == fresh {
				e.sel[depth-1] = i
				break
			}
		}
	}
	e.stack[depth] = fresh

	// Keep the selection within the (possibly smaller) listing.
	if n := len(fresh.Children); e.sel[depth] >= n {
		e.sel[depth] = n - 1
	}
	if e.sel[depth] < 0 {
		e.sel[depth] = 0
	}
	e.baseDir = "" // force a fresh staleness baseline for the rescanned dir
	e.stale = false
	e.status = "rescanned " + cur.Path
}

// last selects the final entry in the current directory.
func (e *explorer) last() { e.set(len(e.children()) - 1) }

func (e *explorer) move(d int) { e.set(e.sel[len(e.sel)-1] + d) }

func (e *explorer) set(i int) {
	n := len(e.children())
	if n == 0 {
		e.sel[len(e.sel)-1] = 0
		return
	}
	if i < 0 {
		i = 0
	}
	if i >= n {
		i = n - 1
	}
	e.sel[len(e.sel)-1] = i
}

// descend opens the selected directory.
func (e *explorer) descend() {
	kids := e.children()
	if len(kids) == 0 {
		return
	}
	i := e.sel[len(e.sel)-1]
	if i >= len(kids) {
		i = len(kids) - 1
	}
	child := kids[i]
	if !child.IsDir {
		return
	}
	sortChildren(child)
	e.stack = append(e.stack, child)
	e.sel = append(e.sel, 0)
	e.off = append(e.off, 0)
}

// ascend returns to the parent directory.
func (e *explorer) ascend() {
	if len(e.stack) <= 1 {
		return
	}
	e.stack = e.stack[:len(e.stack)-1]
	e.sel = e.sel[:len(e.sel)-1]
	e.off = e.off[:len(e.off)-1]
}

// render paints one frame.
func (e *explorer) render() {
	rows, cols := e.size()
	if rows < 3 {
		rows = 3
	}
	if cols < 20 {
		cols = 20
	}
	if e.marksView {
		e.renderMarks(rows, cols)
		return
	}
	if e.help {
		e.renderHelp(rows, cols)
		return
	}
	cur := e.cur()
	total := cur.Size
	kids := e.children()
	body := rows - 2

	sel := e.sel[len(e.sel)-1]
	if sel >= len(kids) {
		sel = len(kids) - 1
	}
	if sel < 0 {
		sel = 0
	}
	e.sel[len(e.sel)-1] = sel

	off := e.off[len(e.off)-1]
	if sel < off {
		off = sel
	}
	if sel >= off+body {
		off = sel - body + 1
	}
	if off < 0 {
		off = 0
	}
	e.off[len(e.off)-1] = off

	var sb strings.Builder
	sb.WriteString("\x1b[H")

	head := headLabel(cur, e.stale)
	if e.hideDot {
		head += "   · dotfiles hidden"
		if h := e.hiddenSize(); h > 0 {
			head += fmt.Sprintf(" (%s, %.0f%%)", humanize(h), float64(h)/float64(total)*100)
		}
	}
	sb.WriteString("\x1b[7m" + fit(head, cols) + "\x1b[0m\r\n")

	for i := 0; i < body; i++ {
		idx := off + i
		sb.WriteString("\x1b[K")
		if idx >= len(kids) {
			sb.WriteString("\r\n")
			continue
		}
		spans := rowSpans(kids[idx], total, e.marked[kids[idx].Path])
		if idx == sel {
			// The highlighted row sits under a row-wide reverse-video (or flash)
			// highlight, so its spans carry no color resets that would cut the
			// highlight short. The exception is the meter fill, which spanSelected
			// keeps cyan instead of letting it invert to the terminal background.
			style, rowStyle := "\x1b[7m", spanPlain // reverse video for the cursor
			if e.flash {
				style = "\x1b[30;42m" // black on green: copy confirmation
			} else if useColor {
				rowStyle = spanSelected
			}
			sb.WriteString(style + fitSpans(spans, cols, rowStyle) + "\x1b[0m")
		} else {
			rowStyle := spanPlain
			if useColor {
				rowStyle = spanColor
			}
			sb.WriteString(fitSpans(spans, cols, rowStyle))
		}
		sb.WriteString("\r\n")
	}

	pos := "0/0"
	if len(kids) > 0 {
		pos = fmt.Sprintf("%d/%d", sel+1, len(kids))
	}
	if len(e.marks) > 0 {
		pos += fmt.Sprintf("  *%d", len(e.marks))
	}
	foot := fmt.Sprintf(" ↑/↓ move  → open  ← back  m mark  M marks  ? help  q quit    %s", pos)
	if e.status != "" {
		foot = " " + e.status
	}
	sb.WriteString("\x1b[K\x1b[7m" + fit(foot, cols) + "\x1b[0m")

	fmt.Fprint(e.tty, sb.String())
}

// renderHelp draws the key reference overlay. Any key dismisses it.
func (e *explorer) renderHelp(rows, cols int) {
	keys := []string{
		"  ↑/↓   j/k         move",
		"  →/l   Enter       open directory",
		"  ←/h   Esc         go up (back)",
		"  g/G               first / last",
		"  c                 copy absolute path",
		"  C                 copy relative path",
		"  f                 open folder in Finder",
		"  r                 rescan from disk",
		"  .                 show/hide dotfiles",
		"  m                 mark / unmark entry",
		"  M                 marks panel",
		"  ?                 toggle this help",
		"  q                 quit",
	}
	var sb strings.Builder
	sb.WriteString("\x1b[H")
	for i := 0; i < rows; i++ {
		sb.WriteString("\x1b[K")
		switch {
		case i == 0:
			sb.WriteString("\x1b[7m" + fit(" Keys", cols) + "\x1b[0m")
		case i-2 >= 0 && i-2 < len(keys):
			sb.WriteString(fit(keys[i-2], cols))
		case i == rows-1:
			sb.WriteString("\x1b[7m" + fit(" press any key to return", cols) + "\x1b[0m")
		}
		if i < rows-1 {
			sb.WriteString("\r\n")
		}
	}
	fmt.Fprint(e.tty, sb.String())
}

// handleMarks dispatches keys while the marks panel is open. Like the help
// overlay it owns all input: j/k move, Enter/l/space jump to a mark's location,
// m/d/x unmark, p toggles the path format, Esc/M close, q/Ctrl-C/Ctrl-D quit.
func (e *explorer) handleMarks(b []byte) bool {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 27 { // CSI arrows, or a lone Esc that closes the panel
			if i+2 < len(b) && b[i+1] == '[' {
				switch b[i+2] {
				case 'A':
					e.marksMove(-1)
				case 'B':
					e.marksMove(1)
				case 'H':
					e.marksSet(0)
				case 'F':
					e.marksSet(len(e.marks) - 1)
				}
				i += 2
				for i+1 < len(b) && b[i+1] == '~' {
					i++
				}
				continue
			}
			e.marksView = false
			continue
		}
		switch c {
		case 'q', 3, 4:
			return true
		case 'M':
			e.marksView = false
		case 'j':
			e.marksMove(1)
		case 'k':
			e.marksMove(-1)
		case 'g':
			e.marksSet(0)
		case 'G':
			e.marksSet(len(e.marks) - 1)
		case 'a':
			e.marksRel = !e.marksRel
		case 'm', 'd', 'x':
			e.unmarkAt(e.marksSel)
		case 'l', '\r', '\n', ' ':
			e.jumpToMark()
		}
	}
	return false
}

func (e *explorer) marksMove(d int) { e.marksSet(e.marksSel + d) }

func (e *explorer) marksSet(i int) {
	if i >= len(e.marks) {
		i = len(e.marks) - 1
	}
	if i < 0 {
		i = 0
	}
	e.marksSel = i
}

// unmarkAt removes the mark at panel row i and keeps the cursor in range.
func (e *explorer) unmarkAt(i int) {
	if i < 0 || i >= len(e.marks) {
		return
	}
	e.unmarkPath(e.marks[i])
	e.marksSet(e.marksSel)
}

// jumpToMark navigates to the highlighted mark's location and closes the panel.
// A mark whose path is gone (deleted then rescanned away) leaves the panel open
// with an explanatory status.
func (e *explorer) jumpToMark() {
	if e.marksSel < 0 || e.marksSel >= len(e.marks) {
		return
	}
	if e.jumpTo(e.marks[e.marksSel]) {
		e.marksView = false
	}
}

// locate returns the chain of nodes from root down to the entry at target
// (inclusive), or nil if target is not in the in-memory tree.
func locate(root *Node, target string) []*Node {
	rel, err := filepath.Rel(root.Path, target)
	if err != nil {
		return nil
	}
	chain := []*Node{root}
	if rel == "." {
		return chain
	}
	n := root
	for _, name := range strings.Split(rel, string(os.PathSeparator)) {
		var next *Node
		for _, c := range n.Children {
			if c.Name == name {
				next = c
				break
			}
		}
		if next == nil {
			return nil
		}
		chain = append(chain, next)
		n = next
	}
	return chain
}

// jumpTo rebuilds the navigation stack so target is selected inside its parent
// (it never opens target, so files and directories behave the same). It reports
// whether the path was found.
func (e *explorer) jumpTo(target string) bool {
	chain := locate(e.stack[0], target)
	if chain == nil {
		e.status = "can't find " + target + " — press r to rescan"
		return false
	}
	// Reveal the target if the dotfile filter would hide it or an ancestor.
	if e.hideDot {
		for _, n := range chain[1:] {
			if strings.HasPrefix(n.Name, ".") {
				e.hideDot = false
				break
			}
		}
	}
	if len(chain) == 1 { // target is the root itself
		e.stack, e.sel, e.off = chain, []int{0}, []int{0}
		return true
	}
	stack := chain[:len(chain)-1] // down to the parent
	sel := make([]int, len(stack))
	off := make([]int, len(stack))
	for d := range stack {
		want := chain[d+1] // the next node toward (and finally at) the target
		for i, c := range e.visible(stack[d]) {
			if c == want {
				sel[d] = i
				break
			}
		}
	}
	e.stack, e.sel, e.off = stack, sel, off
	return true
}

// renderMarks draws the marks panel: the staged paths (relative to the launch
// dir, or absolute with 'p'), with the cursor row highlighted.
func (e *explorer) renderMarks(rows, cols int) {
	wd, _ := os.Getwd()
	e.marksSet(e.marksSel) // keep the cursor in range after unmarking
	body := rows - 2
	if e.marksSel < e.marksOff {
		e.marksOff = e.marksSel
	}
	if e.marksSel >= e.marksOff+body {
		e.marksOff = e.marksSel - body + 1
	}
	if e.marksOff < 0 {
		e.marksOff = 0
	}

	format := "absolute (a: relative)"
	if e.marksRel {
		format = "relative to CWD (a: absolute)"
	}

	var sb strings.Builder
	sb.WriteString("\x1b[H")
	sb.WriteString("\x1b[7m" + fit(fmt.Sprintf(" Marks (%d) — %s", len(e.marks), format), cols) + "\x1b[0m\r\n")

	for i := 0; i < body; i++ {
		sb.WriteString("\x1b[K")
		idx := e.marksOff + i
		switch {
		case len(e.marks) == 0 && i == 0:
			sb.WriteString(fit("  (no marks yet — press m in the explorer)", cols))
		case idx < len(e.marks):
			line := " " + displayPath(e.marks[idx], wd, !e.marksRel)
			if idx == e.marksSel {
				sb.WriteString("\x1b[7m" + fit(line, cols) + "\x1b[0m")
			} else {
				sb.WriteString(fit(line, cols))
			}
		}
		sb.WriteString("\r\n")
	}

	foot := " ↑/↓ move  ↵ go  m unmark  a abs/rel  Esc back  q quit"
	if e.status != "" {
		foot = " " + e.status
	}
	sb.WriteString("\x1b[K\x1b[7m" + fit(foot, cols) + "\x1b[0m")
	fmt.Fprint(e.tty, sb.String())
}

// headLabel is the top line for a directory: its path, totals, and a marker
// when it has changed on disk since it was scanned.
func headLabel(n *Node, stale bool) string {
	s := fmt.Sprintf(" %s — %s · %s files · %s dirs",
		n.Path, humanize(n.Size), comma(n.Files), comma(n.Dirs))
	if stale {
		s += "   ● changed on disk — press r"
	}
	return s
}

// styledSpan is a run of text with an optional SGR parameter ("" means no
// color). Rows are assembled from spans so their width can be measured in
// visible runes independently of the color escapes wrapped around them.
type styledSpan struct {
	text string
	code string
}

// rowSpans builds the styled segments of one entry's row: a mark gutter ("* "
// when staged, else blank), then the non-interactive report's columns (see meter
// and label in report.go) — a plain right-aligned size, a dim percentage, a cyan
// usage bar, two spaces, and the name (bold blue with a trailing slash for
// directories). The segments are returned untruncated; fitSpans lays them out to
// the available width.
func rowSpans(n *Node, total int64, marked bool) []styledSpan {
	frac := 0.0
	if total > 0 {
		frac = float64(n.Size) / float64(total)
	}
	name := n.Name
	nameCode := ""
	if n.IsDir {
		name += "/"
		nameCode = "1;34"
	}
	gutter, gutterCode := "  ", ""
	if marked {
		gutter, gutterCode = "* ", "33" // a yellow star flags a staged entry
	}
	return []styledSpan{
		{gutter, gutterCode},
		{fmt.Sprintf("%10s ", humanize(n.Size)), ""},
		{fmt.Sprintf("%4.0f%%", frac*100), "2"},
		{" ", ""},
		{bar(frac, 16), "36"},
		{"  ", ""},
		{name, nameCode},
	}
}

// fitSpans renders spans to exactly w columns: it truncates with a trailing
// ellipsis when the text overflows and right-pads with spaces when it falls
// short, measuring width in runes while ignoring the SGR escapes. The style
// callback turns each span's (text, code) into its on-screen form; see
// spanColor, spanPlain, and spanSelected.
func fitSpans(spans []styledSpan, w int, style func(text, code string) string) string {
	if w < 0 {
		w = 0
	}
	var b strings.Builder
	used := 0
	for _, s := range spans {
		if used >= w {
			break
		}
		r := []rune(s.text)
		if used+len(r) <= w {
			b.WriteString(style(s.text, s.code))
			used += len(r)
			continue
		}
		// This span overflows the line; truncate it so the row ends with an
		// ellipsis at exactly column w.
		take := w - used // room left, always >= 1 here
		if take == 1 {
			b.WriteString(style("…", s.code))
		} else {
			b.WriteString(style(string(r[:take-1])+"…", s.code))
		}
		used = w
		break
	}
	if used < w {
		b.WriteString(strings.Repeat(" ", w-used))
	}
	return b.String()
}

// spanColor wraps text in its SGR code when one is set; used for ordinary,
// unselected rows.
func spanColor(text, code string) string {
	if code == "" {
		return text
	}
	return "\x1b[" + code + "m" + text + "\x1b[0m"
}

// spanPlain emits text with no styling. It is used under a row-wide reverse-video
// highlight (where an embedded SGR reset would cut the highlight short) and when
// color is disabled.
func spanPlain(text, _ string) string { return text }

// spanSelected is spanPlain except it paints the meter fill (code "36") with a
// cyan background; under the selected row's reverse video that shows as a cyan
// foreground, so the filled blocks stay cyan rather than inverting to the
// terminal background. The background is reset right after, so the rest of the
// row keeps the plain reverse-video highlight.
func spanSelected(text, code string) string {
	if code == "36" {
		return "\x1b[46m" + text + "\x1b[49m"
	}
	return text
}

// fit truncates s with an ellipsis or right-pads it with spaces to exactly w
// runes.
func fit(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		if w <= 1 {
			return string(r[:w])
		}
		return string(r[:w-1]) + "…"
	}
	return s + strings.Repeat(" ", w-len(r))
}

// size returns the terminal dimensions, falling back to 24x80.
func (e *explorer) size() (rows, cols int) {
	rows, cols = 24, 80
	cmd := exec.Command("stty", "size")
	cmd.Stdin = e.tty
	out, err := cmd.Output()
	if err != nil {
		return
	}
	f := strings.Fields(string(out))
	if len(f) == 2 {
		if r, err := strconv.Atoi(f[0]); err == nil {
			rows = r
		}
		if c, err := strconv.Atoi(f[1]); err == nil {
			cols = c
		}
	}
	return
}

// sttyState captures the current terminal settings so they can be restored.
func sttyState(tty *os.File) (string, error) {
	cmd := exec.Command("stty", "-g")
	cmd.Stdin = tty
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// stty runs stty against the terminal with the given arguments.
func stty(tty *os.File, args ...string) error {
	cmd := exec.Command("stty", args...)
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	return cmd.Run()
}
