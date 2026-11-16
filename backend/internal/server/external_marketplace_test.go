package server

import (
	"testing"

	"marketplace/internal/config"
)

func TestParseExternalRepoSource(t *testing.T) {
	cases := []struct {
		in           string
		override     string
		wantProvider string
		wantHost     string
		wantPath     string
		wantWebBase  string
		wantOK       bool
	}{
		{"https://github.com/oglimmer/skill-sync.git", "", "github", "github.com", "oglimmer/skill-sync", "https://github.com", true},
		{"https://github.com/oglimmer/skill-sync", "", "github", "github.com", "oglimmer/skill-sync", "https://github.com", true},
		{"http://github.com/owner/repo.git", "", "github", "github.com", "owner/repo", "http://github.com", true},
		{"git@github.com:oglimmer/skill-sync.git", "", "github", "github.com", "oglimmer/skill-sync", "https://github.com", true},
		{"git@github.com:oglimmer/skill-sync", "", "github", "github.com", "oglimmer/skill-sync", "https://github.com", true},
		{"https://gitlab.com/group/project.git", "", "gitlab", "gitlab.com", "group/project", "https://gitlab.com", true},
		{"git@gitlab.com:group/project.git", "", "gitlab", "gitlab.com", "group/project", "https://gitlab.com", true},

		// Token in URL — host should still parse cleanly, and WebBase must
		// not carry the credentials.
		{"https://x-access-token:abc@github.com/owner/repo.git", "", "github", "github.com", "owner/repo", "https://github.com", true},
		{"https://oauth2:abc@gitlab.com/group/project.git", "", "gitlab", "gitlab.com", "group/project", "https://gitlab.com", true},

		// GitLab subgroups are fine; GitHub has no nesting.
		{"https://gitlab.com/group/sub/project.git", "", "gitlab", "gitlab.com", "group/sub/project", "https://gitlab.com", true},
		{"https://github.com/owner/repo/extra.git", "", "", "", "", "", false},

		// Self-hosted GitLab detected by a "gitlab" host label.
		{"https://gitlab.example.com/team/mirror.git", "", "gitlab", "gitlab.example.com", "team/mirror", "https://gitlab.example.com", true},
		{"git@gitlab.example.com:team/sub/mirror.git", "", "gitlab", "gitlab.example.com", "team/sub/mirror", "https://gitlab.example.com", true},
		// SSH port is not the HTTPS port — drop it.
		{"ssh://git@gitlab.example.com:2222/team/mirror.git", "", "gitlab", "gitlab.example.com", "team/mirror", "https://gitlab.example.com", true},
		// HTTPS port is kept.
		{"https://GitLab.Example.com:8443/team/mirror.git", "", "gitlab", "gitlab.example.com", "team/mirror", "https://gitlab.example.com:8443", true},
		// "gitlab" must be a whole label, not a substring.
		{"https://mygitlabby.example.com/team/mirror.git", "", "", "", "", "", false},

		// Self-hosted GitLab on an unrecognisable host needs the override.
		{"https://git.example.com/owner/repo.git", "", "", "", "", "", false},
		{"https://git.example.com/owner/repo.git", "gitlab", "gitlab", "git.example.com", "owner/repo", "https://git.example.com", true},
		{"https://git.example.com/owner/repo.git", "GitLab", "gitlab", "git.example.com", "owner/repo", "https://git.example.com", true},
		{"https://ghe.example.com/owner/repo.git", "github", "github", "ghe.example.com", "owner/repo", "https://ghe.example.com", true},
		{"https://git.example.com/owner/repo.git", "bitbucket", "", "", "", "", false},

		// Unsupported hosts.
		{"git@bitbucket.org:owner/repo.git", "", "", "", "", "", false},

		// Garbage / empty.
		{"", "", "", "", "", "", false},
		{"not-a-url", "", "", "", "", "", false},
		{"https://github.com/", "", "", "", "", "", false},
		{"https://github.com/owner", "", "", "", "", "", false},
		{"https://gitlab.com/group//project.git", "", "", "", "", "", false},
	}
	for _, c := range cases {
		t.Run(c.in+"|"+c.override, func(t *testing.T) {
			r, ok := parseExternalRepoSource(c.in, c.override)
			if r.Provider != c.wantProvider || r.Host != c.wantHost || r.Path != c.wantPath || r.WebBase != c.wantWebBase || ok != c.wantOK {
				t.Errorf("parseExternalRepoSource(%q, %q) = (%+v, %v), want (%q, %q, %q, %q, %v)",
					c.in, c.override, r, ok, c.wantProvider, c.wantHost, c.wantPath, c.wantWebBase, c.wantOK)
			}
		})
	}
}

func TestBuildExternalMarketplaceDoc_GitLabSubgroup(t *testing.T) {
	cfg := config.Config{
		ExternalGitRemoteURL: "https://oauth2:secret@gitlab.com/acme/tools/skills-mirror.git",
		ExternalGitBranch:    "main",
		MarketplaceName:      "acme",
	}
	doc, ok := buildExternalMarketplaceDoc(cfg, []Plugin{
		{Name: "alpha", Version: "1.0.0", AuthorName: "A"},
		{Name: "beta", Version: "2.0.0"},
	})
	if !ok {
		t.Fatal("expected a marketplace doc for a gitlab.com remote")
	}
	if doc.Owner.URL != "https://gitlab.com/acme/tools/skills-mirror" {
		t.Errorf("owner.url = %q", doc.Owner.URL)
	}
	if len(doc.Plugins) != 2 {
		t.Fatalf("got %d plugins, want 2", len(doc.Plugins))
	}
	src := doc.Plugins[0].Source
	if src.Source != "git-subdir" ||
		src.URL != "https://gitlab.com/acme/tools/skills-mirror.git" ||
		src.Path != "plugins/alpha" ||
		src.Ref != "main" {
		t.Errorf("source = %+v", src)
	}
	if doc.Plugins[0].Author == nil || doc.Plugins[1].Author != nil {
		t.Errorf("author handling wrong: %+v / %+v", doc.Plugins[0].Author, doc.Plugins[1].Author)
	}
}

func TestBuildExternalMarketplaceDoc_KeepsHTTPSPort(t *testing.T) {
	cfg := config.Config{ExternalGitRemoteURL: "https://gitlab.example.com:8443/team/mirror.git"}
	doc, ok := buildExternalMarketplaceDoc(cfg, []Plugin{{Name: "p"}})
	if !ok {
		t.Fatal("expected a doc")
	}
	if got := doc.Plugins[0].Source.URL; got != "https://gitlab.example.com:8443/team/mirror.git" {
		t.Errorf("source url = %q", got)
	}
	if got := doc.Plugins[0].Source.Ref; got != "main" {
		t.Errorf("default ref = %q, want main", got)
	}
}

func TestBuildExternalMarketplaceDoc_UnsupportedRemote(t *testing.T) {
	cfg := config.Config{ExternalGitRemoteURL: "https://git.example.com/o/r.git"}
	if _, ok := buildExternalMarketplaceDoc(cfg, nil); ok {
		t.Error("expected no doc for an unrecognised host without EXTERNAL_GIT_PROVIDER")
	}
}
