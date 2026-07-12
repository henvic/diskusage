package main

import (
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
