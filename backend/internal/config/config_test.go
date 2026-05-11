package config

import "testing"

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
