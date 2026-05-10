package main

import (
	"strings"
	"testing"
)

func TestBuildSkillMarkdown_WithBody(t *testing.T) {
	s := Skill{Name: "tester", Description: "does things", Body: "## Tester\n\nbody here"}
	out := buildSkillMarkdown(s)
	if !strings.HasPrefix(out, "---\n") {
		t.Error("missing frontmatter opener")
	}
	if !strings.Contains(out, "name: tester\n") {
		t.Error("missing name line")
	}
	if !strings.Contains(out, "description: does things\n") {
		t.Error("missing description line")
	}
	if !strings.HasSuffix(out, "body here\n") {
		t.Error("expected body to end with newline")
	}
}

func TestBuildSkillMarkdown_DescriptionNewlinesFlattened(t *testing.T) {
	s := Skill{Name: "x", Description: "line1\nline2", Body: "b"}
	out := buildSkillMarkdown(s)
	if !strings.Contains(out, "description: line1 line2\n") {
		t.Errorf("description newlines not flattened to spaces; got: %q", out)
	}
}

func TestBuildSkillMarkdown_EmptyBodyDefaults(t *testing.T) {
	s := Skill{Name: "x", Description: "d", Body: ""}
	out := buildSkillMarkdown(s)
	if !strings.Contains(out, "## x\n\nd\n") {
		t.Errorf("empty body should fall back to default heading; got: %q", out)
	}
}
