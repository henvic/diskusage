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
	wd     string   // the directory diskusage was launched from
	status string   // transient message shown in the footer until the next key

	rows, cols int // terminal size, measured at startup and again on SIGWINCH

	scanning bool // the initial scan is still running; the stack holds snapshots

	// job is the 'r' rescan running in the background, or nil. The explorer
	// keeps browsing the old tree meanwhile and splices the result in when the
	// walk finishes, so a big subtree no longer freezes the screen.
	job *rescanJob

	stale   bool   // current directory changed on disk since it was scanned
	baseSig uint64 // fingerprint of the current dir at scan/rescan time
	baseDir string // path the baseline fingerprint belongs to
	flash   bool   // briefly highlight the selected row (copy feedback)
	help    bool   // the key reference overlay is showing
	hideDot bool   // hide dot ("hidden") entries from the listing

	// apparent shows and sorts by apparent size instead of space on disk (the
	// 'a' key). Both sizes are in every node, so switching is a re-sort only.
	apparent bool

	// marks is a session-only staging area of entries to revisit, keyed by
	// Node.Path so they survive a rescan (which replaces nodes but reproduces the
	// same paths). marksView is a second overlay, modeled on help.
	marks     []string        // marked paths, in mark order
	marked    map[string]bool // membership set (lazy-init); drives the row star
	marksView bool            // the marks panel is showing
	marksSel  int             // cursor row within the panel
	marksOff  int             // panel scroll offset
	marksRel  bool            // panel shows paths relative to CWD (default: absolute)
	marksGone map[string]bool // marks no longer in the tree; rebuilt when the panel opens
}

// rescanJob is one background 'r' refresh: the node being replaced, the fresh
// tree the scanner is filling, and the scanned counter at the start (the
// counter is cumulative across scans, so progress is the difference).
type rescanJob struct {
	old   *Node
	fresh *Node
	done  <-chan struct{}
	base  int64
}

// escDelay is how long a lone Esc byte waits for the rest of an escape
// sequence. Terminals send an arrow key as one write (ESC [ B), but a slow or
// lossy link can split it, and without the wait the ESC would back out a level
// and the "[B" would be dropped. Only a bare Esc press pays this latency; any
// other key that follows resolves it at once.
const escDelay = 250 * time.Millisecond

