package server

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"

	"marketplace/internal/metrics"
)

// NewRouter wires every route the backend exposes onto a chi router. The
// application owner constructs an *App, then passes it here to obtain the
// fully configured http.Handler.
func NewRouter(app *App) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(metrics.HTTPMiddleware)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
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

	r.Route("/api", func(r chi.Router) {
		r.Get("/version", app.handleVersion)
		r.Get("/auth/config", app.handleAuthConfig)

		switch app.Cfg.AuthMode {
		case "password":
			r.Post("/auth/register", app.handleRegister)
			r.Post("/auth/login", app.handleLogin)
		case "oidc":
			r.Get("/auth/oidc/login", app.handleOIDCLogin)
			r.Get("/auth/oidc/callback", app.handleOIDCCallback)
			r.Get("/auth/oidc/logout", app.handleOIDCLogout)
		}

		r.Group(func(r chi.Router) {
			r.Use(app.authMiddleware)
			// /api/me stays outside the approval gate so a pending user can
			// read their own status and the SPA can show the right screen.
			r.Get("/me", app.handleMe)

			r.Group(func(r chi.Router) {
				r.Use(app.requireApprovedMiddleware)
				r.Post("/me/token/regenerate", app.handleRegenerateAPIToken)
				r.Get("/me/deleted-plugins", app.handleListDeletedPlugins)
				r.Get("/users", app.handleListUsers)
				r.Post("/users/{id}/approve", app.handleApproveUser)
				r.Post("/users/{id}/reject", app.handleRejectUser)
				r.Get("/plugins", app.handleListPlugins)
				r.Get("/plugins/{name}", app.handleGetPlugin)
				r.Post("/plugins", app.handleCreatePlugin)
				r.Delete("/plugins/{name}", app.handleDeletePlugin)
				r.Post("/plugins/{name}/restore", app.handleRestorePlugin)
				r.Post("/plugins/{name}/skills", app.handleCreateSkill)
				r.Put("/plugins/{name}/skills/{skill}", app.handleUpdateSkill)
				r.Delete("/plugins/{name}/skills/{skill}", app.handleDeleteSkill)
				r.Get("/plugins/{name}/deleted-skills", app.handleListDeletedSkills)
				r.Post("/plugins/{name}/skills/{skill}/restore", app.handleRestoreSkill)
				r.Get("/plugins/{name}/skills/{skill}/versions", app.handleListSkillVersions)
				r.Post("/plugins/{name}/skills/{skill}/revert/{version}", app.handleRevertSkill)
				r.Get("/plugins/{name}/skills/{skill}/files", app.handleListSkillFiles)
				r.Get("/plugins/{name}/skills/{skill}/files/*", app.handleGetSkillFile)
				r.Put("/plugins/{name}/skills/{skill}/files/*", app.handleUpsertSkillFile)
				r.Delete("/plugins/{name}/skills/{skill}/files/*", app.handleDeleteSkillFile)
				r.Post("/skills/validate", app.handleValidateSkill)
			})
		})
	})

	return r
}
