package main

import "testing"

func TestEmbedTokenInBase(t *testing.T) {
	cases := []struct {
		base, token, want string
	}{
		{"https://example.com", "tok123", "https://_:tok123@example.com"},
		{"https://example.com/", "tok123", "https://_:tok123@example.com"},
		{"https://example.com/path/", "tok123", "https://_:tok123@example.com/path"},
		{"https://example.com", "", "https://example.com"}, // empty token => unchanged
		{"::not a url", "tok", "::not a url"},              // unparseable => unchanged
	}
	for _, c := range cases {
		got := embedTokenInBase(c.base, c.token)
		if got != c.want {
			t.Errorf("embedTokenInBase(%q, %q) = %q, want %q", c.base, c.token, got, c.want)
		}
	}
}
