package main

import (
	"fmt"
	"strconv"
	"strings"
)

type bumpKind int

const (
	bumpMajor bumpKind = iota
	bumpMinor
	bumpPatch
)

func parseSemver(v string) (int, int, int) {
	parts := strings.SplitN(strings.TrimSpace(v), ".", 3)
	if len(parts) != 3 {
		return 0, 0, 0
	}
	mj, e1 := strconv.Atoi(parts[0])
	mn, e2 := strconv.Atoi(parts[1])
	pt, e3 := strconv.Atoi(parts[2])
	if e1 != nil || e2 != nil || e3 != nil {
		return 0, 0, 0
	}
	return mj, mn, pt
}

func bumpVersion(current string, kind bumpKind) string {
	mj, mn, pt := parseSemver(current)
	switch kind {
	case bumpMajor:
		return fmt.Sprintf("%d.0.0", mj+1)
	case bumpMinor:
		return fmt.Sprintf("%d.%d.0", mj, mn+1)
	case bumpPatch:
		return fmt.Sprintf("%d.%d.%d", mj, mn, pt+1)
	}
	return current
}
