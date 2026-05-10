package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

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

func TestAuthenticateRequest_NoHeader(t *testing.T) {
	a := &App{}
	r := httptest.NewRequest("GET", "/", nil)
	u, msg := a.authenticateRequest(r)
	if u != nil {
		t.Errorf("expected nil user with no auth header, got %+v", u)
	}
	if msg != "" {
		t.Errorf("expected empty message with no auth header, got %q", msg)
	}
}

func TestAuthenticateRequest_EmptyBearer(t *testing.T) {
	a := &App{}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer ")
	u, msg := a.authenticateRequest(r)
	if u != nil {
		t.Errorf("expected nil user with empty bearer, got %+v", u)
	}
	if msg != "empty bearer token" {
		t.Errorf("msg = %q, want empty bearer token", msg)
	}
}

func TestAuthenticateRequest_BadJWT(t *testing.T) {
	// Garbage with two dots looks like a JWT but parseToken rejects it before
	// any DB lookup, so this exercises the JWT branch without a database.
	a := &App{cfg: Config{JWTSecret: "x"}}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer not.a.jwt")
	u, msg := a.authenticateRequest(r)
	if u != nil {
		t.Errorf("expected nil user with bad JWT, got %+v", u)
	}
	if msg != "invalid token" {
		t.Errorf("msg = %q, want invalid token", msg)
	}
}

func TestAuthenticateRequest_BasicWithoutPassword(t *testing.T) {
	a := &App{}
	r := httptest.NewRequest("GET", "/", nil)
	// Username without password — Go's r.BasicAuth still parses but pass is empty.
	r.SetBasicAuth("anything", "")
	u, msg := a.authenticateRequest(r)
	if u != nil {
		t.Errorf("expected nil user, got %+v", u)
	}
	if msg != "invalid basic auth" {
		t.Errorf("msg = %q, want invalid basic auth", msg)
	}
}

func TestAuthenticateRequest_UnknownScheme(t *testing.T) {
	a := &App{}
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Digest something")
	u, msg := a.authenticateRequest(r)
	if u != nil {
		t.Errorf("expected nil user for unknown scheme, got %+v", u)
	}
	if msg != "" {
		t.Errorf("msg = %q, want empty (unknown scheme is treated as no creds)", msg)
	}
}

func TestTokenGateMiddleware_ChallengesWithBasic(t *testing.T) {
	a := &App{}
	called := false
	h := a.tokenGateMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Basic") {
		t.Errorf("WWW-Authenticate = %q, want Basic challenge", got)
	}
	if called {
		t.Error("downstream handler should not run on missing auth")
	}
}

func TestMCPTokenGateMiddleware_ChallengesWithBearer(t *testing.T) {
	a := &App{}
	h := a.mcpTokenGateMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("downstream handler should not run on missing auth")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if got := rec.Header().Get("WWW-Authenticate"); !strings.HasPrefix(got, "Bearer") {
		t.Errorf("WWW-Authenticate = %q, want Bearer challenge", got)
	}
}

func TestAuthMiddleware_RejectsMissingToken(t *testing.T) {
	a := &App{}
	h := a.authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("downstream handler should not run")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "missing bearer token") {
		t.Errorf("body = %q, want missing-bearer-token error", rec.Body.String())
	}
}

func TestAuthMiddleware_RejectsBadJWT(t *testing.T) {
	a := &App{cfg: Config{JWTSecret: "x"}}
	h := a.authMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("downstream handler should not run")
	}))
	rec := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/", nil)
	r.Header.Set("Authorization", "Bearer not.a.jwt")
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "invalid token") {
		t.Errorf("body = %q, want invalid-token error", rec.Body.String())
	}
}

func TestCurrentUser_NotSet(t *testing.T) {
	r := httptest.NewRequest("GET", "/", nil)
	if u := currentUser(r); u != nil {
		t.Errorf("currentUser without context value should return nil, got %+v", u)
	}
}
