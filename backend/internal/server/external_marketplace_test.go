package server

import "testing"

func TestParseExternalRepoSource(t *testing.T) {
	cases := []struct {
		in           string
		wantProvider string
		wantSlug     string
		wantOK       bool
	}{
		{"https://github.com/oglimmer/skill-sync.git", "github", "oglimmer/skill-sync", true},
		{"https://github.com/oglimmer/skill-sync", "github", "oglimmer/skill-sync", true},
		{"http://github.com/owner/repo.git", "github", "owner/repo", true},
		{"git@github.com:oglimmer/skill-sync.git", "github", "oglimmer/skill-sync", true},
		{"git@github.com:oglimmer/skill-sync", "github", "oglimmer/skill-sync", true},
		{"https://gitlab.com/group/project.git", "gitlab", "group/project", true},
		{"git@gitlab.com:group/project.git", "gitlab", "group/project", true},

		// Token in URL — host should still parse cleanly.
		{"https://x-access-token:abc@github.com/owner/repo.git", "github", "owner/repo", true},

		// Subgroups / nested paths aren't representable as "owner/repo".
		{"https://gitlab.com/group/sub/project.git", "", "", false},

		// Unsupported hosts.
		{"https://git.example.com/owner/repo.git", "", "", false},
		{"git@bitbucket.org:owner/repo.git", "", "", false},

		// Garbage / empty.
		{"", "", "", false},
		{"not-a-url", "", "", false},
		{"https://github.com/", "", "", false},
		{"https://github.com/owner", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			provider, slug, ok := parseExternalRepoSource(c.in)
			if provider != c.wantProvider || slug != c.wantSlug || ok != c.wantOK {
				t.Errorf("parseExternalRepoSource(%q) = (%q, %q, %v), want (%q, %q, %v)",
					c.in, provider, slug, ok, c.wantProvider, c.wantSlug, c.wantOK)
			}
		})
	}
}

func TestStripGitSuffix(t *testing.T) {
	cases := map[string]string{
		"https://github.com/o/r.git": "https://github.com/o/r",
		"https://github.com/o/r":     "https://github.com/o/r",
		"":                           "",
	}
	for in, want := range cases {
		if got := stripGitSuffix(in); got != want {
			t.Errorf("stripGitSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
