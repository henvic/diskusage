package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// useColor is set once in main from the terminal/NO_COLOR/--no-color state and
// read by paint.
var useColor bool

// paint wraps s in an ANSI SGR code when color is enabled, otherwise returns it
// unchanged.
func paint(s, code string) string {
	if !useColor {
		return s
	}
	return "\x1b[" + code + "m" + s + "\x1b[0m"
}

// isTTY reports whether f is connected to a terminal.
func isTTY(f *os.File) bool {
	fi, err := f.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// safe makes a file name or path harmless to print on a terminal. Unix names
// may hold any byte but '/' and NUL, so a name carrying an escape sequence
// could recolor or rewrite the screen, and one carrying a newline would split
// its row. Control characters become visible escapes (\n, \t, \x1b, \u0085)
// and bytes that are not valid UTF-8 become \xNN; everything else, including
// non-ASCII text, passes through unchanged. It is for display only: JSON, the
// clipboard, and the marks printed on quit keep the raw path.
func safe(s string) string {
	clean := true
	for _, r := range s {
		if r == utf8.RuneError || unicode.IsControl(r) {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			fmt.Fprintf(&b, "\\x%02x", s[i])
		case r == '\n':
			b.WriteString("\\n")
		case r == '\t':
			b.WriteString("\\t")
		case r == '\r':
			b.WriteString("\\r")
		case unicode.IsControl(r) && r < 0x80:
			fmt.Fprintf(&b, "\\x%02x", r)
		case unicode.IsControl(r):
			fmt.Fprintf(&b, "\\u%04x", r)
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// humanize renders a byte count using IEC units (KiB, MiB, ...).
func humanize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < len(units)-1; n /= unit {
		div *= unit
		exp++
	}
	v := float64(b) / float64(div)
	// Just under a boundary, %.1f would round up to "1024.0"; promote the unit
	// instead so it reads "1.0" of the next one.
	if math.Round(v*10)/10 >= unit && exp < len(units)-1 {
		v /= unit
		exp++
	}
	return fmt.Sprintf("%.1f %s", v, units[exp])
}

// comma groups an integer with thousands separators.
func comma(n int64) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.FormatInt(n, 10)
	if len(s) > 3 {
		var b strings.Builder
		pre := len(s) % 3
		if pre > 0 {
			b.WriteString(s[:pre])
			b.WriteByte(',')
		}
		for i := pre; i < len(s); i += 3 {
			b.WriteString(s[i : i+3])
			if i+3 < len(s) {
				b.WriteByte(',')
			}
		}
		s = b.String()
	}
	if neg {
		return "-" + s
	}
	return s
}

// bar draws a width-character meter for frac in [0,1] using Unicode eighth
// blocks, so a fraction smaller than a single cell still shows a thin sliver.
// The unfilled cells are left blank: the eighth blocks are left-aligned and
// narrower than a cell, and against a blank track their bare right side merges
// into the empty space instead of standing out as a gap (as it did against a
// dither).
func bar(frac float64, width int) string {
	if width <= 0 {
		return ""
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	parts := []rune(" ▏▎▍▌▋▊▉█")
	eighths := int(frac*float64(width)*8 + 0.5)
	full := eighths / 8
	rem := eighths % 8
	var b strings.Builder
	used := 0
	for ; used < full && used < width; used++ {
		b.WriteRune('█')
	}
	if rem > 0 && used < width {
		b.WriteRune(parts[rem])
		used++
	}
	for ; used < width; used++ {
		b.WriteRune(' ')
	}
	return b.String()
}

// parseSize turns a human size like "10M" or "1.5GiB" into bytes. An empty
// string is zero.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	i := 0
	for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
		i++
	}
	num, unit := s[:i], strings.ToLower(strings.TrimSpace(s[i:]))
	var mult int64
	switch unit {
	case "", "b":
		mult = 1
	case "k", "kb", "kib":
		mult = 1 << 10
	case "m", "mb", "mib":
		mult = 1 << 20
	case "g", "gb", "gib":
		mult = 1 << 30
	case "t", "tb", "tib":
		mult = 1 << 40
	case "p", "pb", "pib":
		mult = 1 << 50
	default:
		return 0, fmt.Errorf("invalid size unit %q", unit)
	}
	f, err := strconv.ParseFloat(num, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid size %q", s)
	}
	if f >= float64(math.MaxInt64)/float64(mult) {
		return 0, fmt.Errorf("size %q is too large", s)
	}
	return int64(f * float64(mult)), nil
}

// meter is the fixed-width "size  pct  bar" column shared by every report.
func meter(size, total int64) string {
	frac := 0.0
	if total > 0 {
		frac = float64(size) / float64(total)
	}
	return fmt.Sprintf("%10s %s %s",
		humanize(size),
		paint(fmt.Sprintf("%4.0f%%", frac*100), "2"),
		paint(bar(frac, 18), "36"))
}

// unreadableTag flags a directory whose contents could not be read, in both
// the printed reports and the explorer.
const unreadableTag = "  (unreadable)"

// label renders a node's name, marking directories with a trailing slash and
// color, and flagging directories that could not be read.
func label(n *Node) string {
	s := safe(n.Name)
	if n.IsDir {
		s = paint(s+"/", "1;34")
	}
	if n.Err != "" {
		s += paint(unreadableTag, "31")
	}
	return s
}

// withProgress runs fn while showing a lightweight scan counter on stderr (only
// when stderr is a terminal).
func withProgress(s *scanner, fn func()) {
	if !isTTY(os.Stderr) {
		fn()
		return
	}
	done := make(chan struct{})
	idle := make(chan struct{})
	go func() {
		defer close(idle)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, "\r\x1b[Kscanning… %s items", comma(s.scanned.Load()))
			}
		}
	}()
	fn()
	close(done)
	<-idle // a tick in flight could otherwise print after the final clear
	fmt.Fprint(os.Stderr, "\r\x1b[K")
}

