package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"marketplace/internal/config"
)

// renderExternalMarketplaceJSON writes .claude-plugin/marketplace.json into
// the external work tree, listing every active DB plugin with a "git-subdir"
// source that points at its subdirectory inside the same repo. The resulting
// file lets the external repo be used directly as a Claude Code marketplace,
// e.g.
//
//	/plugin marketplace add https://github.com/<owner>/<repo>
//	/plugin marketplace add https://gitlab.example.com/<group>/<project>.git
//
// Only GitHub and GitLab remotes (gitlab.com or self-hosted, see
// parseExternalRepoSource) produce a marketplace.json; other providers are
// skipped with a no-op so we don't write a URL the extractor can't normalize.
func (a *App) renderExternalMarketplaceJSON(ctx context.Context, workDir string) error {
	if _, ok := parseExternalRepoSource(a.Cfg.ExternalGitRemoteURL, a.Cfg.ExternalGitProvider); !ok {
		// Unsupported provider — leave any existing file alone. Outbound
		// push still works, but the repo can't be used as a marketplace
		// without manual marketplace.json maintenance.
		return nil
	}

	plugins, err := a.queryPlugins(ctx, `WHERE p.deleted_at IS NULL ORDER BY p.name ASC`)
	if err != nil {
		return err
	}
	doc, ok := buildExternalMarketplaceDoc(a.Cfg, plugins)
	if !ok {
		return nil
	}

	body, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')

	dir := filepath.Join(workDir, ".claude-plugin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "marketplace.json"), body, 0o644)
}

// buildExternalMarketplaceDoc builds the marketplace.json document for the
// external mirror. Each plugin gets a "git-subdir" source that points at
// plugins/<name> inside the mirror repo. Returns false when the remote is not
// a supported GitHub / GitLab URL.
func buildExternalMarketplaceDoc(cfg config.Config, plugins []Plugin) (marketplaceDoc, bool) {
	repo, ok := parseExternalRepoSource(cfg.ExternalGitRemoteURL, cfg.ExternalGitProvider)
	if !ok {
		return marketplaceDoc{}, false
	}
	webURL := repo.WebBase + "/" + repo.Path
	cloneURL := webURL + ".git"

	ref := cfg.ExternalGitBranch
	if ref == "" {
		ref = "main"
	}

	name := cfg.MarketplaceName
	if name == "" {
		name = "oglimmer-marketplace"
	}

	doc := marketplaceDoc{
		Schema: MarketplaceSchemaURL,
		Name:   name,
		Owner: marketplaceOwner{
			Name: name,
			URL:  webURL,
		},
		Plugins: []marketplacePlugin{},
	}

	for _, p := range plugins {
		mp := marketplacePlugin{
			Name:        p.Name,
			Description: p.Description,
			Version:     p.Version,
			Homepage:    p.Homepage,
			License:     p.License,
			Source: marketplaceSource{
				Source: "git-subdir",
				URL:    cloneURL,
				Path:   "plugins/" + p.Name,
				Ref:    ref,
			},
		}
		if p.AuthorName != "" || p.AuthorEmail != "" {
			mp.Author = &marketplaceAuthor{Name: p.AuthorName, Email: p.AuthorEmail}
		}
		doc.Plugins = append(doc.Plugins, mp)
	}
	return doc, true
}

// externalRepo is a parsed external mirror remote.
type externalRepo struct {
	Provider string // "github" or "gitlab"
	Host     string // lower-case host name, no port, no credentials
	Path     string // repo path, no leading slash, no .git suffix
	// WebBase is the browsable / cloneable origin without credentials:
	// scheme://host[:port] for http(s) remotes, https://host for SSH remotes
	// (the SSH port says nothing about the HTTPS port).
	WebBase string
}

// parseExternalRepoSource recognises the remote URL forms we can turn into a
// marketplace.json git-subdir source: GitHub and GitLab remotes over HTTPS or
// SSH. Returns false for anything else.
//
// providerOverride ("github" / "gitlab", from EXTERNAL_GIT_PROVIDER) forces
// the provider for hosts we can't recognise by name, e.g. a self-hosted
// GitLab at git.example.com. When empty the provider is detected from the
// host (see providerForHost).
//
// GitHub paths must be exactly "owner/repo". GitLab paths may be nested
// ("group/subgroup/project").
func parseExternalRepoSource(remoteURL, providerOverride string) (externalRepo, bool) {
	r, ok := splitRemoteURL(remoteURL)
	if !ok {
		return externalRepo{}, false
	}
	r.Provider = resolveProvider(r.Host, providerOverride)
	if r.Provider == "" || !validRepoPath(r.Provider, r.Path) {
		return externalRepo{}, false
	}
	return r, true
}

// splitRemoteURL fills Host, Path and WebBase (see externalRepo) from an
// HTTPS, HTTP, ssh:// or scp-style git@ remote. Credentials are dropped.
func splitRemoteURL(remoteURL string) (externalRepo, bool) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return externalRepo{}, false
	}

	var r externalRepo
	// SCP-style SSH form: git@host:path(.git)
	if strings.HasPrefix(remoteURL, "git@") && !strings.Contains(remoteURL, "://") {
		idx := strings.Index(remoteURL, ":")
		if idx < 0 {
			return externalRepo{}, false
		}
		r.Host = strings.ToLower(strings.TrimPrefix(remoteURL[:idx], "git@"))
		r.Path = strings.TrimSuffix(strings.Trim(remoteURL[idx+1:], "/"), ".git")
		r.WebBase = "https://" + r.Host
	} else {
		u, err := url.Parse(remoteURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ssh") {
			return externalRepo{}, false
		}
		r.Host = strings.ToLower(u.Hostname())
		r.Path = strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if u.Scheme == "ssh" {
			r.WebBase = "https://" + r.Host
		} else {
			hostPort := r.Host
			if port := u.Port(); port != "" {
				hostPort += ":" + port
			}
			r.WebBase = u.Scheme + "://" + hostPort
		}
	}
	if r.Host == "" || r.Path == "" {
		return externalRepo{}, false
	}
	return r, true
}

func validRepoPath(provider, path string) bool {
	parts := strings.Split(path, "/")
	for _, p := range parts {
		if p == "" {
			return false
		}
	}
	switch provider {
	case "github":
		return len(parts) == 2
	case "gitlab":
		// GitLab supports nested groups: group/sub/.../project.
		return len(parts) >= 2
	}
	return false
}

// resolveProvider returns the explicit override when set, otherwise the
// provider detected from the host name.
func resolveProvider(host, override string) string {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case "github":
		return "github"
	case "gitlab":
		return "gitlab"
	}
	return providerForHost(host)
}

// providerForHost detects the provider from the host name: github.com,
// gitlab.com, and any host with a "gitlab" DNS label (gitlab.example.com,
// code.gitlab.example.org) for self-hosted GitLab.
func providerForHost(host string) string {
	host = strings.ToLower(host)
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return "github"
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		return "gitlab"
	}
	for _, label := range strings.Split(host, ".") {
		if label == "gitlab" {
			return "gitlab"
		}
	}
	return ""
}
