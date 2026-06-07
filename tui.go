//go:build unix

package main

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
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
}

// runTUI drives the interactive explorer on the controlling terminal. It always
// restores the terminal, even on Ctrl-C.
func runTUI(root *Node, path string, sc *scanner) error {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer tty.Close()

	orig, err := sttyState(tty)
	if err != nil {
		return err
	}
	// cbreak: unbuffered, no echo, but signals stay on so Ctrl-C still fires.
	if err := stty(tty, "-echo", "-icanon", "min", "1", "time", "0"); err != nil {
		return err
	}

	restore := func() {
		stty(tty, orig)
		fmt.Fprint(tty, "\x1b[?25h\x1b[?1049l") // show cursor, leave alt screen
	}
	defer restore()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		restore()
		os.Exit(130)
	}()

	fmt.Fprint(tty, "\x1b[?1049h\x1b[?25l") // enter alt screen, hide cursor

	sortChildren(root)
	e := &explorer{stack: []*Node{root}, sel: []int{0}, off: []int{0}, tty: tty, sc: sc}

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
				return nil // terminal closed
			}
			if e.handle(k) {
				return nil
			}
			e.ensureBaseline()
			e.render()
		case <-ticker.C:
			if e.pollStale() {
				e.render()
			}
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
		case 'q', 3, 4: // q, Ctrl-C, Ctrl-D
			return true
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
		case 'g':
			e.set(0)
		case 'G':
			e.last()
		}
	}
	return false
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
func (e *explorer) last() { e.set(len(e.cur().Children) - 1) }

func (e *explorer) move(d int) { e.set(e.sel[len(e.sel)-1] + d) }

func (e *explorer) set(i int) {
	n := len(e.cur().Children)
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
	cur := e.cur()
	if len(cur.Children) == 0 {
		return
	}
	child := cur.Children[e.sel[len(e.sel)-1]]
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
	cur := e.cur()
	total := cur.Size
	body := rows - 2

	sel := e.sel[len(e.sel)-1]
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

	sb.WriteString("\x1b[7m" + fit(headLabel(cur, e.stale), cols) + "\x1b[0m\r\n")

	for i := 0; i < body; i++ {
		idx := off + i
		sb.WriteString("\x1b[K")
		if idx >= len(cur.Children) {
			sb.WriteString("\r\n")
			continue
		}
		row := rowText(cur.Children[idx], total, cols)
		if idx == sel {
			sb.WriteString("\x1b[7m" + row + "\x1b[0m")
		} else {
			sb.WriteString(row)
		}
		sb.WriteString("\r\n")
	}

	pos := "0/0"
	if len(cur.Children) > 0 {
		pos = fmt.Sprintf("%d/%d", sel+1, len(cur.Children))
	}
	foot := fmt.Sprintf(" ↑/↓ move  →/⏎ open  ←/h/Esc back  r refresh  g/G ends  q quit    %s", pos)
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

// rowText formats one entry padded to the full width so reverse-video selection
// covers the line. It carries no color codes, so truncation is safe.
func rowText(n *Node, total int64, cols int) string {
	frac := 0.0
	if total > 0 {
		frac = float64(n.Size) / float64(total)
	}
	name := n.Name
	if n.IsDir {
		name += "/"
	}
	prefix := fmt.Sprintf("%10s %4.0f%% %s  ", humanize(n.Size), frac*100, bar(frac, 16))
	return fit(prefix+name, cols)
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
