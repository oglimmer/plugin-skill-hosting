package server

import (
	"path"
	"sort"
	"strings"
)

// FileRefEdge is one detected reference from file From → file To, discovered
// by scanning text-file contents for substring mentions of other files'
// paths (or unique basenames). The edge list is the *only* information
// derived from file contents that we surface to the validator; raw contents
// never leave the server. See computeFileRefEdges.
type FileRefEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// computeFileRefEdges scans every text file's content for substring mentions
// of every other file in the same skill and returns the resulting reference
// graph. The matching is intentionally conservative — we'd rather miss an
// edge than fabricate one — because the prompt treats edges as positive
// evidence ("file X is referenced by file Y") that suppresses the "unused
// supporting file" finding.
//
// Matching rules:
//   - Full relative path (e.g. "scripts/helper.sh") is matched as a plain
//     substring. This catches the common `source scripts/helper.sh`,
//     `cat references/x.md`, Markdown links, etc.
//   - Basename (e.g. "helper.sh") is matched with ASCII word boundaries, but
//     ONLY when the basename is unique within the skill — otherwise a
//     mention of "main.sh" can't be attributed to a specific file and we
//     would risk a wrong edge.
//   - Binary files do not contribute as sources (we don't scan their bytes)
//     but they can appear as targets of edges from text files.
//   - A file never references itself.
func computeFileRefEdges(files []SkillFile) []FileRefEdge {
	if len(files) < 2 {
		return nil
	}

	// Build basename → number of files sharing it. Only basenames with
	// count == 1 are safe to match by basename alone.
	basenameCount := map[string]int{}
	for _, f := range files {
		basenameCount[path.Base(f.Path)]++
	}

	edges := []FileRefEdge{}
	for _, src := range files {
		if src.IsBinary {
			continue
		}
		content := src.Content
		if content == "" {
			continue
		}
		for _, tgt := range files {
			if tgt.Path == src.Path {
				continue
			}
			if !mentions(content, tgt.Path, basenameCount) {
				continue
			}
			edges = append(edges, FileRefEdge{From: src.Path, To: tgt.Path})
		}
	}

	// Stable order so prompt + tests are deterministic.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].From != edges[j].From {
			return edges[i].From < edges[j].From
		}
		return edges[i].To < edges[j].To
	})
	return edges
}

// mentions reports whether content references targetPath, either by the full
// relative path or — if its basename is unique within the skill — by a
// word-boundary basename match.
func mentions(content, targetPath string, basenameCount map[string]int) bool {
	if strings.Contains(content, targetPath) {
		return true
	}
	base := path.Base(targetPath)
	if base == targetPath {
		// Top-level files would otherwise be matched twice; the first
		// Contains already covered them.
		return false
	}
	if basenameCount[base] != 1 {
		return false
	}
	return containsWord(content, base)
}

// containsWord reports whether word appears in s with ASCII word boundaries
// on both sides — i.e. the characters immediately before and after the match
// are not [A-Za-z0-9_]. Standalone occurrences (start/end of string) count.
// Filenames typically contain "." which already breaks identifier-style word
// chars, so this catches `./helper.sh`, `"helper.sh"`, `(helper.sh)` etc.
func containsWord(s, word string) bool {
	if word == "" {
		return false
	}
	start := 0
	for {
		idx := strings.Index(s[start:], word)
		if idx < 0 {
			return false
		}
		abs := start + idx
		if !isWordChar(byteAt(s, abs-1)) && !isWordChar(byteAt(s, abs+len(word))) {
			return true
		}
		start = abs + 1
	}
}

func byteAt(s string, i int) byte {
	if i < 0 || i >= len(s) {
		return 0
	}
	return s[i]
}

func isWordChar(b byte) bool {
	switch {
	case b >= 'A' && b <= 'Z':
		return true
	case b >= 'a' && b <= 'z':
		return true
	case b >= '0' && b <= '9':
		return true
	case b == '_':
		return true
	}
	return false
}
