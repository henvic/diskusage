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
	"runtime/debug"
	"strings"
)

// options holds everything parsed from the command line.
type options struct {
	printOut bool
	tree     bool
	files    bool
	jsonOut  bool
	apparent bool
	follow   bool
	oneFS    bool
	noColor  bool

	depth    int
	limit    int
	min      int64
	excludes []string
}

// multiFlag collects a repeatable string flag (used for --exclude).
type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }

// outputMode is the view diskusage produces for a run.
type outputMode int

const (
	modeInteractive outputMode = iota // the interactive explorer (TUI)
	modeSummary                       // largest items directly under the path
	modeTree                          // size-sorted tree
	modeFiles                         // largest individual files
	modeJSON                          // the full tree as JSON
)

func (m outputMode) String() string {
	switch m {
	case modeInteractive:
		return "interactive"
	case modeSummary:
		return "summary"
	case modeTree:
		return "tree"
	case modeFiles:
		return "files"
	case modeJSON:
		return "json"
	default:
		return "unknown"
	}
}

// resolveMode picks the output mode from the parsed flags and whether stdout is a
// terminal. With no format flag it defaults to the interactive explorer on a
// terminal and the printed summary otherwise, so piped or redirected output stays
// non-interactive.
func resolveMode(o options, stdoutTTY bool) outputMode {
	switch {
	case o.jsonOut:
		return modeJSON
	case o.tree:
		return modeTree
	case o.files:
		return modeFiles
	case o.printOut:
		return modeSummary
	default:
		if stdoutTTY {
			return modeInteractive
		}
		return modeSummary
	}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "diskusage:", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		o           options
		minStr      string
		excl        multiFlag
		showVersion bool
	)

	flag.BoolVar(&o.printOut, "p", false, "print output instead of the interactive explorer")
	flag.BoolVar(&o.printOut, "print", false, "print output instead of the interactive explorer")
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
	flag.BoolVar(&showVersion, "version", false, "print the version and exit")
	flag.Usage = usage
	flag.Parse()

	if showVersion {
		v := "unknown"
		if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
			v = bi.Main.Version
		}
		fmt.Println("diskusage", v)
		return nil
	}

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
	mode := resolveMode(o, isTTY(os.Stdout))

	if mode == modeInteractive {
		// The explorer opens immediately and browses snapshots of the tree
		// while the scan fills it in the background.
		root, done, err := scanStart(path, s)
		if err != nil {
			return err
		}
		marks, err := runTUI(root, path, s, done)
		if err != nil {
			// On non-Unix the runTUI stub returns an error, and /dev/tty can
			// fail to open; either way fall back to the printed summary —
			// silently, since interactive mode is only ever the default, never
			// explicitly requested. The summary needs the finished tree.
			withProgress(s, func() { <-done })
			printSummary(root, path, o)
			return nil
		}
		// Entries staged with 'm' print as absolute paths after the explorer exits.
		for _, m := range marks {
			fmt.Println(m)
		}
		return nil
	}

	var root *Node
	withProgress(s, func() {
		root, err = scan(path, s)
	})
	if err != nil {
		return err
	}

	switch mode {
	case modeJSON:
		return printJSON(root)
	case modeTree:
		printTree(root, path, o)
	case modeFiles:
		printFiles(root, path, o)
	default: // modeSummary
		printSummary(root, path, o)
	}
	return nil
}

func usage() {
	fmt.Fprint(os.Stderr, `diskusage — find where disk space goes under a path

Usage:
  diskusage [flags] [path]

With no path it scans the current directory. Size is measured in allocated disk
blocks (matching du); pass --apparent for logical file sizes. On a terminal it
opens an interactive explorer by default; with --print or a format flag, or when
output is piped, it prints instead.

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, `
Interactive keys:
  ↑/↓ or j/k  move           →/Enter or l  open dir   ←/h/Esc  go up (back)
  c / C  copy absolute / relative path     f  open folder in Finder
  r  rescan from disk        g/G  first/last           ?  show keys
  .  show/hide dotfiles      m  mark / unmark           M  marks panel
  q or Ctrl-C  quit
A ● in the header marks the current directory as changed on disk since scanned.
Entries you mark with m are printed as absolute paths when you quit.

Examples:
  diskusage                       explore the current directory
  diskusage ~/Downloads           explore a specific path
  diskusage -p                    print the summary instead of exploring
  diskusage --tree --depth 3 .    a three-level size tree
  diskusage --files --limit 20 ~  the 20 biggest files under home
  diskusage --min 100M ~          only entries of at least 100 MiB
`)
}