// runTUI drives the interactive explorer on the controlling terminal. It always
// restores the terminal, even on Ctrl-C. root may still be filling in: until
// done closes, the explorer browses periodic snapshots of the tree (the live
// nodes are only safe to read through scanner.snapshot), then it adopts the
// finished tree. apparent picks the size measure the explorer starts with.
func runTUI(root *Node, path string, sc *scanner, done <-chan struct{}, apparent bool) ([]string, error) {
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
	cbreak := func() error { return stty(tty, "-echo", "-icanon", "min", "1", "time", "0") }
	if err := cbreak(); err != nil {
		return nil, err
	}

	restore := func() {
		stty(tty, orig)                                 // #nosec G104 -- best-effort cleanup; nowhere to report a failure
		fmt.Fprint(tty, "\x1b[?25h\x1b[?7h\x1b[?1049l") // show cursor, re-enable autowrap, leave alt screen
	}
	defer restore()

	// Quit signals are handled in the main loop below, so Ctrl-C behaves exactly
	// like q: the deferred restore runs and the marks still print.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)

	// The loop can be busy for a while (a rescan of a big subtree), leaving the
	// first signal queued. A second one means "really quit": bail out now.
	force := make(chan os.Signal, 2)
	signal.Notify(force, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		<-force
		<-force
		restore()
		os.Exit(130)
	}()

	// Ctrl-Z: the terminal must be restored before the process stops, and set up
	// again when it resumes, or the user gets their shell back with no echo.
	tstp := make(chan os.Signal, 1)
	signal.Notify(tstp, syscall.SIGTSTP)

	// Window resizes trigger a re-measure and a full repaint.
	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)

	// Alt screen, hidden cursor, and no autowrap: an overwide row (e.g. a name
	// with double-width runes) then clips instead of wrapping and shifting the
	// frame.
	enter := func() { fmt.Fprint(tty, "\x1b[?1049h\x1b[?25l\x1b[?7l") }
	enter()

	// While the scan is still running, browse a private snapshot; the live tree
	// is mutated concurrently and is only read via scanner.snapshot.
	scanning := true
	select {
	case <-done:
		scanning = false
	default:
	}
	browse := root
	if scanning {
		browse = sc.snapshot(root)
	}
	sortChildren(browse, apparent)

	wd, _ := os.Getwd() // launch dir; the program never chdirs, so it holds until quit
	e := &explorer{stack: []*Node{browse}, sel: []int{0}, off: []int{0}, tty: tty, sc: sc, wd: wd, apparent: apparent}
	e.scanning = scanning
	e.measure()

	// repaint re-measures and redraws from scratch, for the paths where the
	// whole screen may be stale: a resize, or resuming from Ctrl-Z.
	repaint := func() {
		e.measure()
		fmt.Fprint(e.tty, "\x1b[2J")
		e.render()
	}

	// Read keys in a goroutine so the main loop can also wake on a timer to
	// refresh the scan snapshot or re-check whether the directory changed on
	// disk.
	stop := make(chan struct{})
	defer close(stop)
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
				case <-stop:
					return
				}
			}
			if err != nil {
				close(keys)
				return
			}
		}
	}()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	var nextSnap time.Time

	// pending holds a trailing, possibly incomplete escape sequence until the
	// rest of it arrives or escTimer fires (see escDelay).
	var pending []byte
	var escTimer <-chan time.Time

	e.ensureBaseline()
	e.render()
	for {
		var rescanDone <-chan struct{}
		if e.job != nil {
			rescanDone = e.job.done
		}
		select {
		case k, ok := <-keys:
			if !ok {
				return markPaths(e.marks, wd), nil // terminal closed
			}
			var ready []byte
			ready, pending = splitPendingEsc(append(pending, k...))
			escTimer = nil
			if len(pending) > 0 {
				escTimer = time.After(escDelay)
			}
			if len(ready) == 0 {
				continue
			}
			if e.handle(ready) {
				return markPaths(e.marks, wd), nil
			}
			e.ensureBaseline()
			e.render()
		case <-escTimer:
			// Nothing followed the Esc in time: it was a real Esc key press.
			escTimer = nil
			ready := pending
			pending = nil
			if e.handle(ready) {
				return markPaths(e.marks, wd), nil
			}
			e.ensureBaseline()
			e.render()
		case <-rescanDone:
			e.finishRescan()
			e.ensureBaseline()
			e.render()
		case <-ticker.C:
			if e.job != nil {
				e.render() // advance the rescan counter
			}
			if e.scanning {
				if time.Now().After(nextSnap) {
					start := time.Now()
					e.adopt(e.sc.snapshot(root))
					// Copying pauses the walk, so back off to at least 5x the
					// copy cost: snapshotting a huge tree must never dominate
					// the scan it is showing.
					nextSnap = time.Now().Add(5 * time.Since(start))
					e.render()
				}
			} else if e.pollStale() {
				e.render()
			}
		case <-done:
			done = nil // a closed channel would fire on every loop iteration
			e.scanning = false
			e.adopt(root) // the live tree is quiescent now; browse it directly
			e.baseDir = ""
			e.ensureBaseline()
			e.status = "scan complete"
			e.render()
		case <-winch:
			repaint()
		case <-quit:
			return markPaths(e.marks, wd), nil
		case <-tstp:
			restore()
			signal.Reset(syscall.SIGTSTP)
			// Actually stop, with the default action restored.
			syscall.Kill(syscall.Getpid(), syscall.SIGTSTP) // #nosec G104 -- best effort; worst case we keep running
			// Execution resumes here on SIGCONT (fg/bg). Redo the whole setup;
			// the window may have been resized while suspended.
			signal.Notify(tstp, syscall.SIGTSTP)
			cbreak() // #nosec G104 -- it worked at startup; nowhere to report a failure now
			enter()
			repaint()
		}
	}
}

