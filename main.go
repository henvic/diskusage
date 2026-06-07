// Command diskusage reports where disk space goes under a path so you can find
// what to clean up. It scans a directory tree concurrently and shows the
// largest consumers as a breakdown, a tree, a list of big files, JSON, or an
// interactive explorer.
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// options holds everything parsed from the command line.
type options struct {
	interactive bool
	tree        bool
	files       bool
	jsonOut     bool
	apparent    bool
	follow      bool
	oneFS       bool
	noColor     bool

	depth    int
	limit    int
	min      int64
	excludes []string
}

// multiFlag collects a repeatable string flag (used for --exclude).
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "diskusage:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		o      options
		minStr string
		excl   multiFlag
	)

	flag.BoolVar(&o.interactive, "i", false, "explore interactively (TUI)")
	flag.BoolVar(&o.interactive, "interactive", false, "explore interactively (TUI)")
	flag.BoolVar(&o.tree, "tree", false, "print a size-sorted tree")
	flag.BoolVar(&o.files, "files", false, "list the largest files in the subtree")
	flag.BoolVar(&o.jsonOut, "json", false, "emit the full tree as JSON")
	flag.BoolVar(&o.apparent, "apparent", false, "measure apparent size instead of disk blocks")
	flag.BoolVar(&o.follow, "L", false, "follow symbolic links")
	flag.BoolVar(&o.oneFS, "x", false, "stay on a single filesystem")
	flag.BoolVar(&o.noColor, "no-color", false, "disable colored output")
	flag.IntVar(&o.depth, "depth", 2, "max depth for --tree (0 = unlimited)")
	flag.IntVar(&o.limit, "limit", 0, "max entries to show (0 = no limit)")
	flag.StringVar(&minStr, "min", "", "hide entries smaller than this (e.g. 10M, 1.5G)")
	flag.Var(&excl, "exclude", "skip paths/names matching glob (repeatable)")
	flag.Usage = usage
	flag.Parse()

	min, err := parseSize(minStr)
	if err != nil {
		return err
	}
	o.min = min
	o.excludes = excl

	path := flag.Arg(0)
	if path == "" {
		path = "."
	}
	path = filepath.Clean(path)

	useColor = isTTY(os.Stdout) && !o.noColor && os.Getenv("NO_COLOR") == ""

	s := &scanner{
		apparent: o.apparent,
		follow:   o.follow,
		oneFS:    o.oneFS,
		excludes: o.excludes,
	}

	var root *Node
	withProgress(s, func() {
		root, err = scan(path, s)
	})
	if err != nil {
		return err
	}

	switch {
	case o.jsonOut:
		return printJSON(root)
	case o.interactive:
		if !isTTY(os.Stdout) {
			fmt.Fprintln(os.Stderr, "diskusage: stdout is not a terminal; printing summary instead")
			printSummary(root, path, o)
			return nil
		}
		if err := runTUI(root, path, s); err != nil {
			fmt.Fprintln(os.Stderr, "diskusage: interactive mode unavailable:", err)
			printSummary(root, path, o)
		}
		return nil
	case o.files:
		printFiles(root, path, o)
	case o.tree:
		printTree(root, path, o)
	default:
		printSummary(root, path, o)
	}
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `diskusage — find where disk space goes under a path

Usage:
  diskusage [flags] [path]

With no path it scans the current directory. Size is measured in allocated disk
blocks (matching du); pass --apparent for logical file sizes. The default output
is a breakdown of the largest items directly under the path.

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, `
Interactive keys (-i):
  ↑/↓ or j/k  move      →/Enter or l  open dir    ←/h/Esc  go up (back)
  r  rescan from disk   g/G  first/last           q or Ctrl-C  quit

Examples:
  diskusage                       breakdown of the current directory
  diskusage ~/Downloads           breakdown of a specific path
  diskusage -i /                  explore the whole disk interactively
  diskusage --tree --depth 3 .    a three-level size tree
  diskusage --files --limit 20 ~  the 20 biggest files under home
  diskusage --min 100M ~          only entries of at least 100 MiB
`)
}
