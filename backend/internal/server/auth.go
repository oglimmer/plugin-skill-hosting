package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"marketplace/internal/metrics"
)

// tokenTypeMCPAccess is the "typ" claim stamped on OAuth access tokens issued
// to MCP clients. Session/API JWTs carry no "typ" claim and remain full-access;
// an mcp_access token is accepted only at the /mcp gate (see resolveToken's
// allowMCPScope argument) so a Claude-held OAuth token can't reach regular /api
// routes — in particular it can't regenerate the user's long-lived API token.
const tokenTypeMCPAccess = "mcp_access"

// issueToken mints a 30-day browser session JWT. tokenVersion is the user's
// current revocation counter, stamped as "ver" so the session is rejected the
// moment the counter is bumped (see resolveToken / handleRevokeSessions).
func (a *App) issueToken(userID string, tokenVersion int) (string, error) {
	claims := jwt.MapClaims{
		"sub": userID,
		"ver": tokenVersion,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(a.Cfg.JWTSecret))
}

// issueMCPAccessToken mints a 1-hour OAuth access token tagged with the
// mcp_access purpose claim. Used only by the OAuth token endpoint; the claim
// confines the token to the /mcp gate. It also carries "ver" so a session
// revocation invalidates outstanding MCP access tokens (the client then
// silently refreshes and receives one stamped with the new version).
func (a *App) issueMCPAccessToken(userID string, tokenVersion int) (string, error) {
	claims := jwt.MapClaims{
		"sub": userID,
		"typ": tokenTypeMCPAccess,
		"ver": tokenVersion,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(time.Hour).Unix(),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(a.Cfg.JWTSecret))
}

// parseToken validates a JWT and returns its subject, "typ" claim, and "ver"
// claim. typ is empty for ordinary session/API tokens and "mcp_access" for
// OAuth access tokens. A token minted before "ver" existed decodes as version
// 0, matching the column default so pre-upgrade sessions keep working.
func (a *App) parseToken(tok string) (sub, typ string, ver int, err error) {
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(a.Cfg.JWTSecret), nil
	})
	if err != nil {
		return "", "", 0, err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return "", "", 0, errors.New("invalid token")
	}
	sub, _ = claims["sub"].(string)
	if sub == "" {
		return "", "", 0, errors.New("missing sub")
	}
	typ, _ = claims["typ"].(string)
	// JSON numbers decode to float64 in MapClaims; absent "ver" stays 0.
	if v, ok := claims["ver"].(float64); ok {
		ver = int(v)
	}
	return sub, typ, ver, nil
}

// authenticateRequest accepts:
//   - Authorization: Bearer <jwt> — JWTs (browser sessions) — recognised by 2 dots
//   - Authorization: Bearer <api_token> — opaque per-user API token
//   - HTTP Basic Auth — password = api token (username ignored)
//
// allowMCPScope reports whether an OAuth mcp_access JWT is acceptable here; only
// the /mcp gate passes true. All other gates reject mcp_access tokens so a
// Claude-held OAuth token can't be replayed against regular /api routes.
//
// Returns (user, "", nil) on success; (nil, msg, nil) on credential failure
// (msg is the 401 reason, "" when no credential was presented at all); and
// (nil, "", err) on an unexpected backend error (DB outage, etc.) so callers
// can map that to 500 instead of silently 401-ing legitimate clients.
func (a *App) authenticateRequest(r *http.Request, allowMCPScope bool) (*User, string, error) {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			if tok == "" {
				return nil, "empty bearer token", nil
			}
			return a.resolveToken(r.Context(), tok, allowMCPScope)
		}
		if strings.HasPrefix(h, "Basic ") {
			if _, pass, ok := r.BasicAuth(); ok && pass != "" {
				return a.resolveToken(r.Context(), pass, allowMCPScope)
			}
			return nil, "invalid basic auth", nil
		}
	}
	return nil, "", nil
}