// splitPendingEsc splits input into the bytes ready to handle and a trailing
// escape sequence that may still be incomplete: a bare ESC, ESC [ or ESC O, or
// ESC [ followed only by parameter digits (Page Up/Down end in '~'). The
// incomplete tail is returned as a fresh slice so the caller can keep it
// across reads.
func splitPendingEsc(b []byte) (ready, pending []byte) {
	i := len(b) - 1
	for i >= 0 && b[i] != 27 {
		i--
	}
	if i < 0 {
		return b, nil
	}
	tail := b[i:]
	incomplete := len(tail) == 1 ||
		(len(tail) == 2 && (tail[1] == '[' || tail[1] == 'O'))
	if !incomplete && tail[1] == '[' {
		incomplete = true
		for _, c := range tail[2:] {
			if (c < '0' || c > '9') && c != ';' {
				incomplete = false
				break
			}
		}
	}
	if !incomplete {
		return b, nil
	}
	return b[:i], append([]byte(nil), tail...)
}

// measure refreshes the cached terminal size. Spawning stty is too costly to
// repeat per frame, so this runs once at startup and on SIGWINCH.
func (e *explorer) measure() { e.rows, e.cols = e.size() }

// adopt swaps the tree the explorer is browsing — a fresh snapshot mid-scan,
// the live tree once the scan completes, or the tree with a finished rescan
// spliced in — reopening the same directory path and keeping each level's
// cursor on the same entry. A level is dropped only if the new tree lacks it
// (a rescan can find a directory deleted), and a cursor whose entry vanished
// keeps its old row, clamped to the listing.
func (e *explorer) adopt(root *Node) {
	// Remember where the cursor sits at every open level, by path.
	want := make([]string, len(e.stack))
	for d := range e.stack {
		if kids := e.visible(e.stack[d]); len(kids) > 0 {
			i := e.sel[d]
			if i >= len(kids) {
				i = len(kids) - 1
			}
			if i < 0 {
				i = 0
			}
			want[d] = kids[i].Path
		}
	}

	sortChildren(root, e.apparent)
	stack, sel, off := []*Node{root}, []int{0}, []int{0}
	for d := 1; d < len(e.stack); d++ {
		var next *Node
		for _, c := range stack[d-1].Children {
			if c.Path == e.stack[d].Path {
				next = c
				break
			}
		}
		if next == nil {
			break
		}
		sortChildren(next, e.apparent)
		stack = append(stack, next)
		sel = append(sel, 0)
		off = append(off, e.off[d])
	}
	for d := range stack {
		kids := e.visible(stack[d])
		sel[d] = min(e.sel[d], len(kids)-1)
		if sel[d] < 0 {
			sel[d] = 0
		}
		for i, c := range kids {
			if c.Path == want[d] {
				sel[d] = i
				break
			}
		}
	}
	e.stack, e.sel, e.off = stack, sel, off
}

// ensureBaseline records the on-disk fingerprint of the current directory the
// first time it is shown (and after a rescan), so later polls can tell whether
// it has changed since.
func (e *explorer) ensureBaseline() {
	if e.scanning {
		return // everything is provisional until the scan itself finishes
	}
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
	if e.scanning {
		return false
	}
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
		mixU64(uint64(info.Size()))               // #nosec G115 -- hash mixing; wraparound is fine
		mixU64(uint64(info.ModTime().UnixNano())) // #nosec G115 -- hash mixing; wraparound is fine
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

		// Escape sequence: ESC [ <final> (CSI) or ESC O <final> (SS3, sent for
		// Home/End by some terminal modes), optionally with a trailing '~'.
		if c == 27 {
			if i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
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
		case 'o': // open the selected entry with its default app
			e.openEntry()
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
		case 'a':
			e.toggleMeasure()
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
			n += c.bytes(e.apparent)
		}
	}
	return n
}

