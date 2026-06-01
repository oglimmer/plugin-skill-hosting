package server

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
)

func TestValidateSkillFilePath(t *testing.T) {
	good := []string{
		"scripts/run.sh",
		"references/notes.md",
		"assets/img/logo.png",
		"scripts/a/b/c/d.txt", // 5 segments — under the 6-segment cap
		"custom/x.sh",         // arbitrary top-level folder via API tool
		"evals/case_01.json",
		"config.json", // root-level file (bare filename)
		"LICENSE",     // root-level file, no extension
	}
	for _, p := range good {
		if _, err := validateSkillFilePath(p); err != nil {
			t.Errorf("validateSkillFilePath(%q) unexpected err: %v", p, err)
		}
	}

	bad := []struct {
		in     string
		reason string
	}{
		{"", "empty"},
		{"scripts/../etc/passwd", "non-canonical"},
		{"./scripts/x.sh", "non-canonical"},
		{"/scripts/x.sh", "absolute"},
		{"SKILL.md", "reserved manifest name"},
		{"skill.md", "reserved manifest name (case-insensitive)"},
		{"scripts/a/b/c/d/e/f.txt", "too deep"},
		{"scripts/with space.sh", "bad chars"},
		{"with space/x.sh", "bad chars in top-level folder"},
		{"scripts/dir/", "trailing slash → non-canonical"},
	}
	for _, c := range bad {
		if _, err := validateSkillFilePath(c.in); err == nil {
			t.Errorf("validateSkillFilePath(%q) should fail (%s)", c.in, c.reason)
		}
	}
}

func TestDecodeFileContent_Text(t *testing.T) {
	req := &skillFileUpsertReq{Content: "hello"}
	data, isBin, err := decodeFileContent(req)
	if err != nil {
		t.Fatalf("decodeFileContent: %v", err)
	}
	if isBin {
		t.Error("expected text mode")
	}
	if string(data) != "hello" {
		t.Errorf("data = %q, want hello", string(data))
	}
}

func TestDecodeFileContent_RejectsInvalidUTF8(t *testing.T) {
	req := &skillFileUpsertReq{Content: "\xff\xfe\xfd"}
	if _, _, err := decodeFileContent(req); err == nil {
		t.Error("expected invalid-UTF8 error")
	}
}

func TestDecodeFileContent_Binary(t *testing.T) {
	raw := []byte{0x00, 0x01, 0x02, 0xff}
	tru := true
	req := &skillFileUpsertReq{
		Content:  base64.StdEncoding.EncodeToString(raw),
		IsBinary: &tru,
	}
	data, isBin, err := decodeFileContent(req)
	if err != nil {
		t.Fatalf("decodeFileContent: %v", err)
	}
	if !isBin {
		t.Error("expected binary mode")
	}
	if string(data) != string(raw) {
		t.Errorf("decoded payload mismatch")
	}
}

func TestDecodeFileContent_BadBase64(t *testing.T) {
	tru := true
	req := &skillFileUpsertReq{Content: "not base64!!!", IsBinary: &tru}
	if _, _, err := decodeFileContent(req); err == nil {
		t.Error("expected base64 decode error")
	}
}

func TestDecodeFileContent_NilIsBinaryDefaultsToText(t *testing.T) {
	req := &skillFileUpsertReq{Content: "plain text", IsBinary: nil}
	data, isBin, err := decodeFileContent(req)
	if err != nil {
		t.Fatalf("decodeFileContent: %v", err)
	}
	if isBin {
		t.Error("nil IsBinary should default to text")
	}
	if string(data) != "plain text" {
		t.Errorf("data = %q", string(data))
	}
}

func TestDecodeFileContent_FalseIsBinaryRejectsInvalidUTF8(t *testing.T) {
	fls := false
	req := &skillFileUpsertReq{Content: "\xff\xfe", IsBinary: &fls}
	if _, _, err := decodeFileContent(req); err == nil {
		t.Error("expected invalid-UTF8 error when IsBinary=false")
	}
}

func TestValidateSkillFilePath_LengthCap(t *testing.T) {
	long := "scripts/" + strings.Repeat("a", 250)
	if _, err := validateSkillFilePath(long); err == nil {
		t.Error("expected too-long error")
	}
}

func TestValidateSkillFilePath_AllRoots(t *testing.T) {
	for _, root := range []string{"scripts", "references", "assets"} {
		if _, err := validateSkillFilePath(root + "/x.md"); err != nil {
			t.Errorf("root %s/x.md should be accepted: %v", root, err)
		}
	}
}

func TestValidateSkillFilePath_DotFile(t *testing.T) {
	// "." segments are explicitly rejected; a leading dot in a filename like
	// ".env" is a single segment so should be allowed by the segment regex.
	if _, err := validateSkillFilePath("scripts/.env"); err != nil {
		t.Errorf("dotfile under whitelisted root should be allowed: %v", err)
	}
}

func TestFilePathParam_StripsLeadingSlash(t *testing.T) {
	r := chi.NewRouter()
	var captured string
	r.Get("/files/*", func(_ http.ResponseWriter, req *http.Request) {
		captured = filePathParam(req)
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest("GET", "/files/scripts/run.sh", nil))
	if captured != "scripts/run.sh" {
		t.Errorf("captured = %q, want scripts/run.sh", captured)
	}
}
