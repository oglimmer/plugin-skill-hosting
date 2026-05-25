package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"marketplace/internal/config"
)

func TestVerifyGitHubSignature_Valid(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	secret := "shh"
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	header := "sha256=" + hex.EncodeToString(mac.Sum(nil))
	if !verifyGitHubSignature(header, body, secret) {
		t.Error("expected valid signature to pass")
	}
}

func TestVerifyGitHubSignature_BadSig(t *testing.T) {
	body := []byte(`{"ref":"refs/heads/main"}`)
	if verifyGitHubSignature("sha256=deadbeef", body, "shh") {
		t.Error("garbage signature should fail")
	}
	if verifyGitHubSignature("not-prefixed", body, "shh") {
		t.Error("missing prefix should fail")
	}
	if verifyGitHubSignature("sha256=zz", body, "shh") {
		t.Error("non-hex hex should fail")
	}
	if verifyGitHubSignature("sha256=00", body, "") {
		t.Error("empty secret should fail")
	}
}

func TestDetectWebhookProvider(t *testing.T) {
	cases := []struct {
		name string
		h    http.Header
		want webhookProvider
	}{
		{"github-sig", http.Header{"X-Hub-Signature-256": {"sha256=x"}}, providerGitHub},
		{"github-event", http.Header{"X-Github-Event": {"push"}}, providerGitHub},
		{"gitlab-token", http.Header{"X-Gitlab-Token": {"x"}}, providerGitLab},
		{"gitlab-event", http.Header{"X-Gitlab-Event": {"Push Hook"}}, providerGitLab},
		{"none", http.Header{}, providerUnknown},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := detectWebhookProvider(c.h); got != c.want {
				t.Errorf("detectWebhookProvider = %v, want %v", got, c.want)
			}
		})
	}
}

func TestIsPushEvent(t *testing.T) {
	push := http.Header{"X-Github-Event": {"push"}}
	if !isPushEvent(providerGitHub, push) {
		t.Error("github push should be a push event")
	}
	ping := http.Header{"X-Github-Event": {"ping"}}
	if isPushEvent(providerGitHub, ping) {
		t.Error("github ping should not be a push event")
	}
	gl := http.Header{"X-Gitlab-Event": {"Push Hook"}}
	if !isPushEvent(providerGitLab, gl) {
		t.Error("gitlab Push Hook should be a push event")
	}
	mr := http.Header{"X-Gitlab-Event": {"Merge Request Hook"}}
	if isPushEvent(providerGitLab, mr) {
		t.Error("gitlab MR hook should not be a push event")
	}
}

