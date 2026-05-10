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
		{"9.9.9", bumpMajor, "10.0.0"},
		{"0.99.99", bumpMinor, "0.100.0"},
	}
	for _, c := range cases {
		got := bumpVersion(c.current, c.kind)
		if got != c.want {
			t.Errorf("bumpVersion(%q, %v) = %q, want %q", c.current, c.kind, got, c.want)
		}
	}
}

func TestBumpVersion_UnknownKindReturnsCurrent(t *testing.T) {
	got := bumpVersion("1.2.3", bumpKind(99))
	if got != "1.2.3" {
		t.Errorf("unknown bumpKind should return current; got %q", got)
	}
}

func TestBumpKindForSizeChange_SymmetricThreshold(t *testing.T) {
	// Shrink by exactly 30% — equal to threshold, so still patch.
	if got := bumpKindForSizeChange(100, 70); got != bumpPatch {
		t.Errorf("100→70 should be patch (= 30%% threshold), got %v", got)
	}
	// Shrink by 31% — minor.
	if got := bumpKindForSizeChange(100, 69); got != bumpMinor {
		t.Errorf("100→69 should be minor (>30%%), got %v", got)
	}
}
