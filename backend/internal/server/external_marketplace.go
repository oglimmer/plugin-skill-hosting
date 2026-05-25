package server

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// renderExternalMarketplaceJSON writes .claude-plugin/marketplace.json into
// the external work tree, listing every active DB plugin with a source that
// points at its subdirectory inside the same repo. The resulting file lets
// the external repo be used directly as a Claude Code marketplace, e.g.
//
//	/plugin marketplace add https://github.com/<owner>/<repo>
//
// Today only GitHub and GitLab URLs produce a usable marketplace.json;
// other providers are skipped with a no-op (Claude Code's marketplace
// schema has no generic "git+path" source type as of this writing).
func (a *App) renderExternalMarketplaceJSON(ctx context.Context, workDir string) error {
	provider, repoSlug, ok := parseExternalRepoSource(a.Cfg.ExternalGitRemoteURL)
	if !ok {
		// Unsupported provider — leave any existing file alone. Outbound
		// push still works, but the repo can't be used as a marketplace
		// without manual marketplace.json maintenance.
		return nil
	}

	plugins, err := a.queryPlugins(ctx, `WHERE p.deleted_at IS NULL ORDER BY p.name ASC`)
	if err != nil {
		return err
	}

	branch := a.Cfg.ExternalGitBranch
	if branch == "" {
		branch = "main"
	}

	name := a.Cfg.MarketplaceName
	if name == "" {
		name = "oglimmer-marketplace"
	}
	ownerURL := stripGitSuffix(a.Cfg.ExternalGitRemoteURL)

	doc := marketplaceDoc{
		Schema: MarketplaceSchemaURL,
		Name:   name,
		Owner: marketplaceOwner{
			Name: name,
			URL:  ownerURL,
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
				Source: provider,
				Repo:   repoSlug,
				Path:   "plugins/" + p.Name,
				Branch: branch,
			},
		}
		if p.AuthorName != "" || p.AuthorEmail != "" {
			mp.Author = &marketplaceAuthor{Name: p.AuthorName, Email: p.AuthorEmail}
		}
		doc.Plugins = append(doc.Plugins, mp)
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

// parseExternalRepoSource recognises the URL forms Claude Code's marketplace
// schema supports as first-class source types. Returns (provider, "owner/repo",
// true) for github.com and gitlab.com URLs over HTTPS or SSH; (_, _, false)
// for anything else.
func parseExternalRepoSource(remoteURL string) (provider, repoSlug string, ok bool) {
	remoteURL = strings.TrimSpace(remoteURL)
	if remoteURL == "" {
		return "", "", false
	}

	// SSH form: git@host:owner/repo(.git)
	if strings.HasPrefix(remoteURL, "git@") {
		idx := strings.Index(remoteURL, ":")
		if idx < 0 {
			return "", "", false
		}
		host := strings.TrimPrefix(remoteURL[:idx], "git@")
		path := strings.TrimSuffix(remoteURL[idx+1:], ".git")
		prov := providerForHost(host)
		if prov == "" || path == "" || strings.Count(path, "/") != 1 {
			return "", "", false
		}
		return prov, path, true
	}

	u, err := url.Parse(remoteURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "ssh") {
		return "", "", false
	}
	host := u.Host
	if at := strings.LastIndex(host, "@"); at >= 0 {
		host = host[at+1:]
	}
	path := strings.TrimSuffix(strings.TrimPrefix(u.Path, "/"), ".git")
	prov := providerForHost(host)
	if prov == "" || path == "" {
		return "", "", false
	}
	// Marketplace plugins are addressed by exactly "owner/repo" — anything
	// deeper (subgroups, subprojects) isn't yet representable, so bail.
	if strings.Count(path, "/") != 1 {
		return "", "", false
	}
	return prov, path, true
}

func providerForHost(host string) string {
	host = strings.ToLower(host)
	switch {
	case host == "github.com" || strings.HasSuffix(host, ".github.com"):
		return "github"
	case host == "gitlab.com" || strings.HasSuffix(host, ".gitlab.com"):
		return "gitlab"
	}
	return ""
}

func stripGitSuffix(u string) string {
	return strings.TrimSuffix(u, ".git")
}
