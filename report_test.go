package main

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

func TestHumanize(t *testing.T) {
	cases := map[int64]string{
		0:        "0 B",
		512:      "512 B",
		1023:     "1023 B",
		1024:     "1.0 KiB",
		1536:     "1.5 KiB",
		1048575:  "1.0 MiB", // would read "1024.0 KiB" without unit promotion
		10 << 20: "10.0 MiB",
		1 << 30:  "1.0 GiB",
		1 << 40:  "1.0 TiB",
	}
	for in, want := range cases {
		if got := humanize(in); got != want {
			t.Errorf("humanize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestComma(t *testing.T) {
	cases := map[int64]string{
		0:       "0",
		100:     "100",
		1000:    "1,000",
		999999:  "999,999",
		1234567: "1,234,567",
		-1234:   "-1,234",
	}
	for in, want := range cases {
		if got := comma(in); got != want {
			t.Errorf("comma(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestBar(t *testing.T) {
	cases := []struct {
		frac  float64
		width int
		want  string
	}{
		{1, 4, "████"},
		{0, 4, "    "},
		{0.5, 4, "██  "},
		{2, 4, "████"},        // clamped high
		{-1, 4, "    "},       // clamped low
		{1.0 / 32, 4, "▏   "}, // one eighth of one cell still shows a sliver
		{0.5, 0, ""},          // no room at all
	}
	for _, c := range cases {
		if got := bar(c.frac, c.width); got != c.want {
			t.Errorf("bar(%v, %d) = %q, want %q", c.frac, c.width, got, c.want)
		}
		if n := len([]rune(bar(c.frac, c.width))); n != c.width && c.width > 0 {
			t.Errorf("bar(%v, %d) is %d runes wide", c.frac, c.width, n)
		}
	}
	if got := bar(0.98, 8); strings.Count(got, "█") != 7 || !strings.Contains(got, "▉") {
		t.Errorf("bar(0.98, 8) = %q, want 7 full blocks and a partial", got)
	}
}

// TestWithJSONProgress checks that the progress feed writes valid,
// newline-delimited JSON regardless of whether stderr is a tty (unlike
// withProgress, which only writes to a terminal), and that the final line is
// flagged done with the final scanned count.
func TestWithJSONProgress(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	defer func() { os.Stderr = orig }()

	s := &scanner{}
	s.scanned.Store(41)
	withJSONProgress(s, func() {
		s.scanned.Store(42)
	})
	w.Close()

	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	var lines []map[string]any
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		var line map[string]any
		if err := json.Unmarshal(sc.Bytes(), &line); err != nil {
			t.Fatalf("invalid JSON line %q: %v", sc.Text(), err)
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		t.Fatal("withJSONProgress wrote no lines")
	}
	last := lines[len(lines)-1]
	if last["done"] != true {
		t.Errorf("last line = %v, want done:true", last)
	}
	if last["scanned"].(float64) != 42 {
		t.Errorf("last line scanned = %v, want 42", last["scanned"])
	}
}

// TestSafe checks that names are made harmless for a terminal: control bytes
// and invalid UTF-8 become visible escapes, other text is untouched.
func TestSafe(t *testing.T) {
	cases := map[string]string{
		"plain.txt":      "plain.txt",
		"日本語 ファイル.txt":   "日本語 ファイル.txt",
		"a\x1b[31mRED":   `a\x1b[31mRED`,
		"line1\nline2":   `line1\nline2`,
		"tab\there\r":    `tab\there\r`,
		"del\x7f":        `del\x7f`,
		"c1\u0085next":   `c1\u0085next`,
		"bad\xffutf8":    `bad\xffutf8`,
		"back\\slash":    `back\slash`,
		"replacement�ok": "replacement�ok",
	}
	for in, want := range cases {
		if got := safe(in); got != want {
			t.Errorf("safe(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestLabelEscapesName checks the printed reports never emit a raw control
// byte from a file name.
func TestLabelEscapesName(t *testing.T) {
	got := label(&Node{Name: "evil\x1b]0;pwned\x07\nname"})
	if strings.ContainsAny(got, "\x1b\x07\n") {
		t.Errorf("label leaked a control byte: %q", got)
	}
}
