package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
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

func TestBuildSkillMarkdown_BodyWithoutTrailingNewline(t *testing.T) {
	s := Skill{Name: "n", Description: "d", Body: "no newline"}
	out := buildSkillMarkdown(s)
	if !strings.HasSuffix(out, "no newline\n") {
		t.Errorf("expected trailing newline appended; got: %q", out)
	}
}

func TestRepoAndWorkPaths(t *testing.T) {
	a := &App{cfg: Config{DataDir: "/var/data"}}
	if got := a.repoPath("foo"); got != "/var/data/repos/foo.git" {
		t.Errorf("repoPath = %q", got)
	}
	if got := a.workPath("foo"); got != "/var/data/work/foo" {
		t.Errorf("workPath = %q", got)
	}
}

func TestWipeWorkTree_PreservesGitDir(t *testing.T) {
	dir := t.TempDir()
	mustMkdir(t, filepath.Join(dir, ".git"))
	mustWrite(t, filepath.Join(dir, ".git", "HEAD"), "ref: refs/heads/main\n")
	mustWrite(t, filepath.Join(dir, "README.md"), "hi")
	mustMkdir(t, filepath.Join(dir, "skills"))
	mustWrite(t, filepath.Join(dir, "skills", "a.txt"), "x")

	if err := wipeWorkTree(dir); err != nil {
		t.Fatalf("wipeWorkTree: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, ".git", "HEAD")); err != nil {
		t.Errorf(".git contents removed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "README.md")); !os.IsNotExist(err) {
		t.Errorf("README.md should be gone, err = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "skills")); !os.IsNotExist(err) {
		t.Errorf("skills/ should be gone, err = %v", err)
	}
}

func TestWipeWorkTree_MissingDir(t *testing.T) {
	if err := wipeWorkTree(filepath.Join(t.TempDir(), "does-not-exist")); err == nil {
		t.Error("expected error for missing directory")
	}
}

func TestWriteSkillFileToWorkTree_Text(t *testing.T) {
	dir := t.TempDir()
	f := SkillFile{Path: "scripts/run.sh", Content: "#!/bin/sh\necho hi\n"}
	if err := writeSkillFileToWorkTree(dir, f); err != nil {
		t.Fatalf("writeSkillFileToWorkTree: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "scripts", "run.sh"))
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	if string(got) != f.Content {
		t.Errorf("content = %q, want %q", string(got), f.Content)
	}
}

func TestWriteSkillFileToWorkTree_Binary(t *testing.T) {
	dir := t.TempDir()
	raw := []byte{0x00, 0x01, 0xff, 0xfe}
	f := SkillFile{
		Path:     "assets/tiny.bin",
		IsBinary: true,
		Content:  base64.StdEncoding.EncodeToString(raw),
	}
	if err := writeSkillFileToWorkTree(dir, f); err != nil {
		t.Fatalf("writeSkillFileToWorkTree: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "assets", "tiny.bin"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(raw) {
		t.Errorf("binary mismatch")
	}
}

func TestWriteSkillFileToWorkTree_BadBase64(t *testing.T) {
	dir := t.TempDir()
	f := SkillFile{Path: "assets/x.bin", IsBinary: true, Content: "not_base64!!!"}
	if err := writeSkillFileToWorkTree(dir, f); err == nil {
		t.Error("expected error for invalid base64 content")
	}
}

func mustMkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", path, err)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}
