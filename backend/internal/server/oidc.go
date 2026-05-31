package server

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"

	"marketplace/internal/metrics"
	"marketplace/internal/workspaceauth"
)

type oidcRuntime struct {
	provider           *oidc.Provider
	verifier           *oidc.IDTokenVerifier
	oauth2             *oauth2.Config
	endSessionEndpoint string // empty when the IdP's discovery document doesn't advertise one
}

const (
	oidcStateCookie   = "oidc_state"
	oidcNonceCookie   = "oidc_nonce"
	oidcIDTokenCookie = "oidc_id_token"
	// Cookie lifetime for the cached id_token. Matches the JWT lifetime
	// (30 days) so RP-initiated logout still has a hint to send even if
	// the user lets the tab sit for weeks.
	oidcIDTokenMaxAge = 30 * 24 * 3600
)

// OIDC sign-in failure reason codes. They travel to the SPA as
// /auth/callback#error=<code>; OIDCCallbackView.vue maps each to user-facing
// copy, so keep the two in sync. Stable, non-sensitive identifiers only — never
// raw error text (which could leak internals into the URL/history).
const (
	oidcErrProvider      = "provider_error"     // protocol / IdP / transient — retryable
	oidcErrDomain        = "domain_not_allowed" // Workspace-domain allowlist rejection
	oidcErrAccountReject = "account_rejected"   // admin declined the account
	oidcErrAccountGone   = "account_deleted"    // account no longer active
	oidcErrEmailConflict = "email_conflict"     // unverified email collides with an existing account
	oidcErrAccount       = "account_error"      // unexpected provisioning failure
)

// Provisioning outcomes from findOrCreateOIDCUser that the callback maps to the
// reason codes above. Sentinels (matched with errors.Is) so the message text
// never reaches the user-facing URL.
var (
	errOIDCAccountRejected = errors.New("oidc: account rejected")
	errOIDCAccountDeleted  = errors.New("oidc: account deleted")
	errOIDCEmailConflict   = errors.New("oidc: unverified email conflicts with an existing account")
)

func (a *App) InitOIDC(ctx context.Context) error {
	if a.Cfg.OIDCIssuerURL == "" || a.Cfg.OIDCClientID == "" || a.Cfg.OIDCClientSecret == "" {
		return errors.New("OIDC_ISSUER_URL, OIDC_CLIENT_ID and OIDC_CLIENT_SECRET are required when AUTH_MODE=oidc")
	}
	provider, err := oidc.NewProvider(ctx, a.Cfg.OIDCIssuerURL)
	if err != nil {
		return fmt.Errorf("discover provider: %w", err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: a.Cfg.OIDCClientID})
	scopes := strings.Fields(a.Cfg.OIDCScopes)
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	// RP-initiated logout (https://openid.net/specs/openid-connect-rpinitiated-1_0.html)
	// is advertised in the discovery document. Read it best-effort — IdPs that
	// don't expose one simply skip upstream logout later.
	var extra struct {
		EndSessionEndpoint string `json:"end_session_endpoint"`
	}
	_ = provider.Claims(&extra)

	a.OIDC = &oidcRuntime{
		provider: provider,
		verifier: verifier,
		oauth2: &oauth2.Config{
			ClientID:     a.Cfg.OIDCClientID,
			ClientSecret: a.Cfg.OIDCClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  a.Cfg.OIDCRedirectURL,
			Scopes:       scopes,
		},
		endSessionEndpoint: extra.EndSessionEndpoint,
	}
	return nil
}

func randHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

func (a *App) setShortLivedCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/api/auth/oidc",
		HttpOnly: true,
		Secure:   strings.HasPrefix(a.Cfg.PublicBaseURL, "https://"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
	})
}

// setOIDCSessionCookie persists data needed for the logout round-trip (the
// id_token, used as id_token_hint by the IdP). Path mirrors the short-lived
// cookies so it's only sent to /api/auth/oidc endpoints.
func (a *App) setOIDCSessionCookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/api/auth/oidc",
		HttpOnly: true,
		Secure:   strings.HasPrefix(a.Cfg.PublicBaseURL, "https://"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   oidcIDTokenMaxAge,
	})
}

func clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/api/auth/oidc",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

// initiateOIDCFlow generates a nonce, sets the OIDC state/nonce cookies, and
// redirects the browser to the OIDC provider. The state value is caller-supplied
// so that both the normal login path (plain hex) and the OAuth-initiated path
// ("oauth:<key>") can share the same redirect machinery.
func (a *App) initiateOIDCFlow(w http.ResponseWriter, r *http.Request, state string) error {
	nonce, err := randHex(16)
	if err != nil {
		return err
	}
	a.setShortLivedCookie(w, oidcStateCookie, state)
	a.setShortLivedCookie(w, oidcNonceCookie, nonce)
	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce)}
	// UI hint only: when exactly one Workspace domain is configured, ask Google
	// to pre-filter the account chooser. Google only honours a single `hd`, so
	// with multiple configured domains we omit the hint and let the user pick.
	// Backend validation in handleOIDCCallback is the authoritative check.
	if len(a.Cfg.AllowedGoogleWorkspaceDomains) == 1 {
		opts = append(opts, oauth2.SetAuthURLParam("hd", a.Cfg.AllowedGoogleWorkspaceDomains[0]))
	}
	http.Redirect(w, r, a.OIDC.oauth2.AuthCodeURL(state, opts...), http.StatusFound)
	return nil
}

func (a *App) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randHex(16)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "state error")
		return
	}
	if err := a.initiateOIDCFlow(w, r, state); err != nil {
		writeErr(w, http.StatusInternalServerError, "oidc init error")
	}
}

