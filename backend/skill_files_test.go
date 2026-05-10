package main

import (
	"encoding/base64"
	"testing"
)

func TestValidateSkillFilePath(t *testing.T) {
	good := []string{
		"scripts/run.sh",
		"references/notes.md",
		"assets/img/logo.png",
		"scripts/a/b/c/d.txt", // 5 segments — under the 6-segment cap
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
		{"random/x.sh", "wrong root"},
		{"scripts", "no filename"},
		{"scripts/a/b/c/d/e/f.txt", "too deep"},
		{"scripts/with space.sh", "bad chars"},
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
