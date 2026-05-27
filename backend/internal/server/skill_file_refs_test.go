package server

import "testing"

func TestComputeFileRefEdges_FullPathMatch(t *testing.T) {
	files := []SkillFile{
		{Path: "scripts/main.sh", Content: "source scripts/helper.sh\n"},
		{Path: "scripts/helper.sh", Content: "echo helper"},
	}
	edges := computeFileRefEdges(files)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d: %+v", len(edges), edges)
	}
	if edges[0].From != "scripts/main.sh" || edges[0].To != "scripts/helper.sh" {
		t.Errorf("unexpected edge: %+v", edges[0])
	}
}

func TestComputeFileRefEdges_UniqueBasenameMatch(t *testing.T) {
	// Body references the helper by basename only ("./helper.sh"); since
	// "helper.sh" is unique within the skill, the word-boundary match
	// should still produce an edge.
	files := []SkillFile{
		{Path: "scripts/main.sh", Content: "source ./helper.sh\n"},
		{Path: "scripts/helper.sh", Content: ""},
	}
	edges := computeFileRefEdges(files)
	if len(edges) != 1 || edges[0].From != "scripts/main.sh" || edges[0].To != "scripts/helper.sh" {
		t.Fatalf("expected main.sh -> helper.sh, got %+v", edges)
	}
}

func TestComputeFileRefEdges_AmbiguousBasenameSkipped(t *testing.T) {
	// Two files share basename "run.sh"; a mention of "run.sh" alone is
	// ambiguous so no edge should be emitted by basename. Full-path
	// mentions still count.
	files := []SkillFile{
		{Path: "scripts/a/run.sh", Content: "echo a"},
		{Path: "scripts/b/run.sh", Content: "echo b"},
		{Path: "scripts/main.sh", Content: "see run.sh for details\nthen scripts/b/run.sh\n"},
	}
	edges := computeFileRefEdges(files)
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (only the full-path one), got %d: %+v", len(edges), edges)
	}
	if edges[0].From != "scripts/main.sh" || edges[0].To != "scripts/b/run.sh" {
		t.Errorf("unexpected edge: %+v", edges[0])
	}
}

func TestComputeFileRefEdges_BinarySourceSkipped(t *testing.T) {
	// Binary file content is not scanned even if (in this test) we set
	// Content to a string that looks like a reference.
	files := []SkillFile{
		{Path: "assets/font.ttf", IsBinary: true, Content: "scripts/helper.sh"},
		{Path: "scripts/helper.sh", Content: ""},
	}
	edges := computeFileRefEdges(files)
	if len(edges) != 0 {
		t.Errorf("binary source should not produce edges, got %+v", edges)
	}
}

func TestComputeFileRefEdges_BinaryAsTarget(t *testing.T) {
	// Text file references a binary asset by path. That's a valid edge —
	// binaries can be referenced (e.g. "load assets/font.ttf").
	files := []SkillFile{
		{Path: "scripts/main.sh", Content: "convert -font assets/font.ttf"},
		{Path: "assets/font.ttf", IsBinary: true},
	}
	edges := computeFileRefEdges(files)
	if len(edges) != 1 || edges[0].To != "assets/font.ttf" {
		t.Fatalf("expected edge to binary target, got %+v", edges)
	}
}

func TestComputeFileRefEdges_NoSelfReference(t *testing.T) {
	// "main.sh" appearing inside scripts/main.sh shouldn't produce a
	// self-loop.
	files := []SkillFile{
		{Path: "scripts/main.sh", Content: "# main.sh — entrypoint\nscripts/main.sh args"},
	}
	edges := computeFileRefEdges(files)
	if len(edges) != 0 {
		t.Errorf("self-references should be filtered, got %+v", edges)
	}
}

func TestComputeFileRefEdges_WordBoundaryAvoidsSubstring(t *testing.T) {
	// "helper.sh" appearing as a fragment of "superhelper.shx" should not
	// match. Note: we rely on word boundaries around the unique basename.
	files := []SkillFile{
		{Path: "scripts/main.sh", Content: "superhelper.shx is unrelated"},
		{Path: "scripts/helper.sh", Content: ""},
	}
	edges := computeFileRefEdges(files)
	if len(edges) != 0 {
		t.Errorf("substring within larger identifier should not match, got %+v", edges)
	}
}

func TestComputeFileRefEdges_StableOrder(t *testing.T) {
	files := []SkillFile{
		{Path: "scripts/c.sh", Content: "scripts/a.sh and scripts/b.sh"},
		{Path: "scripts/a.sh", Content: ""},
		{Path: "scripts/b.sh", Content: ""},
	}
	edges := computeFileRefEdges(files)
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d: %+v", len(edges), edges)
	}
	if edges[0].To != "scripts/a.sh" || edges[1].To != "scripts/b.sh" {
		t.Errorf("expected edges sorted by target, got %+v", edges)
	}
}

func TestComputeFileRefEdges_FewerThanTwoFiles(t *testing.T) {
	if got := computeFileRefEdges(nil); got != nil {
		t.Errorf("nil input should yield nil, got %+v", got)
	}
	one := []SkillFile{{Path: "scripts/x.sh", Content: "x"}}
	if got := computeFileRefEdges(one); got != nil {
		t.Errorf("single-file input should yield nil, got %+v", got)
	}
}

func TestContainsWord(t *testing.T) {
	cases := []struct {
		s, word string
		want    bool
	}{
		{"helper.sh", "helper.sh", true},
		{"./helper.sh", "helper.sh", true},
		{"\"helper.sh\"", "helper.sh", true},
		{"superhelper.sh", "helper.sh", false},
		{"helper.shx", "helper.sh", false},
		{"see helper.sh end", "helper.sh", true},
		{"", "helper.sh", false},
		{"helper.sh", "", false},
	}
	for _, c := range cases {
		if got := containsWord(c.s, c.word); got != c.want {
			t.Errorf("containsWord(%q, %q) = %v, want %v", c.s, c.word, got, c.want)
		}
	}
}