// withJSONProgress runs fn while emitting newline-delimited JSON progress
// events to stderr ({"scanned":N}), finishing with {"scanned":N,"done":true}.
// Unlike withProgress it writes regardless of whether stderr is a terminal,
// so a non-interactive caller (e.g. a GUI driving diskusage as a subprocess)
// can show scan progress without a tty of its own.
func withJSONProgress(s *scanner, fn func()) {
	done := make(chan struct{})
	idle := make(chan struct{})
	go func() {
		defer close(idle)
		t := time.NewTicker(100 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				fmt.Fprintf(os.Stderr, `{"scanned":%d}`+"\n", s.scanned.Load())
			}
		}
	}()
	fn()
	close(done)
	<-idle // a tick in flight could otherwise print after the final line
	fmt.Fprintf(os.Stderr, `{"scanned":%d,"done":true}`+"\n", s.scanned.Load())
}

// totals renders a node's size under the chosen measure, followed by the other
// measure when it reads differently: "8.1 GiB on disk · 64.3 GiB apparent".
// Shared by the printed header and the explorer's totals line.
func totals(n *Node, apparent bool) string {
	disk, app := humanize(n.Size), humanize(n.Apparent)
	if disk == app {
		return disk
	}
	if apparent {
		return app + " apparent · " + disk + " on disk"
	}
	return disk + " on disk · " + app + " apparent"
}

// header prints the path line plus a one-line subtree total, noting how many
// directories anywhere in the tree could not be read (their contents are
// missing from the totals).
func header(root *Node, path string, apparent bool) {
	fmt.Println(paint(safe(path), "1"))
	line := fmt.Sprintf("%s in %s files, %s dirs",
		totals(root, apparent), comma(root.Files), comma(root.Dirs))
	unreadable := 0
	walk(root, func(n *Node) {
		if n.Err != "" {
			unreadable++
		}
	})
	if unreadable > 0 {
		line += "  " + paint(fmt.Sprintf("(%d unreadable)", unreadable), "31")
	}
	fmt.Println(line)
}

// printSummary lists the largest items directly under the path. This is the
// default mode and the quickest answer to "where did my space go?".
func printSummary(root *Node, path string, o options) {
	sortChildren(root, o.apparent)
	header(root, path, o.apparent)
	fmt.Println()

	total := root.bytes(o.apparent)
	shown := 0
	for _, c := range root.Children {
		if o.min > 0 && c.bytes(o.apparent) < o.min {
			break
		}
		fmt.Printf("%s  %s\n", meter(c.bytes(o.apparent), total), label(c))
		shown++
		if o.limit > 0 && shown >= o.limit {
			break
		}
	}
	if shown == 0 {
		fmt.Println(paint("(empty)", "2"))
	}
	if isTTY(os.Stdout) {
		fmt.Printf("\n%s\n", paint("tip: --tree for a tree · --files for the biggest files · --json to script", "2"))
	}
}

// printTree prints a size-sorted tree with box-drawing connectors, pruned by
// --depth, --limit (per directory) and --min.
func printTree(root *Node, path string, o options) {
	sortChildren(root, o.apparent)
	header(root, path, o.apparent)
	total := root.bytes(o.apparent)
	fmt.Printf("%s  %s\n", meter(total, total), paint(safe(filepath.Base(path))+"/", "1;34"))
	treeKids(root, total, "", o, 1)
}

func treeKids(n *Node, total int64, prefix string, o options, depth int) {
	if o.depth > 0 && depth > o.depth {
		return
	}
	kids := shownKids(n, o)
	for i, c := range kids {
		last := i == len(kids)-1
		branch, next := "├─ ", prefix+"│  "
		if last {
			branch, next = "└─ ", prefix+"   "
		}
		fmt.Printf("%s  %s%s\n", meter(c.bytes(o.apparent), total), prefix+branch, label(c))
		if c.IsDir {
			treeKids(c, total, next, o, depth+1)
		}
	}
}

// shownKids returns a node's children in display order after applying the --min
// and --limit cutoffs.
func shownKids(n *Node, o options) []*Node {
	sortChildren(n, o.apparent)
	var out []*Node
	for _, c := range n.Children {
		if o.min > 0 && c.bytes(o.apparent) < o.min {
			break
		}
		out = append(out, c)
		if o.limit > 0 && len(out) >= o.limit {
			break
		}
	}
	return out
}

// printFiles lists the largest individual files anywhere in the subtree.
func printFiles(root *Node, path string, o options) {
	var files []*Node
	walk(root, func(n *Node) {
		if !n.IsDir {
			files = append(files, n)
		}
	})
	sort.Slice(files, func(i, j int) bool {
		if a, b := files[i].bytes(o.apparent), files[j].bytes(o.apparent); a != b {
			return a > b
		}
		return files[i].Path < files[j].Path
	})

	header(root, path, o.apparent)
	fmt.Println()

	limit := o.limit
	if limit <= 0 {
		limit = 30
	}
	total := root.bytes(o.apparent)
	shown := 0
	for _, f := range files {
		if o.min > 0 && f.bytes(o.apparent) < o.min {
			break
		}
		rel, err := filepath.Rel(path, f.Path)
		if err != nil {
			rel = f.Path
		}
		fmt.Printf("%s  %s\n", meter(f.bytes(o.apparent), total), safe(rel))
		shown++
		if shown >= limit {
			break
		}
	}
	if shown == 0 {
		fmt.Println(paint("(no files)", "2"))
	}
}

// printJSON writes the whole tree as indented JSON for further processing.
func printJSON(root *Node) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(root)
}
