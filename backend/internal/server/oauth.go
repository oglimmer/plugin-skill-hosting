package server

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

	"marketplace/internal/metrics"
)

// loginFormTmpl is rendered by GET /oauth/authorize in password mode.
var loginFormTmpl = template.Must(template.New("oauth-login").Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Sign in</title>
<style>
*{box-sizing:border-box}
body{font-family:system-ui,sans-serif;display:flex;align-items:center;justify-content:center;min-height:100vh;margin:0;background:#f3f4f6}
.card{background:#fff;border-radius:10px;box-shadow:0 2px 12px rgba(0,0,0,.1);padding:2.25rem;width:100%;max-width:380px}
h1{font-size:1.15rem;margin:0 0 1.5rem;font-weight:600;color:#111}
label{display:block;font-size:.85rem;font-weight:500;margin-bottom:.3rem;color:#374151}
input{width:100%;border:1px solid #d1d5db;border-radius:6px;padding:.5rem .75rem;font-size:1rem;margin-bottom:1rem;background:#fafafa}
input:focus{outline:none;border-color:#6366f1;box-shadow:0 0 0 3px rgba(99,102,241,.15);background:#fff}
button{width:100%;background:#6366f1;color:#fff;border:none;border-radius:6px;padding:.65rem;font-size:1rem;font-weight:500;cursor:pointer;margin-top:.25rem}
button:hover{background:#4f46e5}
.err{background:#fee2e2;color:#b91c1c;border-radius:6px;padding:.6rem .75rem;font-size:.875rem;margin-bottom:1.25rem}
</style>
</head>
<body>
<div class="card">
<h1>Sign in to continue</h1>
{{if .Error}}<div class="err">{{.Error}}</div>{{end}}
<form method="POST" action="/oauth/authorize">
<input type="hidden" name="client_id"            value="{{.ClientID}}">
<input type="hidden" name="redirect_uri"          value="{{.RedirectURI}}">
<input type="hidden" name="state"                 value="{{.State}}">
<input type="hidden" name="code_challenge"        value="{{.CodeChallenge}}">
<input type="hidden" name="code_challenge_method" value="{{.CodeChallengeMethod}}">
<label for="email">Email</label>
<input type="email" id="email" name="email" required autofocus autocomplete="email">
<label for="password">Password</label>
<input type="password" id="password" name="password" required autocomplete="current-password">
<button type="submit">Sign in</button>
</form>
</div>
</body>
</html>`))

type loginFormData struct {
	Error               string
	ClientID            string
	RedirectURI         string
	State               string
	CodeChallenge       string
	CodeChallengeMethod string
}

// oauthPendingRow is the payload read back from the oauth_pending table when an
// OIDC-initiated OAuth flow completes its OIDC leg.
type oauthPendingRow struct {
	RedirectURI   string
	CodeChallenge string
	OAuthState    string
}

// handleOAuthMeta serves GET /.well-known/oauth-authorization-server (RFC 8414).
func (a *App) handleOAuthMeta(w http.ResponseWriter, r *http.Request) {
	if a.Cfg.MCPOAuthClientID == "" {
		http.NotFound(w, r)
		return
	}
	base := strings.TrimRight(a.Cfg.PublicBaseURL, "/")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"issuer":                                base,
		"authorization_endpoint":                base + "/oauth/authorize",
		"token_endpoint":                        base + "/oauth/token",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token"},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"client_secret_basic", "client_secret_post"},
	})
}

// handleOAuthAuthorize serves GET /oauth/authorize.
// In password mode it renders an HTML login form.
// In OIDC mode it stores the OAuth params in oauth_pending and redirects to the
// configured OIDC provider; the OIDC callback resumes the flow on return.
func (a *App) handleOAuthAuthorize(w http.ResponseWriter, r *http.Request) {
	if a.Cfg.MCPOAuthClientID == "" {
		http.NotFound(w, r)
		return
	}

	q := r.URL.Query()
	clientID := q.Get("client_id")
	redirectURI := q.Get("redirect_uri")
	state := q.Get("state")
	codeChallenge := q.Get("code_challenge")
	codeChallengeMethod := q.Get("code_challenge_method")
	responseType := q.Get("response_type")

	// Validate client_id and redirect_uri before redirecting — prevents open-redirect.
	if clientID != a.Cfg.MCPOAuthClientID {
		writeErr(w, http.StatusBadRequest, "invalid client_id")
		return
	}
	if !a.validRedirectURI(redirectURI) {
		writeErr(w, http.StatusBadRequest, "invalid redirect_uri")
		return
	}
	// From here failures redirect to the client with an error parameter.
	if responseType != "code" {
		oauthRedirectErr(w, r, redirectURI, state, "unsupported_response_type", "only code is supported")
		return
	}
	if codeChallenge == "" {
		oauthRedirectErr(w, r, redirectURI, state, "invalid_request", "code_challenge required")
		return
	}
	if codeChallengeMethod != "S256" {
		oauthRedirectErr(w, r, redirectURI, state, "invalid_request", "only S256 is supported")
		return
	}

	if a.Cfg.AuthMode == "oidc" {
		if a.OIDC == nil {
			oauthRedirectErr(w, r, redirectURI, state, "server_error", "oidc not initialised")
			return
		}
		stateKey, err := randHex(16)
		if err != nil {
			oauthRedirectErr(w, r, redirectURI, state, "server_error", "state generation failed")
			return
		}
		if _, err := a.DB.ExecContext(r.Context(),
			`INSERT INTO oauth_pending (state_key, redirect_uri, code_challenge, oauth_state, expires_at)
			 VALUES ($1, $2, $3, $4, $5)`,
			stateKey, redirectURI, codeChallenge, state, time.Now().Add(10*time.Minute),
		); err != nil {
			log.Printf("ERROR: oauth_pending insert: %v", err)
			oauthRedirectErr(w, r, redirectURI, state, "server_error", "internal error")
			return
		}
		if err := a.initiateOIDCFlow(w, r, "oauth:"+stateKey); err != nil {
			oauthRedirectErr(w, r, redirectURI, state, "server_error", "oidc init failed")
		}
		return
	}

	// Password mode: render the login form.
	renderLoginForm(w, http.StatusOK, loginFormData{
		ClientID:            clientID,
		RedirectURI:         redirectURI,
		State:               state,
		CodeChallenge:       codeChallenge,
		CodeChallengeMethod: codeChallengeMethod,
	})
}

// handleOAuthAuthorizeSubmit serves POST /oauth/authorize (password mode only).
// The login form posts here; on success it redirects to the OAuth client's
// redirect_uri with the authorization code.
func (a *App) handleOAuthAuthorizeSubmit(w http.ResponseWriter, r *http.Request) {
	if a.Cfg.MCPOAuthClientID == "" {
		http.NotFound(w, r)
		return
	}
	if a.Cfg.AuthMode != "password" {
		writeErr(w, http.StatusBadRequest, "not supported in oidc mode")
		return
	}
	if err := r.ParseForm(); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid form data")
		return
	}

	clientID := r.FormValue("client_id")
	redirectURI := r.FormValue("redirect_uri")
	state := r.FormValue("state")
	codeChallenge := r.FormValue("code_challenge")
	codeChallengeMethod := r.FormValue("code_challenge_method")

	// Re-validate all OAuth params — hidden fields can be tampered with.
	if clientID != a.Cfg.MCPOAuthClientID || !a.validRedirectURI(redirectURI) ||
		codeChallenge == "" || codeChallengeMethod != "S256" {
		writeErr(w, http.StatusBadRequest, "invalid oauth parameters")
		return
	}

	rerender := func(errMsg string) {
		renderLoginForm(w, http.StatusUnauthorized, loginFormData{
			Error:               errMsg,
			ClientID:            clientID,
			RedirectURI:         redirectURI,
			State:               state,
			CodeChallenge:       codeChallenge,
			CodeChallengeMethod: codeChallengeMethod,
		})
	}

	email := strings.TrimSpace(strings.ToLower(r.FormValue("email")))
	password := r.FormValue("password")
	if email == "" || password == "" {
		rerender("Email and password are required.")
		return
	}

	var id, hash, userStatus string
	err := a.DB.QueryRowContext(r.Context(),
		`SELECT id, password_hash, status FROM users WHERE email = $1`, email,
	).Scan(&id, &hash, &userStatus)
	if err == sql.ErrNoRows {
		metrics.LoginsTotal.WithLabelValues("password", "failure").Inc()
		rerender("Invalid email or password.")
		return
	}
	if err != nil {
		log.Printf("ERROR: oauth authorize db: %v", err)
		rerender("An error occurred. Please try again.")
		return
	}
	if bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		metrics.LoginsTotal.WithLabelValues("password", "failure").Inc()
		rerender("Invalid email or password.")
		return
	}
	if userStatus != UserStatusApproved {
		rerender("Your account is not yet approved.")
		return
	}

	metrics.LoginsTotal.WithLabelValues("password", "success").Inc()

	authCode, err := a.issueAuthCode(r.Context(), id, redirectURI, codeChallenge)
	if err != nil {
		log.Printf("ERROR: issue auth code: %v", err)
		rerender("An error occurred. Please try again.")
		return
	}
	dest := redirectURI + "?" + url.Values{"code": {authCode}, "state": {state}}.Encode()
	http.Redirect(w, r, dest, http.StatusFound)
}

// handleOAuthToken serves POST /oauth/token.
// Supports grant_type=authorization_code (code exchange) and refresh_token.
func (a *App) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	if a.Cfg.MCPOAuthClientID == "" {
		http.NotFound(w, r)
		return
	}
	// RFC 6749 §5.1: token endpoint responses (success and error) must not be
	// cached. Set once here so every downstream writer inherits the header.
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if err := r.ParseForm(); err != nil {
		oauthTokenErr(w, http.StatusBadRequest, "invalid_request", "cannot parse request body")
		return
	}
	if !a.validateOAuthClient(r) {
		w.Header().Set("WWW-Authenticate", `Basic realm="oauth"`)
		oauthTokenErr(w, http.StatusUnauthorized, "invalid_client", "invalid client credentials")
		return
	}

	switch r.FormValue("grant_type") {
	case "authorization_code":
		a.handleCodeExchange(w, r)
	case "refresh_token":
		a.handleRefreshToken(w, r)
	default:
		oauthTokenErr(w, http.StatusBadRequest, "unsupported_grant_type",
			"supported grant types: authorization_code, refresh_token")
	}
}

func (a *App) handleCodeExchange(w http.ResponseWriter, r *http.Request) {
	code := r.FormValue("code")
	codeVerifier := r.FormValue("code_verifier")
	redirectURI := r.FormValue("redirect_uri")

	if code == "" || codeVerifier == "" || redirectURI == "" {
		oauthTokenErr(w, http.StatusBadRequest, "invalid_request",
			"code, code_verifier, and redirect_uri are required")
		return
	}

	var userID, storedChallenge, storedRedirect string
	err := a.DB.QueryRowContext(r.Context(),
		`DELETE FROM oauth_auth_codes
		 WHERE code_hash = $1 AND expires_at > now()
		 RETURNING user_id, code_challenge, redirect_uri`,
		sha256hex(code),
	).Scan(&userID, &storedChallenge, &storedRedirect)
	if err == sql.ErrNoRows {
		oauthTokenErr(w, http.StatusBadRequest, "invalid_grant", "code not found or expired")
		return
	}
	if err != nil {
		log.Printf("ERROR: code exchange db: %v", err)
		oauthTokenErr(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	if storedRedirect != redirectURI {
		oauthTokenErr(w, http.StatusBadRequest, "invalid_grant", "redirect_uri mismatch")
		return
	}
	if !verifyCodeChallenge(codeVerifier, storedChallenge) {
		oauthTokenErr(w, http.StatusBadRequest, "invalid_grant", "code_verifier mismatch")
		return
	}

	pair, err := a.issueOAuthTokenPair(r.Context(), userID)
	if err != nil {
		log.Printf("ERROR: issue token pair: %v", err)
		oauthTokenErr(w, http.StatusInternalServerError, "server_error", "token issuance failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token":  pair.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": pair.RefreshToken,
	})
}

func (a *App) handleRefreshToken(w http.ResponseWriter, r *http.Request) {
	rawToken := r.FormValue("refresh_token")
	if rawToken == "" {
		oauthTokenErr(w, http.StatusBadRequest, "invalid_request", "refresh_token required")
		return
	}

	// Rotate: atomically delete the old token and read the user_id + status.
	// The join enforces that the user still exists and lets us reject suspended
	// accounts at refresh time instead of waiting for the access token to be
	// blocked at the MCP gate.
	var userID, userStatus string
	err := a.DB.QueryRowContext(r.Context(),
		`DELETE FROM oauth_refresh_tokens t
		 USING users u
		 WHERE t.token_hash = $1 AND t.expires_at > now()
		   AND u.id = t.user_id
		 RETURNING t.user_id, u.status`,
		sha256hex(rawToken),
	).Scan(&userID, &userStatus)
	if err == sql.ErrNoRows {
		oauthTokenErr(w, http.StatusBadRequest, "invalid_grant", "refresh_token not found or expired")
		return
	}
	if err != nil {
		log.Printf("ERROR: refresh token db: %v", err)
		oauthTokenErr(w, http.StatusInternalServerError, "server_error", "internal error")
		return
	}
	if userStatus != UserStatusApproved {
		oauthTokenErr(w, http.StatusBadRequest, "invalid_grant", "account "+userStatus)
		return
	}

	pair, err := a.issueOAuthTokenPair(r.Context(), userID)
	if err != nil {
		log.Printf("ERROR: issue token pair (refresh): %v", err)
		oauthTokenErr(w, http.StatusInternalServerError, "server_error", "token issuance failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"access_token":  pair.AccessToken,
		"token_type":    "Bearer",
		"expires_in":    3600,
		"refresh_token": pair.RefreshToken,
	})
}

// --- helpers -----------------------------------------------------------------

type oauthTokenPair struct {
	AccessToken  string
	RefreshToken string
}

func (a *App) issueOAuthTokenPair(ctx context.Context, userID string) (*oauthTokenPair, error) {
	accessToken, err := a.issueShortToken(userID, time.Hour)
	if err != nil {
		return nil, fmt.Errorf("access token: %w", err)
	}
	rawRefresh, err := randHex(32)
	if err != nil {
		return nil, fmt.Errorf("refresh token rand: %w", err)
	}
	if _, err := a.DB.ExecContext(ctx,
		`INSERT INTO oauth_refresh_tokens (token_hash, user_id, expires_at)
		 VALUES ($1, $2, $3)`,
		sha256hex(rawRefresh), userID, time.Now().Add(30*24*time.Hour),
	); err != nil {
		return nil, fmt.Errorf("refresh token store: %w", err)
	}
	return &oauthTokenPair{AccessToken: accessToken, RefreshToken: rawRefresh}, nil
}

func (a *App) issueAuthCode(ctx context.Context, userID, redirectURI, codeChallenge string) (string, error) {
	raw, err := randHex(32)
	if err != nil {
		return "", err
	}
	if _, err := a.DB.ExecContext(ctx,
		`INSERT INTO oauth_auth_codes (code_hash, user_id, redirect_uri, code_challenge, expires_at)
		 VALUES ($1, $2, $3, $4, $5)`,
		sha256hex(raw), userID, redirectURI, codeChallenge, time.Now().Add(10*time.Minute),
	); err != nil {
		return "", err
	}
	return raw, nil
}

// loadAndDeleteOAuthPending atomically reads and removes the pending OAuth
// context stored while the user was going through the OIDC leg.
// Returns (nil, nil) when the row is not found or has expired.
func (a *App) loadAndDeleteOAuthPending(ctx context.Context, stateKey string) (*oauthPendingRow, error) {
	row := &oauthPendingRow{}
	err := a.DB.QueryRowContext(ctx,
		`DELETE FROM oauth_pending
		 WHERE state_key = $1 AND expires_at > now()
		 RETURNING redirect_uri, code_challenge, oauth_state`,
		stateKey,
	).Scan(&row.RedirectURI, &row.CodeChallenge, &row.OAuthState)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return row, nil
}

// StartOAuthGC launches a background goroutine that periodically deletes
// expired rows from the OAuth tables. The happy path already deletes rows on
// successful exchange/rotation, but abandoned flows (user closes the tab,
// client crashes) leave dead rows that the `expires_at > now()` guards keep
// out of use but never remove. No-op when OAuth is not configured.
func (a *App) StartOAuthGC(ctx context.Context) {
	if a.Cfg.MCPOAuthClientID == "" {
		return
	}
	go func() {
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		for {
			a.gcExpiredOAuthRows(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (a *App) gcExpiredOAuthRows(ctx context.Context) {
	for _, table := range []string{"oauth_pending", "oauth_auth_codes", "oauth_refresh_tokens"} {
		if _, err := a.DB.ExecContext(ctx,
			`DELETE FROM `+table+` WHERE expires_at < now()`,
		); err != nil {
			log.Printf("ERROR: oauth gc %s: %v", table, err)
		}
	}
}

// validateOAuthClient checks the client credentials supplied in the request
// (HTTP Basic auth or form body params) against the deployment config.
func (a *App) validateOAuthClient(r *http.Request) bool {
	if a.Cfg.MCPOAuthClientID == "" {
		return false
	}
	var id, secret string
	if cid, csec, ok := r.BasicAuth(); ok {
		id, secret = cid, csec
	} else {
		id, secret = r.FormValue("client_id"), r.FormValue("client_secret")
	}
	return id == a.Cfg.MCPOAuthClientID && secret == a.Cfg.MCPOAuthClientSecret
}

func (a *App) validRedirectURI(uri string) bool {
	for _, allowed := range a.Cfg.MCPOAuthRedirectURIs {
		if uri == allowed {
			return true
		}
	}
	return false
}

// verifyCodeChallenge checks that BASE64URL(SHA256(verifier)) == challenge.
func verifyCodeChallenge(verifier, challenge string) bool {
	h := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(h[:]) == challenge
}

func sha256hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func oauthTokenErr(w http.ResponseWriter, status int, code, description string) {
	writeJSON(w, status, map[string]string{
		"error":             code,
		"error_description": description,
	})
}

// oauthRedirectErr redirects to the OAuth client's redirect_uri with standard
// error query parameters. Used when the redirect_uri is already known-valid.
func oauthRedirectErr(w http.ResponseWriter, r *http.Request, redirectURI, state, code, description string) {
	q := url.Values{"error": {code}, "error_description": {description}}
	if state != "" {
		q.Set("state", state)
	}
	http.Redirect(w, r, redirectURI+"?"+q.Encode(), http.StatusFound)
}

func renderLoginForm(w http.ResponseWriter, status int, data loginFormData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := loginFormTmpl.Execute(w, data); err != nil {
		log.Printf("ERROR: login form template: %v", err)
	}
}
