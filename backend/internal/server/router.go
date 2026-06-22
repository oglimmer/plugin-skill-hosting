package server

import (
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
	"github.com/go-chi/httprate"

	"marketplace/internal/metrics"
)

// NewRouter wires every route the backend exposes onto a chi router. The
// application owner constructs an *App, then passes it here to obtain the
// fully configured http.Handler.
func NewRouter(app *App) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(skipLogger("/healthz", "/readyz"))
	r.Use(middleware.Recoverer)
	r.Use(metrics.HTTPMiddleware)
	r.Use(securityHeaders)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins: app.Cfg.AllowedOrigins,
		AllowedMethods: []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		// Authorization + Content-Type cover the REST API. The Mcp-* and
		// Last-Event-ID request headers are sent by browser-based MCP
		// Streamable HTTP clients (e.g. the MCP Inspector's direct connection);
		// without them the CORS preflight for /mcp fails even when the origin
		// is allowed.
		AllowedHeaders: []string{
			"Authorization", "Content-Type", "Accept",
			"Mcp-Session-Id", "Mcp-Protocol-Version", "Last-Event-ID",
		},
		// Link paginates the REST API; Mcp-Session-Id must be readable by MCP
		// clients so they can carry the session across requests.
		ExposedHeaders:   []string{"Link", "Mcp-Session-Id"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	// Catch-all fallbacks so unmatched routes / wrong methods return a
	// consistent error (JSON for API clients, a friendly HTML page for
	// browsers) instead of chi's bare "404 page not found" plaintext.
	r.NotFound(app.handleNotFound)
	r.MethodNotAllowed(app.handleMethodNotAllowed)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if !app.IsReady() {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte("rematerializing"))
			return
		}
		w.Write([]byte("ok"))
	})

	r.Method("GET", "/metrics", metrics.Handler(app.Cfg.MetricsToken))

	r.Group(func(r chi.Router) {
		r.Use(app.tokenGateMiddleware)
		r.Get("/marketplace.json", app.handleMarketplaceJSON)
		r.Mount("/git", app.gitHandler())
	})

	r.Group(func(r chi.Router) {
		r.Use(app.mcpTokenGateMiddleware)
		r.Mount("/mcp", app.mcpHandler())
	})

	// OAuth 2.1 endpoints — unauthenticated; credential validation is internal.
	// Discovery is cheap JSON hit by clients during setup, left unthrottled.
	r.Get("/.well-known/oauth-authorization-server", app.handleOAuthMeta)
	r.Get("/.well-known/oauth-protected-resource", app.handleOAuthProtectedResource)
	r.Get("/.well-known/oauth-protected-resource/mcp", app.handleOAuthProtectedResource)
	// authorize/token handle auth codes, refresh tokens, and the client secret,
	// so throttle them per client IP. 60/min is far above any real MCP client
	// (a handful of calls per session) but caps runaway/abusive loops. Keyed by
	// the real client IP thanks to the RealIP middleware above. Volumetric DoS
	// stays the edge (ingress/CDN)'s job — this is targeted abuse hardening.
	r.Group(func(r chi.Router) {
		r.Use(httprate.LimitByIP(60, time.Minute))
		r.Get("/oauth/authorize", app.handleOAuthAuthorize)
		r.Post("/oauth/authorize", app.handleOAuthAuthorizeSubmit)
		r.Post("/oauth/token", app.handleOAuthToken)
	})

	r.Route("/api", func(r chi.Router) {
		r.Get("/version", app.handleVersion)
		r.Get("/auth/config", app.handleAuthConfig)

		// Sign-in endpoints (OIDC redirect/callback in production; the dev-only
		// password flow). Per-IP throttle blunts credential-stuffing/abuse;
		// 60/min stays clear of an org's shared-egress-IP login surge.
		r.Group(func(r chi.Router) {
			r.Use(httprate.LimitByIP(60, time.Minute))
			switch app.Cfg.AuthMode {
			case "password":
				r.Post("/auth/register", app.handleRegister)
				r.Post("/auth/login", app.handleLogin)
			case "oidc":
				r.Get("/auth/oidc/login", app.handleOIDCLogin)
				r.Get("/auth/oidc/callback", app.handleOIDCCallback)
				r.Get("/auth/oidc/logout", app.handleOIDCLogout)
			}
		})

		r.Group(func(r chi.Router) {
			r.Use(app.authMiddleware)
			// /api/me stays outside the approval gate so a pending user can
			// read their own status and the SPA can show the right screen.
			r.Get("/me", app.handleMe)
			// Theme preference is cosmetic and likewise allowed pre-approval, so
			// a waiting user can still pick a palette.
			r.Put("/me/theme", app.handleSetTheme)

			r.Group(func(r chi.Router) {
				r.Use(app.requireApprovedMiddleware)
				// Sensitive self-service actions (mint a new API token / revoke
				// all sessions) — a tighter per-IP cap; these are deliberate,
				// infrequent clicks, never hot paths.
				r.Group(func(r chi.Router) {
					r.Use(httprate.LimitByIP(20, time.Minute))
					r.Post("/me/token/regenerate", app.handleRegenerateAPIToken)
					r.Post("/me/sessions/revoke", app.handleRevokeSessions)
				})
				r.Get("/me/deleted-plugins", app.handleListDeletedPlugins)
				r.Group(func(r chi.Router) {
					r.Use(app.requireAdminMiddleware)
					r.Get("/users", app.handleListUsers)
					r.Post("/users/{id}/approve", app.handleApproveUser)
					r.Post("/users/{id}/reject", app.handleRejectUser)
					r.Post("/users/{id}/promote", app.handlePromoteUser)
					r.Post("/users/{id}/demote", app.handleDemoteUser)
					r.Delete("/users/{id}", app.handleDeleteUser)
					// One-shot bootstrap: push every DB plugin to the external
					// git repo. Use when enabling external sync on a populated DB.
					r.Post("/external-git/sync-out", app.handleAdminSyncOut)
					// Read-only drift check + targeted reconcile of the external mirror.
					r.Get("/external-git/status", app.handleAdminSyncStatus)
					r.Post("/external-git/reconcile", app.handleAdminSyncReconcile)
					r.Get("/audit/results", app.handleListAuditResults)
					r.Post("/audit/run", app.handleRunAudit)
				})
				r.Get("/plugins", app.handleListPlugins)
				r.Get("/plugins/{name}", app.handleGetPlugin)
				r.Post("/plugins", app.handleCreatePlugin)
				r.Put("/plugins/{name}", app.handleUpdatePlugin)
				r.Delete("/plugins/{name}", app.handleDeletePlugin)
				r.Post("/plugins/{name}/restore", app.handleRestorePlugin)
				r.Post("/plugins/{name}/skills", app.handleCreateSkill)
				r.Post("/plugins/{name}/skills/import", app.handleImportSkill)
				r.Put("/plugins/{name}/skills/{skill}", app.handleUpdateSkill)
				r.Delete("/plugins/{name}/skills/{skill}", app.handleDeleteSkill)
				r.Post("/plugins/{name}/skills/{skill}/move", app.handleMoveSkill)
				// Lock / unlock are admin-only: only an admin can withdraw a skill
				// from git/MCP or release one the audit auto-locked.
				r.With(app.requireAdminMiddleware).
					Post("/plugins/{name}/skills/{skill}/lock", app.handleLockSkill)
				r.With(app.requireAdminMiddleware).
					Delete("/plugins/{name}/skills/{skill}/lock", app.handleUnlockSkill)
				r.Get("/plugins/{name}/deleted-skills", app.handleListDeletedSkills)
				r.Post("/plugins/{name}/skills/{skill}/restore", app.handleRestoreSkill)
				r.Get("/plugins/{name}/skills/{skill}/versions", app.handleListSkillVersions)
				r.Post("/plugins/{name}/skills/{skill}/revert/{version}", app.handleRevertSkill)
				r.Get("/plugins/{name}/skills/{skill}/files", app.handleListSkillFiles)
				r.Get("/plugins/{name}/skills/{skill}/files/*", app.handleGetSkillFile)
				r.Put("/plugins/{name}/skills/{skill}/files/*", app.handleUpsertSkillFile)
				r.Delete("/plugins/{name}/skills/{skill}/files/*", app.handleDeleteSkillFile)
				r.Post("/skills/validate", app.handleValidateSkill)
				r.Post("/skills/finding-fix", app.handleFixFinding)
			})
		})
	})

	return r
}

// securityHeaders adds defense-in-depth response headers to every backend
// response. In Compose the front nginx already sets richer headers on the SPA
// document; this covers the production path where the Ingress routes /api,
// /oauth, /git and /mcp straight to the backend (bypassing nginx). The CSP is
// deliberately strict — the backend only ever emits JSON or self-contained HTML
// (the OAuth login form and error pages), which load no scripts; inline styles
// are allowed for those pages, and form submissions are confined to same-origin.
// HSTS is intentionally left to the TLS edge (nginx/Ingress).
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "strict-origin-when-cross-origin")
		h.Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; form-action 'self'; base-uri 'none'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// skipLogger wraps chi's request logger so health/readiness probes don't spam logs.
func skipLogger(skipPaths ...string) func(http.Handler) http.Handler {
	skip := make(map[string]struct{}, len(skipPaths))
	for _, p := range skipPaths {
		skip[p] = struct{}{}
	}
	logger := middleware.Logger
	return func(next http.Handler) http.Handler {
		logged := logger(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if _, ok := skip[r.URL.Path]; ok {
				next.ServeHTTP(w, r)
				return
			}
			logged.ServeHTTP(w, r)
		})
	}
}
