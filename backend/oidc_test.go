package main

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSanitizeUsername(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"alice", "alice"},
		{"Alice_99", "Alice_99"},
		{"  spaced  ", "spaced"},
		{"weird name!", "weird_name"},                    // space → _, ! → _, trailing _ trimmed
		{"---bad---", "bad"},                             // trim leading/trailing - and _
		{"___", ""},                                      // all-junk after trim
		{"ab", ""},                                       // too short after sanitisation
		{"abcdefghijklmnopqrstuvwxyz0123456789", "abcdefghijklmnopqrstuvwxyz012345"}, // 32 cap
		{"a/b\\c", "a_b_c"},
		{"über", "ber"}, // ü → _, trim leading underscore → "ber"
	}
	for _, c := range cases {
		got := sanitizeUsername(c.in)
		if got != c.want {
			t.Errorf("sanitizeUsername(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	cases := []struct {
		in   string
		n    int
		want string
	}{
		{"abcdef", 3, "abc"},
		{"abc", 5, "abc"},
		{"", 5, ""},
		{"abcdef", 0, ""},
	}
	for _, c := range cases {
		if got := truncate(c.in, c.n); got != c.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", c.in, c.n, got, c.want)
		}
	}
}

func TestSafeIssuerHost(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"https://accounts.example.com", "accounts.example.com"},
		{"https://idp.example.com:8443/realms/main", "idp.example.com:8443"},
		{"", "oidc.local"},
		{"::not-a-url::", "oidc.local"},
	}
	for _, c := range cases {
		if got := safeIssuerHost(c.in); got != c.want {
			t.Errorf("safeIssuerHost(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRandHex_LengthAndUniqueness(t *testing.T) {
	a, err := randHex(8)
	if err != nil {
		t.Fatalf("randHex: %v", err)
	}
	if len(a) != 16 { // 8 bytes hex-encoded
		t.Errorf("len = %d, want 16", len(a))
	}
	b, _ := randHex(8)
	if a == b {
		t.Error("two randHex calls produced identical output")
	}
}

func TestSetShortLivedCookie_HTTPS(t *testing.T) {
	a := &App{cfg: Config{PublicBaseURL: "https://example.com"}}
	rec := httptest.NewRecorder()
	a.setShortLivedCookie(rec, "test", "value123")
	hdr := rec.Header().Get("Set-Cookie")
	if hdr == "" {
		t.Fatal("expected Set-Cookie header")
	}
	if !strings.Contains(hdr, "test=value123") {
		t.Errorf("cookie value missing: %q", hdr)
	}
	if !strings.Contains(hdr, "HttpOnly") {
		t.Errorf("HttpOnly missing: %q", hdr)
	}
	if !strings.Contains(hdr, "Secure") {
		t.Errorf("Secure flag missing for https base: %q", hdr)
	}
	if !strings.Contains(hdr, "Path=/api/auth/oidc") {
		t.Errorf("Path scope wrong: %q", hdr)
	}
}

func TestSetShortLivedCookie_HTTP_NoSecure(t *testing.T) {
	a := &App{cfg: Config{PublicBaseURL: "http://localhost:8080"}}
	rec := httptest.NewRecorder()
	a.setShortLivedCookie(rec, "test", "v")
	hdr := rec.Header().Get("Set-Cookie")
	if strings.Contains(hdr, "Secure") {
		t.Errorf("Secure should be omitted for http base, got %q", hdr)
	}
}

func TestClearCookie(t *testing.T) {
	rec := httptest.NewRecorder()
	clearCookie(rec, "myname")
	hdr := rec.Header().Get("Set-Cookie")
	if !strings.Contains(hdr, "myname=") {
		t.Errorf("missing cookie name: %q", hdr)
	}
	if !strings.Contains(hdr, "Max-Age=0") {
		t.Errorf("Max-Age=0 missing: %q", hdr)
	}
}
