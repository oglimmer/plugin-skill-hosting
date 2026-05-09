package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/coreos/go-oidc/v3/oidc"
	"golang.org/x/oauth2"
)

type OIDCRuntime struct {
	provider *oidc.Provider
	verifier *oidc.IDTokenVerifier
	oauth2   *oauth2.Config
}

const (
	oidcStateCookie = "oidc_state"
	oidcNonceCookie = "oidc_nonce"
)

func (a *App) initOIDC(ctx context.Context) error {
	if a.cfg.OIDCIssuerURL == "" || a.cfg.OIDCClientID == "" || a.cfg.OIDCClientSecret == "" {
		return errors.New("OIDC_ISSUER_URL, OIDC_CLIENT_ID and OIDC_CLIENT_SECRET are required when AUTH_MODE=oidc")
	}
	provider, err := oidc.NewProvider(ctx, a.cfg.OIDCIssuerURL)
	if err != nil {
		return fmt.Errorf("discover provider: %w", err)
	}
	verifier := provider.Verifier(&oidc.Config{ClientID: a.cfg.OIDCClientID})
	scopes := strings.Fields(a.cfg.OIDCScopes)
	if len(scopes) == 0 {
		scopes = []string{oidc.ScopeOpenID, "email", "profile"}
	}
	a.oidc = &OIDCRuntime{
		provider: provider,
		verifier: verifier,
		oauth2: &oauth2.Config{
			ClientID:     a.cfg.OIDCClientID,
			ClientSecret: a.cfg.OIDCClientSecret,
			Endpoint:     provider.Endpoint(),
			RedirectURL:  a.cfg.OIDCRedirectURL,
			Scopes:       scopes,
		},
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
		Secure:   strings.HasPrefix(a.cfg.PublicBaseURL, "https://"),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   600,
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

func (a *App) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	state, err := randHex(16)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "state error")
		return
	}
	nonce, err := randHex(16)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "nonce error")
		return
	}
	a.setShortLivedCookie(w, oidcStateCookie, state)
	a.setShortLivedCookie(w, oidcNonceCookie, nonce)
	authURL := a.oidc.oauth2.AuthCodeURL(state, oidc.Nonce(nonce))
	http.Redirect(w, r, authURL, http.StatusFound)
}

type oidcClaims struct {
	Sub               string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     *bool  `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
	Nonce             string `json:"nonce"`
}

func (a *App) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie(oidcStateCookie)
	if err != nil || stateCookie.Value == "" || stateCookie.Value != r.URL.Query().Get("state") {
		a.oidcFail(w, r, "state mismatch")
		return
	}
	nonceCookie, err := r.Cookie(oidcNonceCookie)
	if err != nil || nonceCookie.Value == "" {
		a.oidcFail(w, r, "missing nonce")
		return
	}
	clearCookie(w, oidcStateCookie)
	clearCookie(w, oidcNonceCookie)

	if errParam := r.URL.Query().Get("error"); errParam != "" {
		a.oidcFail(w, r, errParam)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" {
		a.oidcFail(w, r, "missing code")
		return
	}

	tok, err := a.oidc.oauth2.Exchange(r.Context(), code)
	if err != nil {
		a.oidcFail(w, r, "token exchange failed")
		return
	}
	rawIDToken, ok := tok.Extra("id_token").(string)
	if !ok || rawIDToken == "" {
		a.oidcFail(w, r, "missing id_token")
		return
	}
	idToken, err := a.oidc.verifier.Verify(r.Context(), rawIDToken)
	if err != nil {
		a.oidcFail(w, r, "id_token verify failed")
		return
	}
	var claims oidcClaims
	if err := idToken.Claims(&claims); err != nil {
		a.oidcFail(w, r, "claims parse failed")
		return
	}
	if claims.Nonce != nonceCookie.Value {
		a.oidcFail(w, r, "nonce mismatch")
		return
	}
	if claims.Sub == "" {
		a.oidcFail(w, r, "missing sub claim")
		return
	}

	user, err := a.findOrCreateOIDCUser(r.Context(), idToken.Issuer, &claims)
	if err != nil {
		a.oidcFail(w, r, "user provisioning failed: "+err.Error())
		return
	}

	jwt, err := a.issueToken(user.ID)
	if err != nil {
		a.oidcFail(w, r, "token issue failed")
		return
	}

	userJSON, _ := json.Marshal(user)
	frag := url.Values{}
	frag.Set("token", jwt)
	frag.Set("user", base64.RawURLEncoding.EncodeToString(userJSON))
	dest := strings.TrimRight(a.cfg.PublicBaseURL, "/") + "/auth/callback#" + frag.Encode()
	http.Redirect(w, r, dest, http.StatusFound)
}

func (a *App) oidcFail(w http.ResponseWriter, r *http.Request, msg string) {
	dest := strings.TrimRight(a.cfg.PublicBaseURL, "/") + "/auth/callback#error=" + url.QueryEscape(msg)
	http.Redirect(w, r, dest, http.StatusFound)
}

func (a *App) findOrCreateOIDCUser(ctx context.Context, issuer string, claims *oidcClaims) (*User, error) {
	// 1) match by (issuer, subject) — already linked
	u := &User{}
	err := a.db.QueryRowContext(ctx,
		`SELECT id, email, username, created_at FROM users WHERE oidc_issuer = $1 AND oidc_subject = $2`,
		issuer, claims.Sub,
	).Scan(&u.ID, &u.Email, &u.Username, &u.CreatedAt)
	if err == nil {
		return u, nil
	}
	if err != sql.ErrNoRows {
		return nil, err
	}

	// 2) match by email — link to existing account if email_verified (or claim absent)
	email := strings.ToLower(strings.TrimSpace(claims.Email))
	if email != "" && (claims.EmailVerified == nil || *claims.EmailVerified) {
		err = a.db.QueryRowContext(ctx,
			`SELECT id, email, username, created_at FROM users WHERE email = $1`, email,
		).Scan(&u.ID, &u.Email, &u.Username, &u.CreatedAt)
		if err == nil {
			if _, err := a.db.ExecContext(ctx,
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
	var id string
	err = a.db.QueryRowContext(ctx,
		`INSERT INTO users (email, username, oidc_issuer, oidc_subject)
		 VALUES ($1, $2, $3, $4) RETURNING id, created_at`,
		email, username, issuer, claims.Sub,
	).Scan(&id, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	u.ID = id
	u.Email = email
	u.Username = username
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
	err := a.db.QueryRowContext(ctx,
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
