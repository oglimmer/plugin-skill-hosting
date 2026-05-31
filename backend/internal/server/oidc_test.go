package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"marketplace/internal/config"
)

func TestSanitizeUsername(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"alice", "alice"},
		{"Alice_99", "Alice_99"},
		{"  spaced  ", "spaced"},
		{"weird name!", "weird_name"}, // space → _, ! → _, trailing _ trimmed
		{"---bad---", "bad"},          // trim leading/trailing - and _
		{"___", ""},                   // all-junk after trim
		{"ab", ""},                    // too short after sanitisation
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
	a := &App{Cfg: config.Config{PublicBaseURL: "https://example.com"}}
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
	a := &App{Cfg: config.Config{PublicBaseURL: "http://localhost:8080"}}
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

// approvalApp returns an App configured for RP-initiated logout: open OIDC
// (no Google Workspace domains) with a real end_session_endpoint stored on
// the runtime so we don't need a live provider during tests.
func approvalApp(endSession string) *App {
	return &App{
		Cfg: config.Config{
			AuthMode:                      "oidc",
			OIDCClientID:                  "test-client",
			PublicBaseURL:                 "https://example.com",
			AllowedGoogleWorkspaceDomains: nil,
		},
		OIDC: &oidcRuntime{endSessionEndpoint: endSession},
	}
}

func TestOIDCLogout_RedirectsToIdPWithHint(t *testing.T) {
	a := approvalApp("https://idp.example.com/realms/r/protocol/openid-connect/logout")
	r := httptest.NewRequest("GET", "/api/auth/oidc/logout", nil)
	r.AddCookie(&http.Cookie{Name: oidcIDTokenCookie, Value: "the.id.token"})
	rec := httptest.NewRecorder()
	a.handleOIDCLogout(rec, r)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	u, err := url.Parse(loc)
	if err != nil {
		t.Fatalf("Location is not a url: %v", err)
	}
	if u.Host != "idp.example.com" {
		t.Errorf("redirect host = %q, want idp.example.com", u.Host)
	}
	q := u.Query()
	if got := q.Get("id_token_hint"); got != "the.id.token" {
		t.Errorf("id_token_hint = %q, want the.id.token", got)
	}
	if got := q.Get("post_logout_redirect_uri"); got != "https://example.com/login" {
		t.Errorf("post_logout_redirect_uri = %q", got)
	}
	if got := q.Get("client_id"); got != "test-client" {
		t.Errorf("client_id = %q, want test-client", got)
	}
	// And the cookie should be cleared on the response.
	if !strings.Contains(rec.Header().Get("Set-Cookie"), oidcIDTokenCookie+"=") {
		t.Errorf("Set-Cookie does not clear id_token cookie: %q", rec.Header().Get("Set-Cookie"))
	}
}

func TestOIDCLogout_FallsBackWhenNoEndSession(t *testing.T) {
	a := approvalApp("") // IdP didn't advertise end_session_endpoint
	r := httptest.NewRequest("GET", "/api/auth/oidc/logout", nil)
	r.AddCookie(&http.Cookie{Name: oidcIDTokenCookie, Value: "x"})
	rec := httptest.NewRecorder()
	a.handleOIDCLogout(rec, r)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "https://example.com/login" {
		t.Errorf("Location = %q, want fallback to /login", loc)
	}
}

func TestOIDCLogout_CorporateModeStaysLocal(t *testing.T) {
	// Corporate mode = OIDC with a Google Workspace domain configured. Even
	// if the IdP exposes end_session_endpoint, we deliberately keep the user
	// signed in upstream so workspace SSO across apps isn't disturbed.
	a := &App{
		Cfg: config.Config{
			AuthMode:                      "oidc",
			OIDCClientID:                  "c",
			PublicBaseURL:                 "https://example.com",
			AllowedGoogleWorkspaceDomains: []string{"acme.com"},
		},
		OIDC: &oidcRuntime{endSessionEndpoint: "https://idp.example.com/logout"},
	}
	r := httptest.NewRequest("GET", "/api/auth/oidc/logout", nil)
	r.AddCookie(&http.Cookie{Name: oidcIDTokenCookie, Value: "x"})
	rec := httptest.NewRecorder()
	a.handleOIDCLogout(rec, r)

	if loc := rec.Header().Get("Location"); loc != "https://example.com/login" {
		t.Errorf("Location = %q, want local /login (corporate mode)", loc)
	}
}

func TestOIDCLogout_MissingCookieStillRedirects(t *testing.T) {
	a := approvalApp("https://idp.example.com/logout")
	rec := httptest.NewRecorder()
	a.handleOIDCLogout(rec, httptest.NewRequest("GET", "/api/auth/oidc/logout", nil))
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.Contains(loc, "idp.example.com") {
		t.Errorf("Location = %q, want IdP redirect even without cookie", loc)
	}
	u, _ := url.Parse(loc)
	if u.Query().Get("id_token_hint") != "" {
		t.Errorf("id_token_hint should be absent when cookie was missing, got %q", u.Query().Get("id_token_hint"))
	}
}

// A failed callback (here: no state cookie) must redirect the browser to the
// SPA callback with a STABLE reason code in the fragment — never raw error
// text — so OIDCCallbackView can render friendly copy.
func TestOIDCCallback_FailureRedirectsWithReasonCode(t *testing.T) {
	a := &App{Cfg: config.Config{AuthMode: "oidc", PublicBaseURL: "https://example.com"}}
	// No oidc_state cookie → state check fails before any DB/IdP work.
	r := httptest.NewRequest("GET", "/api/auth/oidc/callback?state=whatever", nil)
	rec := httptest.NewRecorder()
	a.handleOIDCCallback(rec, r)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want 302", rec.Code)
	}
	loc := rec.Header().Get("Location")
	if !strings.HasPrefix(loc, "https://example.com/auth/callback#") {
		t.Fatalf("Location = %q, want SPA callback redirect", loc)
	}
	if !strings.Contains(loc, "error="+oidcErrProvider) {
		t.Errorf("Location = %q, want error=%s", loc, oidcErrProvider)
	}
}