type oidcClaims struct {
	Sub               string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     *bool  `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Nonce             string `json:"nonce"`
	HD                string `json:"hd"` // Google Workspace hosted-domain claim
}

func (a *App) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie(oidcStateCookie)
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		a.oidcFail(w, r, oidcErrProvider)
		return
	}
	nonceCookie, err := r.Cookie(oidcNonceCookie)
	if err != nil || nonceCookie.Value == "" {
		a.oidcFail(w, r, oidcErrProvider)
		return
	}

	// Detect OAuth-initiated flow: load (and atomically consume) the pending
	// OAuth context early so all downstream failures can redirect correctly.
	var pending *oauthPendingRow
	if strings.HasPrefix(stateCookie.Value, "oauth:") {
		stateKey := strings.TrimPrefix(stateCookie.Value, "oauth:")
		pending, err = a.loadAndDeleteOAuthPending(r.Context(), stateKey)
		if err != nil || pending == nil {
			a.oidcFail(w, r, oidcErrProvider)
			return
		}
	}

	// fail routes a reason code to the OAuth client when in an OAuth flow,
	// otherwise to the SPA callback. Policy/identity reasons map to OAuth
	// access_denied; everything else is a server_error.
	fail := func(reason string) {
		if pending != nil {
			metrics.LoginsTotal.WithLabelValues("oidc", "failure").Inc()
			oauthErr := "server_error"
			switch reason {
			case oidcErrDomain, oidcErrAccountReject, oidcErrAccountGone, oidcErrEmailConflict:
				oauthErr = "access_denied"
			}
			oauthRedirectErr(w, r, pending.RedirectURI, pending.OAuthState, oauthErr, reason)
			return
		}
		a.oidcFail(w, r, reason) // increments the oidc failure metric internally
	}

	clearCookie(w, oidcStateCookie)
	clearCookie(w, oidcNonceCookie)

	// The IdP itself reported an error (e.g. the user cancelled consent).
	if errParam := r.URL.Query().Get("error"); errParam != "" {
		log.Printf("INFO: oidc provider returned error=%q", errParam)
		fail(oidcErrProvider)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		fail(oidcErrProvider)
		return
	}

	tok, err := a.OIDC.oauth2.Exchange(r.Context(), code)
	if err != nil {
		fail(oidcErrProvider)
		return
	}
	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		fail(oidcErrProvider)
		return
	}
	idToken, err := a.OIDC.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		fail(oidcErrProvider)
		return
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		fail(oidcErrProvider)
		return
	}
	if claims.Nonce != nonceCookie.Value {
		fail(oidcErrProvider)
		return
	}
	if claims.Sub == "" {
		fail(oidcErrProvider)
		return
	}

	// Per spec: when the Google Workspace allowlist is configured and the `hd`
	// claim is missing/disallowed, return a structured 401 (not the SPA-
	// redirect path used for other failures) and audit-log the rejection.
	// The error message is generic to avoid leaking the configured domains.
	if err := workspaceauth.ValidateGoogleHD(idToken.Issuer, claims.HD, a.Cfg.AllowedGoogleWorkspaceDomains); err != nil {
		// Audit-logged server-side; the user sees a generic "domain not allowed"
		// page that doesn't reveal which domains are configured. Route through
		// fail() so a browser sign-in lands on the SPA (not a raw JSON 401) and
		// an OAuth-initiated flow returns access_denied to the client.
		log.Printf("WARN: oidc workspace domain rejected: hd=%q email=%q sub=%q issuer=%q",
			claims.HD, claims.Email, claims.Sub, idToken.Issuer)
		fail(oidcErrDomain)
		return
	}

	user, err := a.findOrCreateOIDCUser(r.Context(), idToken.Issuer, &claims)
	if err != nil {
		switch {
		case errors.Is(err, errOIDCAccountRejected):
			fail(oidcErrAccountReject)
		case errors.Is(err, errOIDCAccountDeleted):
			fail(oidcErrAccountGone)
		case errors.Is(err, errOIDCEmailConflict):
			fail(oidcErrEmailConflict)
		default:
			log.Printf("ERROR: oidc user provisioning: %v", err)
			fail(oidcErrAccount)
		}
		return
	}

	metrics.LoginsTotal.WithLabelValues("oidc", "success").Inc()

	if pending != nil {
		// OAuth-initiated flow: issue an auth code and redirect back to the
		// OAuth client's redirect_uri instead of forwarding to the SPA.
		// Mirror the password-mode check so a pending/rejected user can't be
		// silently issued a code (the resulting token would fail at the MCP
		// gate, but failing earlier produces a clearer error for the client).
		if user.Status != UserStatusApproved {
			oauthRedirectErr(w, r, pending.RedirectURI, pending.OAuthState, "access_denied", "account "+user.Status)
			return
		}
		authCode, err := a.issueAuthCode(r.Context(), user.ID, pending.RedirectURI, pending.CodeChallenge)
		if err != nil {
			log.Printf("ERROR: issue auth code (oauth/oidc): %v", err)
			oauthRedirectErr(w, r, pending.RedirectURI, pending.OAuthState, "server_error", "auth code issuance failed")
			return
		}
		q := url.Values{"code": {authCode}}
		if pending.OAuthState != "" {
			q.Set("state", pending.OAuthState)
		}
		http.Redirect(w, r, redirectWithParams(pending.RedirectURI, q), http.StatusFound)
		return
	}

	// Normal OIDC flow: issue a session JWT and redirect to the SPA.
	jwt, err := a.issueToken(user.ID, user.TokenVersion)
	if err != nil {
		a.oidcFail(w, r, oidcErrProvider)
		return
	}
	// Stash the raw id_token so we can present it as id_token_hint when the
	// user later logs out. The cookie is scoped to /api/auth/oidc so it
	// never travels with regular API calls.
	a.setOIDCSessionCookie(w, oidcIDTokenCookie, rawIDToken)
	userJSON, _ := json.Marshal(user)
	frag := url.Values{}
	frag.Set("token", jwt)
	frag.Set("user", base64.RawURLEncoding.EncodeToString(userJSON))
	dest := strings.TrimRight(a.Cfg.PublicBaseURL, "/") + "/auth/callback#" + frag.Encode()
	http.Redirect(w, r, dest, http.StatusFound)
}

func (a *App) oidcFail(w http.ResponseWriter, r *http.Request, msg string) {
	metrics.LoginsTotal.WithLabelValues("oidc", "failure").Inc()
	dest := strings.TrimRight(a.Cfg.PublicBaseURL, "/") + "/auth/callback#error=" + url.QueryEscape(msg)
	http.Redirect(w, r, dest, http.StatusFound)
}

// handleOIDCLogout drives RP-initiated logout. The frontend full-page-
// navigates here after clearing its own session state. We:
//  1. Always clear the cached id_token cookie so the next sign-in starts clean.
//  2. If the deployment is in approval-required mode AND the IdP advertised an
//     end_session_endpoint, redirect to it with id_token_hint and a return URL.
//  3. Otherwise (corporate OIDC, no end_session_endpoint, missing cookie, etc.)
//     silently fall back to /login — per spec, callers asked us to fail soft.
func (a *App) handleOIDCLogout(w http.ResponseWriter, r *http.Request) {
	loginURL := strings.TrimRight(a.Cfg.PublicBaseURL, "/") + "/login"

	var idTokenHint string
	if c, err := r.Cookie(oidcIDTokenCookie); err == nil {
		idTokenHint = c.Value
	}
	clearCookie(w, oidcIDTokenCookie)

	if !a.Cfg.RequiresUserApproval() || a.OIDC == nil || a.OIDC.endSessionEndpoint == "" {
		http.Redirect(w, r, loginURL, http.StatusFound)
		return
	}

	u, err := url.Parse(a.OIDC.endSessionEndpoint)
	if err != nil {
		http.Redirect(w, r, loginURL, http.StatusFound)
		return
	}
	q := u.Query()
	q.Set("post_logout_redirect_uri", loginURL)
	q.Set("client_id", a.Cfg.OIDCClientID)
	if idTokenHint != "" {
		q.Set("id_token_hint", idTokenHint)
	}
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}

func (a *App) findOrCreateOIDCUser(ctx context.Context, issuer string, claims *oidcClaims) (*User, error) {
	// 1) match by (issuer, subject) — already linked
	u := &User{}
	var enc sql.NullString
	err := a.DB.QueryRowContext(ctx,
		`SELECT id, email, username, api_token_enc, status, is_admin, created_at, token_version FROM users WHERE oidc_issuer = $1 AND oidc_subject = $2`,
		issuer, claims.Sub,
	).Scan(&u.ID, &u.Email, &u.Username, &enc, &u.Status, &u.IsAdmin, &u.CreatedAt, &u.TokenVersion)
	if err == nil {
		if u.Status == UserStatusRejected {
			return nil, errOIDCAccountRejected
		}
		if u.Status == UserStatusDeleted {
			return nil, errOIDCAccountDeleted
		}
		u.APIToken = a.apiTokenForDisplay(enc)
		return u, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// 2) match by email — link to an existing local account ONLY when the IdP
	//    asserts the email is verified. Linking an unverified email would let
	//    anyone who can set that address at the IdP take over the account, so an
	//    absent OR false email_verified is treated as NOT verified (fail closed).
	//    Google always sets it; generic IdPs must too for auto-linking to work.
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	emailVerified := claims.EmailVerified != nil && *claims.EmailVerified
	if email != "" && emailVerified {
		err = a.DB.QueryRowContext(ctx,
			`SELECT id, email, username, api_token_enc, status, is_admin, created_at, token_version FROM users WHERE email = $1`, email,
		).Scan(&u.ID, &u.Email, &u.Username, &enc, &u.Status, &u.IsAdmin, &u.CreatedAt, &u.TokenVersion)
		if err == nil {
			if u.Status == UserStatusRejected {
				return nil, errOIDCAccountRejected
			}
			if u.Status == UserStatusDeleted {
				return nil, errOIDCAccountDeleted
			}
			u.APIToken = a.apiTokenForDisplay(enc)
			if _, err := a.DB.ExecContext(ctx,
				`UPDATE users SET oidc_issuer = $1, oidc_subject = $2 WHERE id = $3`,
				issuer, claims.Sub, u.ID,
			); err != nil {
				return nil, err
			}
			return u, nil
		}
		if err != sql.ErrNoRows {
			return nil, err
		}
	}

	// 3) create a new user
	if email == "" {
		// fall back to a synthetic email; some providers emit one for service accounts
		email = strings.ToLower(claims.Sub) + "@" + safeIssuerHost(issuer)
	}
	username, err := a.allocateUsername(ctx, claims.PreferredUsername, email)
	if err != nil {
		return nil, err
	}
	apiTok, err := generateAPIToken()
	if err != nil {
		return nil, err
	}
	apiEnc, err := a.encryptAPIToken(apiTok)
	if err != nil {
		return nil, err
	}
	// Status and is_admin are decided in SQL so the empty-DB bootstrap case
	// stays race-safe: the first ever user is always 'approved' AND admin,
	// even if two callbacks arrive simultaneously.
	var (
		id, status string
		isAdmin    bool
	)
	err = a.DB.QueryRowContext(ctx,
		`INSERT INTO users (email, username, oidc_issuer, oidc_subject, api_token_hash, api_token_enc, status, is_admin)
		 VALUES ($1, $2, $3, $4, $5, $6,
		     CASE
		         WHEN $7::boolean AND EXISTS (SELECT 1 FROM users WHERE status = 'approved')
		             THEN 'pending'
		         ELSE 'approved'
		     END,
		     NOT EXISTS (SELECT 1 FROM users))
		 RETURNING id, status, is_admin, created_at`,
		email, username, issuer, claims.Sub, sha256hex(apiTok), apiEnc, a.Cfg.RequiresUserApproval(),
	).Scan(&id, &status, &isAdmin, &u.CreatedAt)
	if err != nil {
		// A unique-violation here means an account with this email (or username)
		// already exists that we deliberately did NOT link — reached only when
		// the IdP didn't assert a verified email (see step 2). Surface it as a
		// distinct, user-facing reason rather than a raw DB error.
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			return nil, errOIDCEmailConflict
		}
		return nil, err
	}
	u.ID = id
	u.Email = email
	u.Username = username
	u.APIToken = apiTok
	u.Status = status
	u.IsAdmin = isAdmin
	return u, nil
}

func safeIssuerHost(issuer string) string {
	u, err := url.Parse(issuer)
	if err != nil || u.Host == "" {
		return "oidc.local"
	}
	return u.Host
}

func (a *App) allocateUsername(ctx context.Context, preferred, email string) (string, error) {
	candidates := []string{preferred}
	if at := strings.IndexByte(email, '@'); at > 0 {
		candidates = append(candidates, email[:at])
	}
	for _, raw := range candidates {
		base := sanitizeUsername(raw)
		if base == "" {
			continue
		}
		if free, err := a.usernameFree(ctx, base); err != nil {
			return "", err
		} else if free {
			return base, nil
		}
		for i := 0; i < 5; i++ {
			suffix, err := randHex(3)
			if err != nil {
				return "", err
			}
			cand := truncate(base, 32-len(suffix)-1) + "-" + suffix
			if free, err := a.usernameFree(ctx, cand); err != nil {
				return "", err
			} else if free {
				return cand, nil
			}
		}
	}
	// last-ditch: fully random username
	rand, err := randHex(8)
	if err != nil {
		return "", err
	}
	return "user-" + rand, nil
}

func (a *App) usernameFree(ctx context.Context, name string) (bool, error) {
	var exists bool
	err := a.DB.QueryRowContext(ctx,
		`SELECT EXISTS(SELECT 1 FROM users WHERE username = $1)`, name,
	).Scan(&exists)
	if err != nil {
		return false, err
	}
	return !exists, nil
}

func sanitizeUsername(s string) string {
	s = strings.TrimSpace(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z',
			r >= 'A' && r <= 'Z',
			r >= '0' && r <= '9',
			r == '_' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_-")
	if len(out) < 3 {
		return ""
	}
	return truncate(out, 32)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
