package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"marketplace/internal/config"
)

func TestUserSummary_OmitsSecrets(t *testing.T) {
	// Belt-and-braces: confirm the public DTO has no password/api_token/oidc
	// fields. If anyone adds one by mistake, this test fails loudly.
	u := UserSummary{ID: "id", Username: "alice", Email: "a@b.com"}
	buf, err := json.Marshal(u)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	body := string(buf)
	for _, forbidden := range []string{"password", "api_token", "apiToken", "oidc"} {
		if strings.Contains(strings.ToLower(body), strings.ToLower(forbidden)) {
			t.Errorf("UserSummary JSON %q contains forbidden field %q", body, forbidden)
		}
	}
}

func TestListUsers_RequiresAuth(t *testing.T) {
	// Build the real router so we exercise the route-table wiring, not just
	// the handler in isolation. With no Authorization header the request must
	// be rejected by authMiddleware before the (nil) DB is touched.
	app := &App{Cfg: config.Config{
		AuthMode:  "password",
		JWTSecret: "x",
	}}
	h := NewRouter(app)
	for _, path := range []string{
		"/api/users",
		"/api/users/00000000-0000-0000-0000-000000000000/approve",
		"/api/users/00000000-0000-0000-0000-000000000000/reject",
	} {
		method := "GET"
		if path != "/api/users" {
			method = "POST"
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("%s %s: status = %d, want 401", method, path, rec.Code)
		}
	}
}

func TestRequireApprovedMiddleware_Forbids(t *testing.T) {
	a := &App{}
	called := false
	h := a.requireApprovedMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))
	cases := []struct {
		status     string
		wantCode   int
		wantCalled bool
	}{
		{UserStatusApproved, http.StatusOK, true},
		{UserStatusPending, http.StatusForbidden, false},
		{UserStatusRejected, http.StatusForbidden, false},
	}
	for _, c := range cases {
		called = false
		r := httptest.NewRequest("GET", "/", nil)
		r = r.WithContext(context.WithValue(r.Context(), ctxUserKey,
			&User{ID: "u1", Status: c.status}))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != c.wantCode {
			t.Errorf("status=%s: code = %d, want %d", c.status, rec.Code, c.wantCode)
		}
		if called != c.wantCalled {
			t.Errorf("status=%s: handler called = %v, want %v", c.status, called, c.wantCalled)
		}
	}
}

func TestIsUUID(t *testing.T) {
	good := []string{
		"00000000-0000-0000-0000-000000000000",
		"deadbeef-1234-5678-9abc-deadbeef1234",
		"DEADBEEF-1234-5678-9ABC-DEADBEEF1234",
	}
	bad := []string{
		"",
		"not-a-uuid",
		"00000000-0000-0000-0000-00000000000",   // 35
		"00000000-0000-0000-0000-0000000000000", // 37
		"00000000_0000_0000_0000_000000000000",  // wrong separators
		"gggggggg-0000-0000-0000-000000000000",  // non-hex
	}
	for _, s := range good {
		if !isUUID(s) {
			t.Errorf("isUUID(%q) = false, want true", s)
		}
	}
	for _, s := range bad {
		if isUUID(s) {
			t.Errorf("isUUID(%q) = true, want false", s)
		}
	}
}

func TestAuthConfig_ApprovalFlagReflectsMode(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.Config
		want bool
	}{
		{"password", config.Config{AuthMode: "password"}, false},
		{"oidc corporate (domains set)",
			config.Config{AuthMode: "oidc", AllowedGoogleWorkspaceDomains: []string{"acme.com"}}, false},
		{"oidc open (no domains)", config.Config{AuthMode: "oidc"}, true},
	}
	for _, c := range cases {
		a := &App{Cfg: c.cfg}
		rec := httptest.NewRecorder()
		a.handleAuthConfig(rec, httptest.NewRequest("GET", "/api/auth/config", nil))
		body := rec.Body.String()
		needle := `"userApprovalRequired":false`
		if c.want {
			needle = `"userApprovalRequired":true`
		}
		if !strings.Contains(body, needle) {
			t.Errorf("%s: body = %q, want substring %q", c.name, body, needle)
		}
	}
}
