package main

import (
	"flag"
	"io"
	"slices"
	"testing"
)

// TestResolveMode locks the mode-selection precedence: a format flag always
// prints, --print prints the summary, and with no format flag the explorer is the
// default on a terminal but the summary off one (so piped output stays printable).
func TestResolveMode(t *testing.T) {
	cases := []struct {
		name string
		o    options
		tty  bool
		want outputMode
	}{
		{"default on a terminal is interactive", options{}, true, modeInteractive},
		{"default off a terminal prints the summary", options{}, false, modeSummary},
		{"--print prints the summary on a terminal", options{printOut: true}, true, modeSummary},
		{"--print prints off a terminal too", options{printOut: true}, false, modeSummary},
		{"--tree prints on a terminal", options{tree: true}, true, modeTree},
		{"--tree prints off a terminal", options{tree: true}, false, modeTree},
		{"--files prints", options{files: true}, true, modeFiles},
		{"--json prints", options{jsonOut: true}, true, modeJSON},
		{"a format flag wins over --print", options{tree: true, printOut: true}, true, modeTree},
		{"--json has the highest precedence", options{jsonOut: true, tree: true}, true, modeJSON},
		{"-i opens the explorer off a terminal", options{interactive: true}, false, modeInteractive},
		{"-i on a terminal is the explorer too", options{interactive: true}, true, modeInteractive},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := resolveMode(c.o, c.tty)
			if err != nil {
				t.Fatalf("resolveMode(%+v, tty=%v) error: %v", c.o, c.tty, err)
			}
			if got != c.want {
				t.Errorf("resolveMode(%+v, tty=%v) = %v, want %v", c.o, c.tty, got, c.want)
			}
		})
	}

	// -i with a print flag is contradictory, so it fails instead of picking one.
	for _, o := range []options{
		{interactive: true, printOut: true},
		{interactive: true, tree: true},
		{interactive: true, files: true},
		{interactive: true, jsonOut: true},
	} {
		if _, err := resolveMode(o, false); err == nil {
			t.Errorf("resolveMode(%+v) should reject -i with a print flag", o)
		}
	}
}

// TestParseInterspersed checks that flags are honored after the path, that
// "--" still ends flag parsing, and that positional arguments keep their order.
func TestParseInterspersed(t *testing.T) {
	cases := []struct {
		args      []string
		wantPos   []string
		wantTree  bool
		wantDepth int
	}{
		{[]string{"~/Downloads", "--tree"}, []string{"~/Downloads"}, true, 2},
		{[]string{"--tree", "~/Downloads"}, []string{"~/Downloads"}, true, 2},
		{[]string{"a", "--depth", "3", "b", "--tree"}, []string{"a", "b"}, true, 3},
		{[]string{"--depth=4", "a"}, []string{"a"}, false, 4},
		{[]string{"--", "-weird", "--tree"}, []string{"-weird", "--tree"}, false, 2},
		{[]string{"a", "--", "--tree"}, []string{"a", "--tree"}, false, 2},
		{nil, nil, false, 2},
	}
	for _, c := range cases {
		fs := flag.NewFlagSet("test", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		tree := fs.Bool("tree", false, "")
		depth := fs.Int("depth", 2, "")
		pos, err := parseInterspersed(fs, c.args)
		if err != nil {
			t.Errorf("parseInterspersed(%q) error: %v", c.args, err)
			continue
		}
		if !slices.Equal(pos, c.wantPos) || *tree != c.wantTree || *depth != c.wantDepth {
			t.Errorf("parseInterspersed(%q) = %q tree=%v depth=%d, want %q tree=%v depth=%d",
				c.args, pos, *tree, *depth, c.wantPos, c.wantTree, c.wantDepth)
		}
	}

	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	if _, err := parseInterspersed(fs, []string{"a", "--nope"}); err == nil {
		t.Error("an unknown flag after the path should still be an error")
	}
}
