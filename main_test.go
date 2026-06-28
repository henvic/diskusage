package main

import "testing"

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
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveMode(c.o, c.tty); got != c.want {
				t.Errorf("resolveMode(%+v, tty=%v) = %v, want %v", c.o, c.tty, got, c.want)
			}
		})
	}
}
