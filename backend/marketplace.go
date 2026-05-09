package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

type marketplaceAuthor struct {
	Name  string `json:"name,omitempty"`
	Email string `json:"email,omitempty"`
}

type marketplaceSource struct {
	Source string `json:"source"`
	URL    string `json:"url"`
}

type marketplacePlugin struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Version     string             `json:"version,omitempty"`
	Author      *marketplaceAuthor `json:"author,omitempty"`
	Homepage    string             `json:"homepage,omitempty"`
	License     string             `json:"license,omitempty"`
	Source      marketplaceSource  `json:"source"`
}

type marketplaceOwner struct {
	Name  string `json:"name"`
	Email string `json:"email,omitempty"`
	URL   string `json:"url,omitempty"`
}

type marketplaceDoc struct {
	Name    string              `json:"name"`
	Owner   marketplaceOwner    `json:"owner"`
	Plugins []marketplacePlugin `json:"plugins"`
}

func (a *App) handleMarketplaceJSON(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)

	rows, err := a.db.QueryContext(r.Context(), `
		SELECT p.name, p.description, p.version, p.author_name, p.author_email, p.homepage, p.license
		FROM plugins p ORDER BY p.name ASC
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	base := strings.TrimRight(a.cfg.PublicBaseURL, "/")
	authedBase := embedTokenInBase(base, user.APIToken)
	name := a.cfg.MarketplaceName
	if name == "" {
		name = "oglimmer-marketplace"
	}
	doc := marketplaceDoc{
		Name: name,
		Owner: marketplaceOwner{
			Name: name,
			URL:  base,
		},
		Plugins: []marketplacePlugin{},
	}

	for rows.Next() {
		var name, desc, ver, an, ae, hp, lic string
		if err := rows.Scan(&name, &desc, &ver, &an, &ae, &hp, &lic); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error")
			return
		}
		mp := marketplacePlugin{
			Name:        name,
			Description: desc,
			Version:     ver,
			Homepage:    hp,
			License:     lic,
			Source: marketplaceSource{
				Source: "url",
				URL:    authedBase + "/git/" + name + ".git",
			},
		}
		if an != "" || ae != "" {
			mp.Author = &marketplaceAuthor{Name: an, Email: ae}
		}
		doc.Plugins = append(doc.Plugins, mp)
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	json.NewEncoder(w).Encode(doc)
}

// embedTokenInBase returns base with the api token embedded as the HTTP Basic
// Auth password (username "_"), so `git clone <url>` and Claude Code's fetch
// of marketplace.json both authenticate without prompting.
func embedTokenInBase(base, token string) string {
	if token == "" {
		return base
	}
	u, err := url.Parse(base)
	if err != nil {
		return base
	}
	u.User = url.UserPassword("_", token)
	return strings.TrimRight(u.String(), "/")
}
