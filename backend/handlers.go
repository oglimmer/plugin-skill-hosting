package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
)

type createPluginReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	AuthorName  string `json:"authorName"`
	AuthorEmail string `json:"authorEmail"`
	Homepage    string `json:"homepage"`
	License     string `json:"license"`
}

func (a *App) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	rows, err := a.db.QueryContext(r.Context(), `
		SELECT p.id, p.owner_id, u.username, p.name, p.description, p.version,
		       p.author_name, p.author_email, p.homepage, p.license,
		       p.created_at, p.updated_at
		FROM plugins p JOIN users u ON u.id = p.owner_id
		ORDER BY p.updated_at DESC
	`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()

	plugins := []Plugin{}
	for rows.Next() {
		var p Plugin
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.OwnerName, &p.Name, &p.Description, &p.Version,
			&p.AuthorName, &p.AuthorEmail, &p.Homepage, &p.License,
			&p.CreatedAt, &p.UpdatedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error")
			return
		}
		plugins = append(plugins, p)
	}
	writeJSON(w, http.StatusOK, plugins)
}

func (a *App) loadPluginByName(ctx context.Context, name string) (*Plugin, error) {
	p := &Plugin{}
	err := a.db.QueryRowContext(ctx, `
		SELECT p.id, p.owner_id, u.username, p.name, p.description, p.version,
		       p.author_name, p.author_email, p.homepage, p.license,
		       p.created_at, p.updated_at
		FROM plugins p JOIN users u ON u.id = p.owner_id
		WHERE p.name = $1
	`, name).Scan(&p.ID, &p.OwnerID, &p.OwnerName, &p.Name, &p.Description, &p.Version,
		&p.AuthorName, &p.AuthorEmail, &p.Homepage, &p.License,
		&p.CreatedAt, &p.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (a *App) loadSkillsForPlugin(ctx context.Context, pluginID string) ([]Skill, error) {
	rows, err := a.db.QueryContext(ctx, `
		SELECT id, plugin_id, name, description, body, created_at, updated_at
		FROM skills WHERE plugin_id = $1 ORDER BY name ASC
	`, pluginID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	skills := []Skill{}
	for rows.Next() {
		var s Skill
		if err := rows.Scan(&s.ID, &s.PluginID, &s.Name, &s.Description, &s.Body, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		skills = append(skills, s)
	}
	return skills, nil
}

func (a *App) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	p, err := a.loadPluginByName(r.Context(), name)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "plugin not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	skills, err := a.loadSkillsForPlugin(r.Context(), p.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	p.Skills = skills
	writeJSON(w, http.StatusOK, p)
}

func (a *App) handleCreatePlugin(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	var req createPluginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Name = strings.TrimSpace(strings.ToLower(req.Name))
	if !slugRe.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "name must be 3-64 chars, lowercase, [a-z0-9-]")
		return
	}
	if req.Version == "" {
		req.Version = "0.1.0"
	}

	var id string
	err := a.db.QueryRowContext(r.Context(), `
		INSERT INTO plugins (owner_id, name, description, version, author_name, author_email, homepage, license)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id
	`, user.ID, req.Name, req.Description, req.Version, req.AuthorName, req.AuthorEmail, req.Homepage, req.License).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "plugin name already taken")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	p, _ := a.loadPluginByName(r.Context(), req.Name)
	if p != nil {
		if err := a.materializePlugin(r.Context(), p); err != nil {
			writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
			return
		}
	}
	writeJSON(w, http.StatusOK, p)
}

func (a *App) handleDeletePlugin(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	name := chi.URLParam(r, "name")
	p, err := a.loadPluginByName(r.Context(), name)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "plugin not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if p.OwnerID != user.ID {
		writeErr(w, http.StatusForbidden, "not your plugin")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM plugins WHERE id = $1`, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	a.removeRepo(name)
	w.WriteHeader(http.StatusNoContent)
}

type skillReq struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
}

func (a *App) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	pluginName := chi.URLParam(r, "name")
	p, err := a.loadPluginByName(r.Context(), pluginName)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "plugin not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if p.OwnerID != user.ID {
		writeErr(w, http.StatusForbidden, "not your plugin")
		return
	}

	var req skillReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Name = strings.TrimSpace(strings.ToLower(req.Name))
	if !slugRe.MatchString(req.Name) {
		writeErr(w, http.StatusBadRequest, "skill name must be 3-64 chars, lowercase, [a-z0-9-]")
		return
	}
	if strings.TrimSpace(req.Description) == "" {
		writeErr(w, http.StatusBadRequest, "description is required")
		return
	}

	var id string
	err = a.db.QueryRowContext(r.Context(), `
		INSERT INTO skills (plugin_id, name, description, body) VALUES ($1, $2, $3, $4) RETURNING id
	`, p.ID, req.Name, req.Description, req.Body).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "skill with that name already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `UPDATE plugins SET updated_at = now() WHERE id = $1`, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := a.materializePlugin(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	skills, _ := a.loadSkillsForPlugin(r.Context(), p.ID)
	for _, s := range skills {
		if s.ID == id {
			writeJSON(w, http.StatusOK, s)
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (a *App) handleUpdateSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	pluginName := chi.URLParam(r, "name")
	skillName := chi.URLParam(r, "skill")
	p, err := a.loadPluginByName(r.Context(), pluginName)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "plugin not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if p.OwnerID != user.ID {
		writeErr(w, http.StatusForbidden, "not your plugin")
		return
	}

	var req skillReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	res, err := a.db.ExecContext(r.Context(), `
		UPDATE skills SET description = $1, body = $2, updated_at = now()
		WHERE plugin_id = $3 AND name = $4
	`, req.Description, req.Body, p.ID, skillName)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `UPDATE plugins SET updated_at = now() WHERE id = $1`, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := a.materializePlugin(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	pluginName := chi.URLParam(r, "name")
	skillName := chi.URLParam(r, "skill")
	p, err := a.loadPluginByName(r.Context(), pluginName)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "plugin not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if p.OwnerID != user.ID {
		writeErr(w, http.StatusForbidden, "not your plugin")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `DELETE FROM skills WHERE plugin_id = $1 AND name = $2`, p.ID, skillName); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `UPDATE plugins SET updated_at = now() WHERE id = $1`, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.materializePlugin(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
