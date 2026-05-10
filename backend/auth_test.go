package main

import "testing"

func TestUsernameRegex(t *testing.T) {
	good := []string{"abc", "user_1", "User-Name", "u-_-u", "abcdefghijklmnopqrstuvwxyz012345"} // 32 chars
	bad := []string{"", "ab", "abc!", "with space", "öhh", "way-too-long-username-that-exceeds-thirtytwo-chars"}
	for _, s := range good {
		if !usernameRe.MatchString(s) {
			t.Errorf("usernameRe should accept %q", s)
		}
	}
	for _, s := range bad {
		if usernameRe.MatchString(s) {
			t.Errorf("usernameRe should reject %q", s)
		}
	}
}

func TestSlugRegex(t *testing.T) {
	// Slug rule: ^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$ — min 3 chars, max 64,
	// alphanumeric ends, lowercase only.
	good := []string{"abc", "my-plugin", "abc-123", "a0a"}
	bad := []string{"", "a", "ab", "-bad", "bad-", "Caps", "with_underscore"}
	for _, s := range good {
		if !slugRe.MatchString(s) {
			t.Errorf("slugRe should accept %q", s)
		}
	}
	for _, s := range bad {
		if slugRe.MatchString(s) {
			t.Errorf("slugRe should reject %q", s)
		}
	}
}

func TestIssueAndParseTokenRoundtrip(t *testing.T) {
	a := &App{cfg: Config{JWTSecret: "test-secret-do-not-use"}}
	tok, err := a.issueToken("user-123")
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	got, err := a.parseToken(tok)
	if err != nil {
		t.Fatalf("parseToken: %v", err)
	}
	if got != "user-123" {
		t.Errorf("parseToken returned %q, want user-123", got)
	}
}

func TestParseTokenRejectsWrongSecret(t *testing.T) {
	signer := &App{cfg: Config{JWTSecret: "secret-A"}}
	verifier := &App{cfg: Config{JWTSecret: "secret-B"}}
	tok, err := signer.issueToken("user-123")
	if err != nil {
		t.Fatalf("issueToken: %v", err)
	}
	if _, err := verifier.parseToken(tok); err == nil {
		t.Error("parseToken accepted token signed with a different secret")
	}
}

func TestParseTokenRejectsGarbage(t *testing.T) {
	a := &App{cfg: Config{JWTSecret: "x"}}
	if _, err := a.parseToken("not-a-jwt"); err == nil {
		t.Error("parseToken accepted non-JWT input")
	}
}

func TestGenerateAPIToken(t *testing.T) {
	tok1, err := generateAPIToken()
	if err != nil {
		t.Fatalf("generateAPIToken: %v", err)
	}
	tok2, err := generateAPIToken()
	if err != nil {
		t.Fatalf("generateAPIToken: %v", err)
	}
	if tok1 == tok2 {
		t.Error("two generated tokens collided")
	}
	if len(tok1) != 64 { // 32 bytes hex-encoded
		t.Errorf("token length = %d, want 64", len(tok1))
	}
}
