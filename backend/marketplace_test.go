package main

import "testing"

func TestEmbedTokenInBase(t *testing.T) {
	cases := []struct {
		base, token, want string
	}{
		{"https://example.com", "tok123", "https://_:tok123@example.com"},
		{"https://example.com/", "tok123", "https://_:tok123@example.com"},
		{"https://example.com/path/", "tok123", "https://_:tok123@example.com/path"},
		{"https://example.com", "", "https://example.com"},                // empty token => unchanged
		{"::not a url", "tok", "::not a url"},                             // unparseable => unchanged
		{"http://localhost:8080", "abc", "http://_:abc@localhost:8080"},   // port preserved
		{"https://example.com", "to:k", "https://_:to%3Ak@example.com"},   // colon in token escaped
		{"https://example.com/p", "tok", "https://_:tok@example.com/p"},   // path without trailing slash
	}
	for _, c := range cases {
		got := embedTokenInBase(c.base, c.token)
		if got != c.want {
			t.Errorf("embedTokenInBase(%q, %q) = %q, want %q", c.base, c.token, got, c.want)
		}
	}
}
