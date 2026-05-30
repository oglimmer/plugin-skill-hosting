package config

import (
	"strings"
	"testing"
)

func TestRequiresUserApproval(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{"password mode never requires approval", Config{AuthMode: "password"}, false},
		{"password mode with domains set still no approval",
			Config{AuthMode: "password", AllowedGoogleWorkspaceDomains: []string{"acme.com"}}, false},
		{"oidc + workspace domains = corporate, no approval",
			Config{AuthMode: "oidc", AllowedGoogleWorkspaceDomains: []string{"acme.com"}}, false},
		{"oidc + empty domains = open OIDC, approval required",
			Config{AuthMode: "oidc"}, true},
		{"oidc + nil-slice domains = approval required",
			Config{AuthMode: "oidc", AllowedGoogleWorkspaceDomains: nil}, true},
	}
	for _, c := range cases {
		if got := c.cfg.RequiresUserApproval(); got != c.want {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
		}
	}
}

func TestInsecureJWTSecret(t *testing.T) {
	cases := []struct {
		name   string
		secret string
		want   bool
	}{
		{"in-repo default is rejected", defaultJWTSecret, true},
		{"empty is rejected", "", true},
		{"31 chars is rejected", strings.Repeat("a", 31), true},
		{"exactly 32 chars is accepted", strings.Repeat("a", 32), false},
		{"openssl rand -hex 32 (64 chars) is accepted", strings.Repeat("0", 64), false},
	}
	for _, c := range cases {
		if got := insecureJWTSecret(c.secret); got != c.want {
			t.Errorf("%s: insecureJWTSecret(len=%d) = %v, want %v", c.name, len(c.secret), got, c.want)
		}
	}
}

func TestDeriveAllowedOrigins(t *testing.T) {
	cases := []struct {
		name     string
		baseURL  string
		override string
		want     []string
	}{
		{"explicit override wins", "https://mp.example.com", "https://a.com,https://b.com",
			[]string{"https://a.com", "https://b.com"}},
		{"explicit wildcard override", "https://mp.example.com", "*", []string{"*"}},
		{"localhost derives to wildcard for Vite dev", "http://localhost:8080", "", []string{"*"}},
		{"loopback IP derives to wildcard", "http://127.0.0.1:8080", "", []string{"*"}},
		{"production host locks to its own origin", "https://mp.example.com", "",
			[]string{"https://mp.example.com"}},
		{"production host with port keeps the port", "https://mp.example.com:8443", "",
			[]string{"https://mp.example.com:8443"}},
		{"unparseable base falls back to wildcard", "", "", []string{"*"}},
	}
	for _, c := range cases {
		got := deriveAllowedOrigins(c.baseURL, c.override)
		if len(got) != len(c.want) {
			t.Errorf("%s: got %v, want %v", c.name, got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("%s: [%d] = %q, want %q", c.name, i, got[i], c.want[i])
			}
		}
	}
}

func TestParseDomainList(t *testing.T) {
	cases := []struct {
		in   string
		want []string
	}{
		{"", nil},
		{"   ", nil},
		{"yourcompany.com", []string{"yourcompany.com"}},
		{"a.com,b.com", []string{"a.com", "b.com"}},
		{" A.com , B.COM ,, ", []string{"a.com", "b.com"}},
	}
	for _, c := range cases {
		got := parseDomainList(c.in)
		if len(got) != len(c.want) {
			t.Errorf("parseDomainList(%q) len = %d, want %d (%v vs %v)", c.in, len(got), len(c.want), got, c.want)
			continue
		}
		for i := range got {
			if got[i] != c.want[i] {
				t.Errorf("parseDomainList(%q)[%d] = %q, want %q", c.in, i, got[i], c.want[i])
			}
		}
	}
}