func TestParseRefBranch(t *testing.T) {
	cases := map[string]string{
		`{"ref":"refs/heads/main"}`:        "main",
		`{"ref":"refs/heads/feature/foo"}`: "feature/foo",
		`{"ref":"refs/tags/v1.0"}`:         "refs/tags/v1.0", // not stripped (signals "ignore")
		`{"ref":""}`:                       "",
		`malformed json`:                   "",
		`{"other":"x"}`:                    "",
	}
	for in, want := range cases {
		if got := parseRefBranch([]byte(in)); got != want {
			t.Errorf("parseRefBranch(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestHandleGitWebhook_NoSecretConfigured returns 503 when the webhook
// secret is empty — the endpoint is effectively disabled.
func TestHandleGitWebhook_NoSecretConfigured(t *testing.T) {
	a := &App{Cfg: config.Config{}}
	req := httptest.NewRequest("POST", "/api/webhooks/git", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	a.handleGitWebhook(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", w.Code)
	}
}

// TestHandleGitWebhook_NoExternalSync returns 503 when ExternalSync is nil
// even if the secret is set — an import would have nothing to fetch into.
func TestHandleGitWebhook_NoExternalSync(t *testing.T) {
	a := &App{Cfg: config.Config{ExternalGitWebhookSecret: "shh"}}
	req := httptest.NewRequest("POST", "/api/webhooks/git", bytes.NewReader([]byte(`{}`)))
	w := httptest.NewRecorder()
	a.handleGitWebhook(w, req)
	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503; body=%s", w.Code, w.Body.String())
	}
}

// TestHandleGitWebhook_GitHubBadSig returns 401 on signature mismatch.
func TestHandleGitWebhook_GitHubBadSig(t *testing.T) {
	a := newTestAppWithSync(t)
	a.Cfg.ExternalGitWebhookSecret = "shh"
	a.Cfg.ExternalGitBranch = "main"
	body := []byte(`{"ref":"refs/heads/main"}`)
	req := httptest.NewRequest("POST", "/api/webhooks/git", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	req.Header.Set("X-Hub-Signature-256", "sha256=deadbeef")
	w := httptest.NewRecorder()
	a.handleGitWebhook(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestHandleGitWebhook_GitHubBranchFilter returns 204 when push is to a
// branch other than the configured one (no import triggered).
func TestHandleGitWebhook_GitHubBranchFilter(t *testing.T) {
	a := newTestAppWithSync(t)
	a.Cfg.ExternalGitWebhookSecret = "shh"
	a.Cfg.ExternalGitBranch = "main"
	body := []byte(`{"ref":"refs/heads/feature/x"}`)
	req := signedGitHubRequest(body, "shh")
	w := httptest.NewRecorder()
	a.handleGitWebhook(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("status = %d, want 204; body=%s", w.Code, w.Body.String())
	}
}

// TestHandleGitWebhook_GitHubPing returns 204 for non-push events (ping,
// installation, etc.) — we acknowledge but skip.
func TestHandleGitWebhook_GitHubPing(t *testing.T) {
	a := newTestAppWithSync(t)
	a.Cfg.ExternalGitWebhookSecret = "shh"
	body := []byte(`{"zen":"non est ad astra mollis e terris via"}`)
	req := httptest.NewRequest("POST", "/api/webhooks/git", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "ping")
	mac := hmac.New(sha256.New, []byte("shh"))
	mac.Write(body)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	w := httptest.NewRecorder()
	a.handleGitWebhook(w, req)
	if w.Code != http.StatusNoContent {
		t.Errorf("ping should yield 204; got %d", w.Code)
	}
}

// TestHandleGitWebhook_GitLabBadToken returns 401 on mismatched plain token.
func TestHandleGitWebhook_GitLabBadToken(t *testing.T) {
	a := newTestAppWithSync(t)
	a.Cfg.ExternalGitWebhookSecret = "shh"
	req := httptest.NewRequest("POST", "/api/webhooks/git", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("X-Gitlab-Token", "wrong")
	req.Header.Set("X-Gitlab-Event", "Push Hook")
	w := httptest.NewRecorder()
	a.handleGitWebhook(w, req)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", w.Code)
	}
}

// TestHandleGitWebhook_BodyTooLarge rejects payloads exceeding the cap.
func TestHandleGitWebhook_BodyTooLarge(t *testing.T) {
	a := newTestAppWithSync(t)
	a.Cfg.ExternalGitWebhookSecret = "shh"
	huge := bytes.Repeat([]byte("a"), maxWebhookBodyBytes+10)
	req := signedGitHubRequest(huge, "shh")
	w := httptest.NewRecorder()
	a.handleGitWebhook(w, req)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("status = %d, want 413", w.Code)
	}
}

// signedGitHubRequest builds a valid GitHub push request signed with secret.
func signedGitHubRequest(body []byte, secret string) *http.Request {
	req := httptest.NewRequest("POST", "/api/webhooks/git", bytes.NewReader(body))
	req.Header.Set("X-GitHub-Event", "push")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	req.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	return req
}

// newTestAppWithSync produces an App with ExternalSync set to a dummy
// (non-functional) struct so the webhook handler progresses past the
// "external sync not configured" gate. The handler kicks off RunExternalImport
// in a goroutine, which is a no-op because we don't drive it from the test.
func newTestAppWithSync(t *testing.T) *App {
	t.Helper()
	tmp := t.TempDir()
	return &App{
		Cfg: config.Config{
			ExternalGitRemoteURL: "file://" + filepath.Join(tmp, "remote.git"),
			ExternalGitBranch:    "main",
		},
		ExternalSync: &externalSync{workDir: filepath.Join(tmp, "workdir")},
	}
}

// TestSecureCompareString uses constant-time comparison so it doesn't leak
// secret length differences.
func TestSecureCompareString(t *testing.T) {
	if !secureCompareString("abc", "abc") {
		t.Error("equal strings should match")
	}
	if secureCompareString("abc", "abd") {
		t.Error("different strings should not match")
	}
	if secureCompareString("abc", "abcd") {
		t.Error("different-length strings should not match")
	}
	// Sanity: differs from naive strings.EqualFold.
	if secureCompareString("ABC", "abc") {
		t.Error("comparison is case-sensitive")
	}
	if !strings.EqualFold("ABC", "abc") {
		t.Skip("test premise broken")
	}
}
