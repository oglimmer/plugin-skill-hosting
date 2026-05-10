package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

type Config struct {
	DatabaseURL   string
	JWTSecret     string
	ListenAddr    string
	DataDir       string
	PublicBaseURL string

	MarketplaceName string
	DefaultLicense  string

	AuthMode string // "password" (default) or "oidc"

	OIDCIssuerURL    string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string // defaults to PublicBaseURL + "/auth/callback/oidc"
	OIDCScopes       string // space-separated; defaults to "openid email profile"

	// AllowedGoogleWorkspaceDomains, when non-empty, restricts Google sign-in
	// to ID tokens whose `hd` claim is in this list. Only applied when the
	// issuer is Google — generic OIDC providers (e.g. dev/test IdPs) are not
	// affected, so an empty list also means "no restriction".
	AllowedGoogleWorkspaceDomains []string

	AnthropicAPIKey string
	AnthropicModel  string

	// MetricsToken, when non-empty, gates /metrics with Bearer auth. Default
	// is open — relies on the public ingress not routing /metrics.
	MetricsToken string
}

func loadConfig() Config {
	c := Config{
		DatabaseURL:   getenv("DATABASE_URL", "postgres://marketplace:marketplace@localhost:5432/marketplace?sslmode=disable"),
		JWTSecret:     getenv("JWT_SECRET", "dev-secret-change-me-please-32-chars-min"),
		ListenAddr:    getenv("LISTEN_ADDR", ":8080"),
		DataDir:       getenv("DATA_DIR", "./data"),
		PublicBaseURL: getenv("PUBLIC_BASE_URL", "http://localhost:8080"),

		MarketplaceName: getenv("MARKETPLACE_NAME", "oglimmer-marketplace"),
		DefaultLicense:  getenv("DEFAULT_LICENSE", "MIT"),

		AuthMode: strings.ToLower(getenv("AUTH_MODE", "password")),

		OIDCIssuerURL:    strings.TrimRight(getenv("OIDC_ISSUER_URL", ""), "/"),
		OIDCClientID:     getenv("OIDC_CLIENT_ID", ""),
		OIDCClientSecret: getenv("OIDC_CLIENT_SECRET", ""),
		OIDCRedirectURL:  getenv("OIDC_REDIRECT_URL", ""),
		OIDCScopes:       getenv("OIDC_SCOPES", "openid email profile"),

		AllowedGoogleWorkspaceDomains: parseDomainList(getenv("OIDC_GOOGLE_WORKSPACE_DOMAINS", "")),

		AnthropicAPIKey: getenv("ANTHROPIC_API_KEY", ""),
		AnthropicModel:  getenv("ANTHROPIC_MODEL", "claude-sonnet-4-6"),

		MetricsToken: getenv("METRICS_TOKEN", ""),
	}
	if c.AuthMode != "password" && c.AuthMode != "oidc" {
		log.Fatalf("AUTH_MODE must be 'password' or 'oidc', got %q", c.AuthMode)
	}
	if c.OIDCRedirectURL == "" {
		c.OIDCRedirectURL = strings.TrimRight(c.PublicBaseURL, "/") + "/api/auth/oidc/callback"
	}
	if c.AuthMode == "oidc" && len(c.AllowedGoogleWorkspaceDomains) == 0 {
		log.Printf("WARN: AUTH_MODE=oidc but OIDC_GOOGLE_WORKSPACE_DOMAINS is empty — Google Workspace domain restriction is disabled")
	}
	return c
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func parseDomainList(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.ToLower(strings.TrimSpace(p))
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func main() {
	cfg := loadConfig()

	if err := os.MkdirAll(cfg.DataDir+"/repos", 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir+"/work", 0o755); err != nil {
		log.Fatalf("create work dir: %v", err)
	}

	db, err := openDB(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	registerDBStatsCollector(db)

	app := &App{cfg: cfg, db: db}

	if cfg.AuthMode == "oidc" {
		if err := app.initOIDC(context.Background()); err != nil {
			log.Fatalf("oidc init: %v", err)
		}
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(httpMetricsMiddleware)
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

	r.Method("GET", "/metrics", metricsHandler(cfg.MetricsToken))

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

		switch cfg.AuthMode {
		case "password":
			r.Post("/auth/register", app.handleRegister)
			r.Post("/auth/login", app.handleLogin)
		case "oidc":
			r.Get("/auth/oidc/login", app.handleOIDCLogin)
			r.Get("/auth/oidc/callback", app.handleOIDCCallback)
		}

		r.Group(func(r chi.Router) {
			r.Use(app.authMiddleware)
			r.Get("/me", app.handleMe)
			r.Post("/me/token/regenerate", app.handleRegenerateAPIToken)
			r.Get("/me/deleted-plugins", app.handleListDeletedPlugins)
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

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
