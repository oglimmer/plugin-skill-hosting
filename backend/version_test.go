package main

import "testing"

func TestParseSemver(t *testing.T) {
	cases := []struct {
		in            string
		mj, mn, pt    int
	}{
		{"1.2.3", 1, 2, 3},
		{"  0.0.0  ", 0, 0, 0},
		{"10.20.30", 10, 20, 30},
		{"1.2", 0, 0, 0},
		{"a.b.c", 0, 0, 0},
		{"", 0, 0, 0},
		{"1.2.3.4", 0, 0, 0},
	}
	for _, c := range cases {
		mj, mn, pt := parseSemver(c.in)
		if mj != c.mj || mn != c.mn || pt != c.pt {
			t.Errorf("parseSemver(%q) = %d.%d.%d, want %d.%d.%d", c.in, mj, mn, pt, c.mj, c.mn, c.pt)
		}
	}
}

func TestBumpKindForSizeChange(t *testing.T) {
	cases := []struct {
		old, new int
		want     bumpKind
	}{
		{0, 0, bumpPatch},
		{0, 1, bumpMinor},
		{100, 100, bumpPatch},
		{100, 110, bumpPatch},
		{100, 130, bumpPatch}, // exactly 30% — not >30%
		{100, 131, bumpMinor},
		{100, 50, bumpMinor},
		{1000, 999, bumpPatch},
	}
	for _, c := range cases {
		got := bumpKindForSizeChange(c.old, c.new)
		if got != c.want {
			t.Errorf("bumpKindForSizeChange(%d, %d) = %v, want %v", c.old, c.new, got, c.want)
		}
	}
}

func TestBumpVersion(t *testing.T) {
	cases := []struct {
		current string
		kind    bumpKind
		want    string
	}{
		{"1.2.3", bumpMajor, "2.0.0"},
		{"1.2.3", bumpMinor, "1.3.0"},
		{"1.2.3", bumpPatch, "1.2.4"},
		{"0.0.0", bumpPatch, "0.0.1"},
		{"garbage", bumpMinor, "0.1.0"}, // parseSemver returns zeros
	}
	for _, c := range cases {
		got := bumpVersion(c.current, c.kind)
		if got != c.want {
			t.Errorf("bumpVersion(%q, %v) = %q, want %q", c.current, c.kind, got, c.want)
		}
	}
}