// toggleMeasure switches the listing between size on disk and apparent size,
// re-sorting every open level while keeping the cursor on the same entries.
func (e *explorer) toggleMeasure() {
	e.apparent = !e.apparent
	e.adopt(e.stack[0])
	if e.apparent {
		e.status = "showing apparent size — press a for size on disk"
	} else {
		e.status = "showing size on disk — press a for apparent size"
	}
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

// openMarks shows the marks panel with its cursor clamped into range. Marks
// whose paths are no longer in the tree are noted here rather than per frame:
// gone-ness only changes on rescan, which the panel itself cannot trigger.
func (e *explorer) openMarks() {
	e.marksGone = nil
	for _, p := range e.marks {
		if locate(e.stack[0], p) == nil {
			if e.marksGone == nil {
				e.marksGone = map[string]bool{}
			}
			e.marksGone[p] = true
		}
	}
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
	path := displayPath(raw, e.wd, absolute)
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
	e.open(e.cur().Path)
}

// openEntry opens the selected entry with its default app; a directory opens
// in the file manager.
func (e *explorer) openEntry() {
	target := e.selected()
	if target == nil {
		e.status = "nothing to open"
		return
	}
	e.open(target.Path)
}

// open hands raw to the desktop's default handler and reports the outcome on
// the status line. The path is made absolute first so a name starting with "-"
// cannot be taken for an option of the opener.
func (e *explorer) open(raw string) {
	path := displayPath(raw, e.wd, true)
	if err := openPath(path); err != nil {
		e.status = "open failed: " + err.Error()
		return
	}
	e.status = "opened " + path
}

// blink briefly flashes the selected row to confirm an action. Headless (no
// tty) there is nothing to flash and no reason to sleep.
func (e *explorer) blink() {
	if e.tty == nil {
		return
	}
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
	default: // Linux, the BSDs, and other Unixes running Wayland or X11
		for _, c := range [][]string{
			{"wl-copy"},
			{"xclip", "-selection", "clipboard"},
			{"xsel", "--clipboard", "--input"},
		} {
			if _, err := exec.LookPath(c[0]); err == nil {
				cmd = exec.Command(c[0], c[1:]...) // #nosec G204 -- fixed list above, no user input
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

// openPath opens a path with the desktop's default handler: a file in its
// associated app, a directory in the file manager (Finder on macOS).
func openPath(path string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", path).Run() // #nosec G204 -- fixed program, no shell; path is absolute, so never an option
	default: // Linux, the BSDs, and other Unixes with a freedesktop desktop
		return exec.Command("xdg-open", path).Run() // #nosec G204 -- fixed program, no shell; path is absolute, so never an option
	}
}

// rescan starts re-reading the directory currently on screen from disk, in the
// background. At the root this is a full refresh; deeper it only re-walks the
// current subtree. The explorer stays responsive meanwhile, showing the old
// numbers with a progress counter, and finishRescan splices the result in.
func (e *explorer) rescan() {
	if e.scanning {
		e.status = "still scanning — everything refreshes as it goes"
		return
	}
	if e.job != nil {
		e.status = "already rescanning " + e.job.old.Path
		return
	}
	cur := e.cur()
	if !cur.IsDir {
		return
	}
	base := e.sc.scanned.Load()
	fresh, done, err := scanStart(cur.Path, e.sc)
	if err != nil {
		e.status = "rescan failed: " + err.Error()
		return
	}
	e.job = &rescanJob{old: cur, fresh: fresh, done: done, base: base}
	e.status = "rescanning " + cur.Path + "…"
}

// finishRescan splices a completed rescan into the tree: the fresh subtree
// replaces the old node and every ancestor's totals shift by the difference.
// The user may have navigated anywhere meanwhile, so the view is then rebuilt
// by path with adopt, which keeps each level's cursor on the same entry. Only
// one rescan runs at a time and nothing else mutates the tree after the
// initial scan, so the old node is still where it was.
func (e *explorer) finishRescan() {
	job := e.job
	e.job = nil
	<-job.done // already closed when called from the loop; waits in tests
	fresh, old := job.fresh, job.old
	sortChildren(fresh, e.apparent)

	root := e.stack[0]
	chain := locate(root, old.Path)
	if chain == nil || chain[len(chain)-1] != old {
		e.status = "rescan of " + old.Path + " discarded: the tree changed"
		return
	}
	if len(chain) == 1 {
		root = fresh
	} else {
		parent := chain[len(chain)-2]
		for i, c := range parent.Children {
			if c == old {
				parent.Children[i] = fresh
				break
			}
		}
		fresh.parent = parent
		// Every ancestor's totals include this subtree, so shift them all by
		// the same delta.
		dSize := fresh.Size - old.Size
		dApparent := fresh.Apparent - old.Apparent
		dFiles := fresh.Files - old.Files
		dDirs := fresh.Dirs - old.Dirs
		for _, a := range chain[:len(chain)-1] {
			a.Size += dSize
			a.Apparent += dApparent
			a.Files += dFiles
			a.Dirs += dDirs
		}
	}
	e.adopt(root)
	e.baseDir = "" // force a fresh staleness baseline for the rescanned dir
	e.stale = false
	e.status = "rescanned " + old.Path
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
	sortChildren(child, e.apparent)
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
	rows, cols := e.rows, e.cols
	if rows < 4 {
		rows = 4
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
	total := cur.bytes(e.apparent)
	kids := e.children()
	body := rows - 3

	sel := e.sel[len(e.sel)-1]
	if sel >= len(kids) {
		sel = len(kids) - 1
	}
	if sel < 0 {
		sel = 0
	}
	e.sel[len(e.sel)-1] = sel

	off := clampOff(e.off[len(e.off)-1], sel, len(kids), body)
	e.off[len(e.off)-1] = off

	var sb strings.Builder
	sb.WriteString("\x1b[H")

	head := headLabel(cur, e.stale, e.apparent)
	if e.scanning && e.sc != nil {
		head += fmt.Sprintf("   ⟳ scanning… %s items", comma(e.sc.scanned.Load()))
	}
	if j := e.job; j != nil {
		head += fmt.Sprintf("   ⟳ rescanning %s… %s items",
			safe(filepath.Base(j.old.Path)), comma(e.sc.scanned.Load()-j.base))
	}
	if e.hideDot {
		head += "   · dotfiles hidden"
		if h := e.hiddenSize(); h > 0 {
			head += fmt.Sprintf(" (%s, %.0f%%)", humanize(h), float64(h)/float64(total)*100)
		}
	}
	sb.WriteString("\x1b[7m" + fit(head, cols) + "\x1b[0m\r\n")

	// The path sits just above the listing it labels. It is bold rather than
	// reverse video: reverse would cancel the terminal's own selection highlight,
	// making the path impossible to see while you drag-select it to copy.
	pathLine := fitLeft(" "+safe(displayPath(cur.Path, e.wd, true)), cols)
	if useColor {
		pathLine = "\x1b[1m" + pathLine + "\x1b[0m"
	}
	sb.WriteString(pathLine + "\r\n")

	for i := 0; i < body; i++ {
		idx := off + i
		sb.WriteString("\x1b[K")
		if idx >= len(kids) {
			if len(kids) == 0 && i == 0 {
				msg := " (empty)"
				switch {
				case e.scanning:
					msg = " (scanning…)"
				case e.hideDot && len(cur.Children) > 0:
					msg = " (only dotfiles — press . to show them)"
				}
				sb.WriteString(paint(msg, "2"))
			}
			sb.WriteString("\r\n")
			continue
		}
		spans := rowSpans(kids[idx], total, e.marked[kids[idx].Path], e.apparent)
		if idx == sel {
			// The highlighted row sits under a row-wide reverse-video (or flash)
			// highlight, so its spans carry no color resets that would cut the
			// highlight short. The exception is the meter fill, which spanSelected
			// keeps cyan instead of letting it invert to the terminal background.
			style, rowStyle := "\x1b[7m", spanPlain // reverse video for the cursor
			if e.flash {
				style = "\x1b[30;42m" // black on green: copy confirmation
				if !useColor {
					style = "" // still blink: drop the highlight for the flash frame
				}
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
		foot = " " + safe(e.status) // statuses quote paths and names
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
		"  o                 open in default app",
		"  f                 open folder in file manager",
		"  r                 rescan from disk",
		"  .                 show/hide dotfiles",
		"  a                 size on disk / apparent size",
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
// m/d/x unmark, a toggles the path format, Esc/M close, q/Ctrl-C/Ctrl-D quit.
func (e *explorer) handleMarks(b []byte) bool {
	for i := 0; i < len(b); i++ {
		c := b[i]
		if c == 27 { // CSI/SS3 arrows, or a lone Esc that closes the panel
			if i+2 < len(b) && (b[i+1] == '[' || b[i+1] == 'O') {
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

// renderMarks draws the marks panel: the staged paths (absolute by default,
// relative to the launch dir with 'a'), with the cursor row highlighted. A mark
// whose path is no longer in the scanned tree is dimmed and tagged as gone.
func (e *explorer) renderMarks(rows, cols int) {
	e.marksSet(e.marksSel) // keep the cursor in range after unmarking
	body := rows - 2
	e.marksOff = clampOff(e.marksOff, e.marksSel, len(e.marks), body)

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
			line := " " + safe(displayPath(e.marks[idx], e.wd, !e.marksRel))
			gone := e.marksGone[e.marks[idx]]
			if gone {
				line += "  (gone)"
			}
			switch {
			case idx == e.marksSel:
				sb.WriteString("\x1b[7m" + fit(line, cols) + "\x1b[0m")
			case gone:
				sb.WriteString(paint(fit(line, cols), "2"))
			default:
				sb.WriteString(fit(line, cols))
			}
		}
		sb.WriteString("\r\n")
	}

	foot := " ↑/↓ move  ↵ go  m unmark  a abs/rel  Esc back  q quit"
	if e.status != "" {
		foot = " " + safe(e.status)
	}
	sb.WriteString("\x1b[K\x1b[7m" + fit(foot, cols) + "\x1b[0m")
	fmt.Fprint(e.tty, sb.String())
}

// headLabel is the second header line: the current directory's totals (both
// sizes when they differ, the active measure first), plus a marker when it has
// changed on disk since scanned. The absolute path is drawn on the line above it.
func headLabel(n *Node, stale, apparent bool) string {
	s := fmt.Sprintf(" %s · %s files · %s dirs",
		totals(n, apparent), comma(n.Files), comma(n.Dirs))
	if n.Err != "" {
		s += "   · unreadable"
	}
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
func rowSpans(n *Node, total int64, marked, apparent bool) []styledSpan {
	size := n.bytes(apparent)
	frac := 0.0
	if total > 0 {
		frac = float64(size) / float64(total)
	}
	name := safe(n.Name)
	nameCode := ""
	if n.IsDir {
		name += "/"
		nameCode = "1;34"
	}
	gutter, gutterCode := "  ", ""
	if marked {
		gutter, gutterCode = "* ", "33" // a yellow star flags a staged entry
	}
	spans := []styledSpan{
		{gutter, gutterCode},
		{fmt.Sprintf("%10s ", humanize(size)), ""},
		{fmt.Sprintf("%4.0f%%", frac*100), "2"},
		{" ", ""},
		{bar(frac, 16), "36"},
		{"  ", ""},
		{name, nameCode},
	}
	if n.Err != "" {
		spans = append(spans, styledSpan{unreadableTag, "31"})
	}
	return spans
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

// clampOff keeps a scroll offset within range: the selected row stays on
// screen and no blank rows sit at the bottom while entries hide above (as a
// stale offset would leave after the window grows or the listing shrinks).
// Shared by the explorer listing and the marks panel.
func clampOff(off, sel, n, body int) int {
	if last := n - body; off > last {
		off = last
	}
	if sel < off {
		off = sel
	}
	if sel >= off+body {
		off = sel - body + 1
	}
	if off < 0 {
		off = 0
	}
	return off
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

// fitLeft is like fit but trims from the left with a leading ellipsis, so the
// end of s — e.g. the current directory at the tail of a long path — stays
// visible. It right-pads with spaces when s is shorter than w.
func fitLeft(s string, w int) string {
	r := []rune(s)
	if len(r) > w {
		if w <= 1 {
			return string(r[len(r)-w:])
		}
		return "…" + string(r[len(r)-(w-1):])
	}
	return s + strings.Repeat(" ", w-len(r))
}

// size queries the terminal dimensions via stty, falling back to 24x80. It
// costs a subprocess, so render reads the cached copy kept by measure.
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
	cmd := exec.Command("stty", args...) // #nosec G204 -- fixed program, no shell; args are fixed flags or the state stty -g printed
	cmd.Stdin = tty
	cmd.Stdout = tty
	cmd.Stderr = tty
	return cmd.Run()
}
