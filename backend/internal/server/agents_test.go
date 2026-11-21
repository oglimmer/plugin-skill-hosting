package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestYAMLScalar(t *testing.T) {
	cases := []struct{ in, want string }{
		{"opus", "opus"},
		{"claude-opus-5-5", "claude-opus-5-5"},
		{"Read, Grep, Bash", "Read, Grep, Bash"},
		{"Bash(git *), Read", "Bash(git *), Read"},
		// A colon followed by a space would start a nested mapping.
		{"Use when: the user asks", `"Use when: the user asks"`},
		{"has a # hash", `"has a # hash"`},
		{`say "hi"`, `"say \"hi\""`},
		// Plain scalars YAML would resolve to a non-string type.
		{"true", `"true"`},
		{"No", `"No"`},
		{"123", `"123"`},
		{"- starts like a list", `"- starts like a list"`},
		{"", `""`},
		// HTML characters stay readable instead of becoming < escapes.
		{"a <b> & c", `"a <b> & c"`},
	}
	for _, c := range cases {
		if got := yamlScalar(c.in); got != c.want {
			t.Errorf("yamlScalar(%q) = %s, want %s", c.in, got, c.want)
		}
	}
}

func TestBuildAgentMarkdown_Full(t *testing.T) {
	out := buildAgentMarkdown(Agent{
		Name:             "worker",
		Description:      "Implementation worker. Use when: the conductor hands out a task.",
		Body:             "You are a worker.",
		Model:            "claude-opus-5-5",
		Tools:            "Read, Edit, Bash",
		ExtraFrontmatter: "color: blue",
	})
	want := "---\n" +
		"name: worker\n" +
		"description: \"Implementation worker. Use when: the conductor hands out a task.\"\n" +
		"model: claude-opus-5-5\n" +
		"tools: Read, Edit, Bash\n" +
		"color: blue\n" +
		"---\n\n" +
		"You are a worker.\n"
	if out != want {
		t.Errorf("buildAgentMarkdown mismatch\n got: %q\nwant: %q", out, want)
	}
}

func TestBuildAgentMarkdown_OmitsUnsetKeys(t *testing.T) {
	out := buildAgentMarkdown(Agent{Name: "plain", Description: "does things", Body: "prompt\n"})
	want := "---\nname: plain\ndescription: does things\n---\n\nprompt\n"
	if out != want {
		t.Errorf("got %q, want %q", out, want)
	}
}

func validAgentFields() agentFields {
	return agentFields{Name: "worker", Description: "does the work", Body: "You are a worker."}
}

func TestAgentFields_Valid(t *testing.T) {
	f := validAgentFields()
	f.Name = "  Worker "
	f.Description = "line one\nline two"
	f.Model = " opus "
	f.Tools = " Read, Grep "
	f.ExtraFrontmatter = "\ncolor: blue\n"
	if err := f.normalizeAndValidate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if f.Name != "worker" {
		t.Errorf("name = %q, want lowercased + trimmed", f.Name)
	}
	if f.Description != "line one line two" {
		t.Errorf("description = %q, want line breaks folded to spaces", f.Description)
	}
	if f.Model != "opus" || f.Tools != "Read, Grep" || f.ExtraFrontmatter != "color: blue" {
		t.Errorf("fields not trimmed: %+v", f)
	}
}

func TestAgentFields_Invalid(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*agentFields)
		wantErr string
	}{
		{"bad slug", func(f *agentFields) { f.Name = "Bad Name" }, "agent name"},
		{"path traversal name", func(f *agentFields) { f.Name = "../evil" }, "agent name"},
		{"too short name", func(f *agentFields) { f.Name = "ab" }, "agent name"},
		{"empty description", func(f *agentFields) { f.Description = "  \n " }, "description is required"},
		{"long description", func(f *agentFields) { f.Description = strings.Repeat("a", maxAgentDescriptionChars+1) }, "at most"},
		{"empty body", func(f *agentFields) { f.Body = " \n" }, "body is required"},
		{"huge body", func(f *agentFields) { f.Body = strings.Repeat("a", maxAgentBodyBytes+1) }, "body exceeds"},
		{"model with space", func(f *agentFields) { f.Model = "opus 5" }, "model must be"},
		{"model yaml injection", func(f *agentFields) { f.Model = "opus\nhooks: x" }, "model must be"},
		{"multi-line tools", func(f *agentFields) { f.Tools = "Read\nhooks: x" }, "single line"},
		{"extra ends frontmatter", func(f *agentFields) { f.ExtraFrontmatter = "color: blue\n---\nname: other" }, "document marker"},
		{"extra duplicates model", func(f *agentFields) { f.ExtraFrontmatter = "model: haiku" }, `must not set "model"`},
		{"extra duplicates quoted name", func(f *agentFields) { f.ExtraFrontmatter = `"name": other` }, `must not set "name"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := validAgentFields()
			c.mutate(&f)
			err := f.normalizeAndValidate()
			if err == nil {
				t.Fatalf("expected an error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), c.wantErr)
			}
		})
	}
}

func TestValidateAgentExtraFrontmatter_AllowsNestedAndComments(t *testing.T) {
	in := "# a comment\ncolor: blue\ndisallowedTools:\n  - Write\n  - name: nested-is-fine"
	got, err := validateAgentExtraFrontmatter(in)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != in {
		t.Errorf("extra frontmatter changed: %q", got)
	}
}

func TestWriteAgentsToWorkTree(t *testing.T) {
	dir := t.TempDir()

	// No agents: nothing is created, so agent-less plugins render unchanged.
	if err := writeAgentsToWorkTree(dir, "", nil); err != nil {
		t.Fatalf("empty write: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "agents")); !os.IsNotExist(err) {
		t.Errorf("agents/ dir must not exist without agents (stat err = %v)", err)
	}

	agents := []Agent{
		{Name: "worker", Description: "d", Body: "prompt one"},
		{Name: "reviewer", Description: "d", Body: "prompt two", Model: "sonnet"},
	}
	if err := writeAgentsToWorkTree(dir, "", agents); err != nil {
		t.Fatalf("write: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "agents", "reviewer.md"))
	if err != nil {
		t.Fatalf("read reviewer.md: %v", err)
	}
	if !strings.Contains(string(b), "model: sonnet\n") || !strings.HasSuffix(string(b), "prompt two\n") {
		t.Errorf("unexpected reviewer.md: %q", b)
	}
	if _, err := os.Stat(filepath.Join(dir, "agents", "worker.md")); err != nil {
		t.Errorf("worker.md missing: %v", err)
	}

	// A name that is not a slug must never reach the file system.
	if err := writeAgentsToWorkTree(dir, "", []Agent{{Name: "../escape", Description: "d", Body: "b"}}); err == nil {
		t.Error("expected an error for a non-slug agent name")
	}
}
