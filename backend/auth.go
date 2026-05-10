package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"
)

type ctxKey string

const ctxUserKey ctxKey = "user"

var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)
var usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)

func (a *App) issueToken(userID string) (string, error) {
	claims := jwt.MapClaims{
		"sub": userID,
		"iat": time.Now().Unix(),
		"exp": time.Now().Add(30 * 24 * time.Hour).Unix(),
	}
	t := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return t.SignedString([]byte(a.cfg.JWTSecret))
}

func (a *App) parseToken(tok string) (string, error) {
	parsed, err := jwt.Parse(tok, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(a.cfg.JWTSecret), nil
	})
	if err != nil {
		return "", err
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok || !parsed.Valid {
		return "", errors.New("invalid token")
	}
	sub, _ := claims["sub"].(string)
	if sub == "" {
		return "", errors.New("missing sub")
	}
	return sub, nil
}

// authenticateRequest accepts:
//   - Authorization: Bearer <jwt> — JWTs (browser sessions) — recognised by 2 dots
//   - Authorization: Bearer <api_token> — opaque per-user API token
//   - HTTP Basic Auth — password = api token (username ignored)
//
// Returns the resolved user, or an empty error string if no credential was
// presented (so callers can decide between 401 Unauthorized and 401 with
// WWW-Authenticate challenge).
func (a *App) authenticateRequest(r *http.Request) (*User, string) {
	if h := r.Header.Get("Authorization"); h != "" {
		if strings.HasPrefix(h, "Bearer ") {
			tok := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
			if tok == "" {
				return nil, "empty bearer token"
			}
			return a.resolveToken(r.Context(), tok)
		}
		if strings.HasPrefix(h, "Basic ") {
			if _, pass, ok := r.BasicAuth(); ok && pass != "" {
				return a.resolveToken(r.Context(), pass)
			}
			return nil, "invalid basic auth"
		}
	}
	return nil, ""
}

// resolveToken resolves either a JWT or a raw API token to a user.
func (a *App) resolveToken(ctx context.Context, tok string) (*User, string) {
	if strings.Count(tok, ".") == 2 {
		userID, err := a.parseToken(tok)
		if err != nil {
			return nil, "invalid token"
		}
		u, err := a.userByID(ctx, userID)
		if err != nil {
			return nil, "unknown user"
		}
		return u, ""
	}
	u, err := a.userByAPIToken(ctx, tok)
	if err != nil {
		return nil, "invalid token"
	}
	return u, ""
}

func (a *App) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, errMsg := a.authenticateRequest(r)
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

// tokenGateMiddleware authenticates marketplace.json, /git/*, and the read-only
// plugin endpoints. On failure it sends WWW-Authenticate so `git clone` and
// curl prompt for credentials.
func (a *App) tokenGateMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := a.authenticateRequest(r)
		if u == nil {
			w.Header().Set("WWW-Authenticate", `Basic realm="plugin-marketplace"`)
			writeErr(w, http.StatusUnauthorized, "authentication required")
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
		u, errMsg := a.authenticateRequest(r)
		if u == nil {
			w.Header().Set("WWW-Authenticate", `Bearer realm="plugin-marketplace"`)
			if errMsg == "" {
				errMsg = "authentication required"
			}
			writeErr(w, http.StatusUnauthorized, errMsg)
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
	err := a.db.QueryRowContext(ctx,
		`SELECT id, email, username, api_token, created_at FROM users WHERE api_token = $1`, token).
		Scan(&u.ID, &u.Email, &u.Username, &u.APIToken, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
	return u, nil
}

func currentUser(r *http.Request) *User {
	v, _ := r.Context().Value(ctxUserKey).(*User)
	return v
}

func (a *App) userByID(ctx context.Context, id string) (*User, error) {
	u := &User{}
	err := a.db.QueryRowContext(ctx,
		`SELECT id, email, username, api_token, created_at FROM users WHERE id = $1`, id).
		Scan(&u.ID, &u.Email, &u.Username, &u.APIToken, &u.CreatedAt)
	if err != nil {
		return nil, err
	}
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
		writeErr(w, http.StatusInternalServerError, "hash error")
		return
	}

	apiTok, err := generateAPIToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token error")
		return
	}

	var id string
	err = a.db.QueryRowContext(r.Context(),
		`INSERT INTO users (email, username, password_hash, api_token) VALUES ($1, $2, $3, $4) RETURNING id`,
		req.Email, req.Username, string(hash), apiTok).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "email or username already in use")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	tok, err := a.issueToken(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token": tok,
		"user": User{
			ID:       id,
			Email:    req.Email,
			Username: req.Username,
			APIToken: apiTok,
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
		id, username, hash, apiTok string
	)
	err := a.db.QueryRowContext(r.Context(),
		`SELECT id, username, password_hash, api_token FROM users WHERE email = $1`, req.Email).
		Scan(&id, &username, &hash, &apiTok)
	if err != nil {
		loginsTotal.WithLabelValues("password", "failure").Inc()
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	if err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(req.Password)); err != nil {
		loginsTotal.WithLabelValues("password", "failure").Inc()
		writeErr(w, http.StatusUnauthorized, "invalid credentials")
		return
	}
	loginsTotal.WithLabelValues("password", "success").Inc()

	tok, err := a.issueToken(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"token": tok,
		"user": User{
			ID:       id,
			Email:    req.Email,
			Username: username,
			APIToken: apiTok,
		},
	})
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, currentUser(r))
}

func (a *App) handleRegenerateAPIToken(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	newTok, err := generateAPIToken()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "token error")
		return
	}
	if _, err := a.db.ExecContext(r.Context(),
		`UPDATE users SET api_token = $1 WHERE id = $2`, newTok, user.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"apiToken": newTok})
}

type authConfigResp struct {
	Mode            string `json:"mode"`
	MarketplaceName string `json:"marketplaceName"`
	DefaultLicense  string `json:"defaultLicense"`
}

func (a *App) handleAuthConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, authConfigResp{
		Mode:            a.cfg.AuthMode,
		MarketplaceName: a.cfg.MarketplaceName,
		DefaultLicense:  a.cfg.DefaultLicense,
	})
}
