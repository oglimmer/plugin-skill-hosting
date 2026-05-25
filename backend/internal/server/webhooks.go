package server

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
)

// maxWebhookBodyBytes caps incoming webhook payloads. GitHub's "abuse-
// prevention" docs suggest 25 MB; we don't need anywhere near that — we only
// inspect `ref`. 1 MB leaves headroom for huge `commits[]` arrays without
// inviting memory pressure.
const maxWebhookBodyBytes = 1 << 20

type webhookProvider int

const (
	providerUnknown webhookProvider = iota
	providerGitHub
	providerGitLab
)

// handleGitWebhook accepts push notifications from GitHub or GitLab,
// authenticates them (HMAC-SHA256 for GitHub, plain token for GitLab),
// filters out non-push events and pushes to other branches, then triggers
// an asynchronous import. Returns 202 the moment validation passes — the
// reconciliation runs in the background so providers don't time out.
func (a *App) handleGitWebhook(w http.ResponseWriter, r *http.Request) {
	if strings.TrimSpace(a.Cfg.ExternalGitWebhookSecret) == "" {
		writeErr(w, http.StatusServiceUnavailable, "webhook not configured")
		return
	}
	if a.ExternalSync == nil {
		writeErr(w, http.StatusServiceUnavailable, "external git sync not configured")
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxWebhookBodyBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read body: "+err.Error())
		return
	}
	if len(body) > maxWebhookBodyBytes {
		writeErr(w, http.StatusRequestEntityTooLarge, "webhook body exceeds limit")
		return
	}

	provider := detectWebhookProvider(r.Header)
	switch provider {
	case providerGitHub:
		if !verifyGitHubSignature(r.Header.Get("X-Hub-Signature-256"), body, a.Cfg.ExternalGitWebhookSecret) {
			writeErr(w, http.StatusUnauthorized, "invalid signature")
			return
		}
	case providerGitLab:
		if !secureCompareString(r.Header.Get("X-Gitlab-Token"), a.Cfg.ExternalGitWebhookSecret) {
			writeErr(w, http.StatusUnauthorized, "invalid token")
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, "unrecognised webhook provider; expected GitHub or GitLab headers")
		return
	}

	if !isPushEvent(provider, r.Header) {
		// ping, merge-request, tag-push, etc. — acknowledge but do nothing.
		w.WriteHeader(http.StatusNoContent)
		return
	}

	if branch := parseRefBranch(body); branch != "" && branch != a.Cfg.ExternalGitBranch {
		log.Printf("git webhook: ignoring push to branch %q (configured: %q)", branch, a.Cfg.ExternalGitBranch)
		w.WriteHeader(http.StatusNoContent)
		return
	}

	// Run import in the background — providers expect a fast ACK and we
	// don't want to hold their connection through a fetch + DB reconcile.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), externalImportTimeout)
		defer cancel()
		if err := a.RunExternalImport(ctx); err != nil {
			log.Printf("git webhook: import failed: %v", err)
		}
	}()
	w.WriteHeader(http.StatusAccepted)
}

// detectWebhookProvider picks GitHub or GitLab based on the headers each
// provider canonically sends. Falls back to providerUnknown when neither
// match — the request is then rejected as a 400.
func detectWebhookProvider(h http.Header) webhookProvider {
	if h.Get("X-Hub-Signature-256") != "" || h.Get("X-GitHub-Event") != "" {
		return providerGitHub
	}
	if h.Get("X-Gitlab-Token") != "" || h.Get("X-Gitlab-Event") != "" {
		return providerGitLab
	}
	return providerUnknown
}

// verifyGitHubSignature compares an X-Hub-Signature-256 header against the
// HMAC-SHA256 of the raw request body. Returns false for malformed headers,
// missing secret, or mismatching digest.
func verifyGitHubSignature(header string, body []byte, secret string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(header, prefix) || secret == "" {
		return false
	}
	want, err := hex.DecodeString(strings.TrimPrefix(header, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(want, mac.Sum(nil))
}

func secureCompareString(a, b string) bool {
	return hmac.Equal([]byte(a), []byte(b))
}

func isPushEvent(provider webhookProvider, h http.Header) bool {
	switch provider {
	case providerGitHub:
		return strings.EqualFold(h.Get("X-GitHub-Event"), "push")
	case providerGitLab:
		// GitLab sends "Push Hook" for push events; "Tag Push Hook",
		// "Merge Request Hook" etc. for the rest.
		return strings.EqualFold(h.Get("X-Gitlab-Event"), "Push Hook")
	}
	return false
}

// parseRefBranch reads the top-level `ref` field from a push payload and
// returns the branch (the segment after `refs/heads/`). Empty string means
// "could not determine" — caller treats that as "don't filter".
func parseRefBranch(body []byte) string {
	var p struct {
		Ref string `json:"ref"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		return ""
	}
	return strings.TrimPrefix(p.Ref, "refs/heads/")
}
