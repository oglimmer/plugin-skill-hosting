// Package config loads and validates process configuration from environment
// variables. It is the only place getenv-style defaults live.
package config

import (
	"log"
	"os"
	"strings"
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

// RequiresUserApproval reports whether new users must be approved by an
// existing approved user before they can access the system. The flow is
// engaged only for OIDC mode without a Google Workspace domain allowlist —
// password and "corporate" (domain-restricted) OIDC deployments still admit
// users immediately.
func (c Config) RequiresUserApproval() bool {
	return c.AuthMode == "oidc" && len(c.AllowedGoogleWorkspaceDomains) == 0
}

func Load() Config {
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
