// Command diskusage reports where disk space goes under a path so you can find
// what to clean up. It scans a directory tree concurrently and shows the
// largest consumers as a breakdown, a tree, a list of big files, JSON, or an
// interactive explorer.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
)

// options holds everything parsed from the command line.
type options struct {
	interactive bool // -i: open the explorer even when stdout is not a terminal

	printOut bool
	tree     bool
	files    bool
	jsonOut  bool
	apparent bool
	follow   bool
	oneFS    bool
	noColor  bool
	progress bool

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
// non-interactive. -i forces the explorer regardless of stdout (it draws on the
// controlling terminal, so stdout is free to carry the marks into a pipeline);
// combining it with a print flag is an error rather than a silent choice.
func resolveMode(o options, stdoutTTY bool) (outputMode, error) {
	if o.interactive {
		if o.jsonOut || o.tree || o.files || o.printOut {
			return 0, errors.New("-i cannot be combined with --print, --tree, --files, or --json")
		}
		return modeInteractive, nil
	}
	switch {
	case o.jsonOut:
		return modeJSON, nil
	case o.tree:
		return modeTree, nil
	case o.files:
		return modeFiles, nil
	case o.printOut:
		return modeSummary, nil
	default:
		if stdoutTTY {
			return modeInteractive, nil
		}
		return modeSummary, nil
	}
}

// parseInterspersed parses args with fs like fs.Parse, except that flags may
// follow positional arguments: the standard flag package stops at the first
// non-flag, which silently turned "diskusage ~/Downloads --tree" into a path
// plus an ignored "--tree". Parsing resumes after each positional argument, and
// a "--" still ends flag parsing so a path that starts with "-" can be passed.
// It returns the positional arguments in order.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if len(rest) == 0 {
			return pos, nil
		}
		// fs.Parse stopped either at "--" (which it consumed) or at the first
		// non-flag (which it left as rest[0]). After "--" everything is
		// positional.
		if used := len(args) - len(rest); used > 0 && args[used-1] == "--" {
			return append(pos, rest...), nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
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

	flag.BoolVar(&o.interactive, "i", false, "open the interactive explorer even when stdout is not a terminal")
	flag.BoolVar(&o.interactive, "interactive", false, "open the interactive explorer even when stdout is not a terminal")
	flag.BoolVar(&o.printOut, "p", false, "print output instead of the interactive explorer")
	flag.BoolVar(&o.printOut, "print", false, "print output instead of the interactive explorer")
	flag.BoolVar(&o.tree, "tree", false, "print a size-sorted tree")
	flag.BoolVar(&o.files, "files", false, "list the largest files in the subtree")
	flag.BoolVar(&o.jsonOut, "json", false, "emit the full tree as JSON")
	flag.BoolVar(&o.apparent, "apparent", false, "measure apparent size instead of disk blocks")
	flag.BoolVar(&o.follow, "L", false, "follow symbolic links")
	flag.BoolVar(&o.oneFS, "x", false, "stay on a single filesystem")
	flag.BoolVar(&o.noColor, "no-color", false, "disable colored output")
	flag.BoolVar(&o.progress, "progress-json", false, "emit newline-delimited JSON scan progress to stderr, regardless of tty (for tooling, e.g. a GUI)")
	flag.IntVar(&o.depth, "depth", 2, "max depth for --tree (0 = unlimited)")
	flag.IntVar(&o.limit, "limit", 0, "max entries to show (0 = no limit)")
	flag.StringVar(&minStr, "min", "", "hide entries smaller than this (e.g. 10M, 1.5G)")
	flag.Var(&excl, "exclude", "skip paths/names matching glob (repeatable)")
	flag.BoolVar(&showVersion, "version", false, "print the version and exit")
	flag.Usage = usage
	// flag.CommandLine exits on a bad flag (status 2) and on -h, as before.
	args, err := parseInterspersed(flag.CommandLine, os.Args[1:])
	if err != nil {
		return err
	}
	if len(args) > 1 {
		return fmt.Errorf("expected at most one path, got %d: %s", len(args), strings.Join(args, " "))
	}

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

	path := "."
	if len(args) == 1 && args[0] != "" {
		path = args[0]
	}
	path = filepath.Clean(path)

	mode, err := resolveMode(o, isTTY(os.Stdout))
	if err != nil {
		return err
	}

	// Color follows the terminal the output lands on. A forced explorer draws on
	// the controlling terminal, not on stdout, so stdout being a pipe says
	// nothing about it.
	colorOK := !o.noColor && os.Getenv("NO_COLOR") == ""
	useColor = colorOK && (isTTY(os.Stdout) || o.interactive)

	s := &scanner{
		apparent: o.apparent,
		follow:   o.follow,
		oneFS:    o.oneFS,
		excludes: o.excludes,
	}
	if mode == modeInteractive {
		// The explorer opens immediately and browses snapshots of the tree
		// while the scan fills it in the background.
		root, done, err := scanStart(path, s)
		if err != nil {
			return err
		}
		marks, err := runTUI(root, path, s, done)
		if err != nil {
			if o.interactive {
				return fmt.Errorf("cannot open the interactive explorer: %w", err)
			}
			// On non-Unix the runTUI stub returns an error, and /dev/tty can
			// fail to open; either way fall back to the printed summary,
			// silently, since here the explorer was only the default, not
			// requested with -i. The summary needs the finished tree.
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
	progress := withProgress
	if o.progress {
		progress = withJSONProgress
	}
	progress(s, func() {
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
  diskusage [flags] [path] [flags]

With no path it scans the current directory. Size is measured in allocated disk
blocks (matching du); pass --apparent for logical file sizes. On a terminal it
opens an interactive explorer by default; with --print or a format flag, or when
output is piped, it prints instead. -i opens the explorer even when output is
piped, so marked paths can feed another command. Flags may come before or after
the path; use -- before a path that starts with "-".

Flags:
`)
	flag.PrintDefaults()
	fmt.Fprint(os.Stderr, `
Interactive keys:
  ↑/↓ or j/k  move           →/Enter or l  open dir   ←/h/Esc  go up (back)
  c / C  copy absolute / relative path     f  open folder in file manager
  r  rescan from disk        g/G  first/last           ?  show keys
  .  show/hide dotfiles      m  mark / unmark           M  marks panel
  o  open in default app     q or Ctrl-C  quit
A ● in the header marks the current directory as changed on disk since scanned.
Entries you mark with m are printed as absolute paths when you quit.
Kernel filesystems mounted below the path (/proc, /sys, ...) are skipped.

Examples:
  diskusage                       explore the current directory
  diskusage ~/Downloads           explore a specific path
  diskusage -p                    print the summary instead of exploring
  diskusage -i ~ > marks.txt     explore; paths marked with m land in marks.txt
  diskusage --tree --depth 3 .    a three-level size tree
  diskusage --files --limit 20 ~  the 20 biggest files under home
  diskusage --min 100M ~          only entries of at least 100 MiB
`)
}