// resolveToken resolves either a JWT or a raw API token to a user. Distinguishes
// "credential is bad" (msg set, err nil) from "DB lookup failed" (msg empty,
// err set) so an intermittent backend hiccup is not reported back to the
// caller as a 401. An OAuth mcp_access JWT is rejected unless allowMCPScope is
// set; the check runs before the DB lookup so a misdirected token never even
// resolves to a user.
func (a *App) resolveToken(ctx context.Context, tok string, allowMCPScope bool) (*User, string, error) {
	if strings.Count(tok, ".") == 2 {
		userID, typ, ver, err := a.parseToken(tok)
		if err != nil {
			return nil, "invalid token", nil
		}
		if typ == tokenTypeMCPAccess && !allowMCPScope {
			return nil, "token not valid for this endpoint", nil
		}
		u, err := a.userByID(ctx, userID)
		if err == sql.ErrNoRows {
			return nil, "unknown user", nil
		}
		if err != nil {
			return nil, "", err
		}
		// Session revocation: a token signed with a stale version (the user hit
		// "sign out everywhere" since it was issued) is no longer valid.
		if ver != u.TokenVersion {
			return nil, "token revoked", nil
		}
		return u, "", nil
	}
	u, err := a.userByAPIToken(ctx, tok)
	if err == sql.ErrNoRows {
		return nil, "invalid token", nil
	}
	if err != nil {
		return nil, "", err
	}
	return u, "", nil
}

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, errMsg, err := a.authenticateRequest(r, false)
		if err != nil {
			serverErr(w, r, err, "auth lookup error")
			return
		}
		if u == nil {
			if errMsg == "" {
				errMsg = "missing bearer token"
			}
			writeErr(w, http.StatusUnauthorized, errMsg)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// requireApprovedMiddleware refuses requests from users whose account is
// pending approval or has been rejected. Must run AFTER authMiddleware (or
// any other middleware that puts a *User into the request context).
//
// /api/me deliberately bypasses this so the frontend can fetch the user's
// own status and route them to the "pending" page.
func (a *App) requireApprovedMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if u.Status != UserStatusApproved {
			writeErr(w, http.StatusForbidden, "account "+u.Status)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requireAdminMiddleware refuses requests from non-admin users. Must run AFTER
// authMiddleware + requireApprovedMiddleware (the admin set is a subset of
// approved users). User-management endpoints are gated to admins; everything
// else stays open to any approved user.
func (a *App) requireAdminMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u := currentUser(r)
		if u == nil {
			writeErr(w, http.StatusUnauthorized, "missing bearer token")
			return
		}
		if !u.IsAdmin {
			writeErr(w, http.StatusForbidden, "admin only")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// tokenGateMiddleware authenticates marketplace.json, /git/*, and the read-only
// plugin endpoints. On failure it sends WWW-Authenticate so `git clone` and
// curl prompt for credentials.
func (a *App) tokenGateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _, err := a.authenticateRequest(r, false)
		if err != nil {
			serverErr(w, r, err, "auth lookup error")
			return
		}
		if u == nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="plugin-marketplace"`)
			writeErr(w, http.StatusUnauthorized, "authentication required")
			return
		}
		if u.Status != UserStatusApproved {
			writeErr(w, http.StatusForbidden, "account "+u.Status)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// mcpTokenGateMiddleware is the /mcp variant: same Bearer/Basic acceptance as
// the regular gate, but the 401 challenge advertises Bearer rather than Basic.
// MCP clients use the WWW-Authenticate scheme to decide their auth UX, and a
// Basic challenge here pushes them into the OAuth-fallback path.
func (a *App) mcpTokenGateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, errMsg, err := a.authenticateRequest(r, true)
		if err != nil {
			serverErr(w, r, err, "auth lookup error")
			return
		}
		if u == nil {
			w.Header().Set("WWW-Authenticate", a.mcpAuthChallenge())
			if errMsg == "" {
				errMsg = "authentication required"
			}
			writeErr(w, http.StatusUnauthorized, errMsg)
			return
		}
		if u.Status != UserStatusApproved {
			writeErr(w, http.StatusForbidden, "account "+u.Status)
			return
		}
		ctx := context.WithValue(r.Context(), ctxUserKey, u)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func generateAPIToken() (string, error) {
	return randHex(32)
}

func (a *App) userByAPIToken(ctx context.Context, token string) (*User, error) {
	u := &User{}
	var enc sql.NullString
	err := a.DB.QueryRowContext(ctx,
		`SELECT id, email, username, api_token_enc, status, is_admin, theme, created_at, token_version FROM users WHERE api_token_hash = $1`, sha256hex(token)).
		Scan(&u.ID, &u.Email, &u.Username, &enc, &u.Status, &u.IsAdmin, &u.Theme, &u.CreatedAt, &u.TokenVersion)
	if err != nil {
		return nil, err
	}
	// The caller presented the plaintext token, so use it directly for display
	// instead of decrypting api_token_enc — cheaper, and works even if the
	// encryption key has since changed.
	u.APIToken = token
	return u, nil
}

func (a *App) userByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	var enc sql.NullString
	err := a.DB.QueryRowContext(ctx,
		`SELECT id, email, username, api_token_enc, status, is_admin, theme, created_at, token_version FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &u.Username, &enc, &u.Status, &u.IsAdmin, &u.Theme, &u.CreatedAt, &u.TokenVersion)
	if err != nil {
		return nil, err
	}
	u.APIToken = a.apiTokenForDisplay(enc)
	return u, nil
}

type registerReq struct {
	Email    string `json:"email"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func (a *App) handleRegister(w http.ResponseWriter, r *http.Request) {
	var req registerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))
	req.Username = strings.TrimSpace(req.Username)
	if !strings.Contains(req.Email, "@") {
		writeErr(w, http.StatusBadRequest, "invalid email")
		return
	}
	if !usernameRe.MatchString(req.Username) {
		writeErr(w, http.StatusBadRequest, "username must be 3-32 chars, alphanumeric/_/-")
		return
	}
	if len(req.Password) < 8 {
		writeErr(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcrypt.DefaultCost)
	if err != nil {
		serverErr(w, r, err, "hash error")
		return
	}

	apiTok, err := generateAPIToken()
	if err != nil {
		serverErr(w, r, err, "token error")
		return
	}
	apiEnc, err := a.encryptAPIToken(apiTok)
	if err != nil {
		serverErr(w, r, err, "token error")
		return
	}

	// is_admin is computed in SQL so the empty-DB bootstrap is decided
	// atomically with the INSERT (matches the OIDC create path's
	// approved-bootstrap pattern). The first ever user lands as admin so the
	// /users page is operable immediately on a fresh deployment.
	var (
		id      string
		isAdmin bool
	)
	err = a.DB.QueryRowContext(r.Context(),
		`INSERT INTO users (email, username, password_hash, api_token_hash, api_token_enc, is_admin)
		 VALUES ($1, $2, $3, $4, $5, NOT EXISTS (SELECT 1 FROM users))
		 RETURNING id, is_admin`,
		req.Email, req.Username, string(hash), sha256hex(apiTok), apiEnc).Scan(&id, &isAdmin)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "email or username already in use")
			return
		}
		serverErr(w, r, err, "db error")
		return
	}

	// A brand-new user starts at token_version 0 (the column default).
	tok, err := a.issueToken(id, 0)
	if err != nil {
		serverErr(w, r, err, "token error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token": tok,
		"user": User{
			ID:       id,
			Email:    req.Email,
			Username: req.Username,
			APIToken: apiTok,
			Status:   UserStatusApproved,
			IsAdmin:  isAdmin,
			Theme:    DefaultTheme,
		},
	})
}

type loginReq struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Email = strings.TrimSpace(strings.ToLower(req.Email))

	var (
		id, username, hash, status, theme string
		isAdmin                           bool
		tokenVersion                      int
		apiEnc                            sql.NullString
	)
	err := a.DB.QueryRowContext(r.Context(),
		`SELECT id, username, password_hash, api_token_enc, status, is_admin, theme, token_version FROM users WHERE email = $1`, req.Email).
		Scan(&id, &username, &hash, &apiEnc, &status, &isAdmin, &theme, &tokenVersion)
	if err == sql.ErrNoRows {
		metrics.LoginsTotal.WithLabelValues("password", "failure").Inc()
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err != nil {
		metrics.LoginsTotal.WithLabelValues("password", "failure").Inc()
		serverErr(w, r, err, "db error")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		metrics.LoginsTotal.WithLabelValues("password", "failure").Inc()
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	// A soft-deleted account no longer exists from the user's perspective.
	// Treat it as if the credentials don't match rather than logging them in
	// (which would route them to /pending — "awaiting approval" — even though
	// they never appear in the admin's waiting-users list).
	if status == UserStatusDeleted {
		metrics.LoginsTotal.WithLabelValues("password", "failure").Inc()
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	metrics.LoginsTotal.WithLabelValues("password", "success").Inc()

	tok, err := a.issueToken(id, tokenVersion)
	if err != nil {
		serverErr(w, r, err, "token error")
		return
	}
	// Decrypt the stored token for display (best-effort; empty if the key has
	// rotated — the user can regenerate). Password login has no plaintext to hand.
	apiTok := ""
	if apiEnc.Valid && apiEnc.String != "" {
		if t, derr := a.decryptAPIToken(apiEnc.String); derr == nil {
			apiTok = t
		}
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token": tok,
		"user": User{
			ID:       id,
			Email:    req.Email,
			Username: username,
			APIToken: apiTok,
			Status:   status,
			IsAdmin:  isAdmin,
			Theme:    theme,
		},
	})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}

type setThemeReq struct {
	Theme string `json:"theme"`
}

// handleSetTheme persists the caller's UI theme preference. It lives outside
// the approval gate (alongside /me) so a pending user can still pick a theme
// while they wait. The allowed set is validated server-side; unknown values are
// rejected rather than stored, so a stale client can't poison the column.
func (a *App) handleSetTheme(w http.ResponseWriter, r *http.Request) {
	var req setThemeReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if !isValidTheme(req.Theme) {
		writeErr(w, http.StatusBadRequest, "unknown theme")
		return
	}
	user := currentUser(r)
	if _, err := a.DB.ExecContext(r.Context(),
		`UPDATE users SET theme = $1 WHERE id = $2`, req.Theme, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"theme": req.Theme})
}

func (a *App) handleRegenerateAPIToken(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	newTok, err := generateAPIToken()
	if err != nil {
		serverErr(w, r, err, "token error")
		return
	}
	newEnc, err := a.encryptAPIToken(newTok)
	if err != nil {
		serverErr(w, r, err, "token error")
		return
	}
	if _, err := a.DB.ExecContext(r.Context(),
		`UPDATE users SET api_token_hash = $1, api_token_enc = $2, api_token = NULL WHERE id = $3`,
		sha256hex(newTok), newEnc, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"apiToken": newTok})
}

// handleRevokeSessions implements "sign out everywhere": it bumps the user's
// token_version, which immediately invalidates every JWT previously issued to
// them — the caller's current session included. The frontend follows up by
// clearing its local token and routing to /login. Outstanding MCP access tokens
// also stop working until the client refreshes (and is re-stamped with the new
// version); the underlying OAuth refresh token is unaffected, so a connected app
// reconnects on its own rather than being permanently disconnected.
func (a *App) handleRevokeSessions(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	if _, err := a.DB.ExecContext(r.Context(),
		`UPDATE users SET token_version = token_version + 1 WHERE id = $1`, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type authConfigResp struct {
	Mode                 string `json:"mode"`
	MarketplaceName      string `json:"marketplaceName"`
	DefaultLicense       string `json:"defaultLicense"`
	UserApprovalRequired bool   `json:"userApprovalRequired"`
	EnterpriseMode       bool   `json:"enterpriseMode"`
}

func (a *App) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, authConfigResp{
		Mode:                 a.Cfg.AuthMode,
		MarketplaceName:      a.Cfg.MarketplaceName,
		DefaultLicense:       a.Cfg.DefaultLicense,
		UserApprovalRequired: a.Cfg.RequiresUserApproval(),
		EnterpriseMode:       a.Cfg.EnterpriseMode,
	})
}
