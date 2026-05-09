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

// pluginSelectColumns lists every column queryPlugins expects, including the
// deleted-by user join used by the restore UI.
const pluginSelectColumns = `p.id, p.owner_id, u.username, p.name, p.description, p.version,
		       p.author_name, p.author_email, p.homepage, p.license,
		       p.created_at, p.updated_at,
		       p.deleted_at, p.deleted_by, du.username`

const pluginFromJoin = `FROM plugins p
		JOIN users u ON u.id = p.owner_id
		LEFT JOIN users du ON du.id = p.deleted_by`

func (a *App) queryPlugins(ctx context.Context, where string, args ...interface{}) ([]Plugin, error) {
	q := `SELECT ` + pluginSelectColumns + ` ` + pluginFromJoin
	if where != "" {
		q += ` ` + where
	}
	rows, err := a.db.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	plugins := []Plugin{}
	for rows.Next() {
		var p Plugin
		var deletedAt sql.NullTime
		var deletedBy, deletedByName sql.NullString
		if err := rows.Scan(&p.ID, &p.OwnerID, &p.OwnerName, &p.Name, &p.Description, &p.Version,
			&p.AuthorName, &p.AuthorEmail, &p.Homepage, &p.License,
			&p.CreatedAt, &p.UpdatedAt,
			&deletedAt, &deletedBy, &deletedByName); err != nil {
			return nil, err
		}
		if deletedAt.Valid {
			t := deletedAt.Time
			p.DeletedAt = &t
		}
		if deletedBy.Valid {
			v := deletedBy.String
			p.DeletedBy = &v
		}
		if deletedByName.Valid {
			v := deletedByName.String
			p.DeletedByName = &v
		}
		plugins = append(plugins, p)
	}
	return plugins, nil
}

func (a *App) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	plugins, err := a.queryPlugins(r.Context(),
		`WHERE p.deleted_at IS NULL ORDER BY p.updated_at DESC`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, plugins)
}

// handleListDeletedPlugins returns soft-deleted plugins owned by the caller,
// used to drive the restore UI.
func (a *App) handleListDeletedPlugins(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	plugins, err := a.queryPlugins(r.Context(),
		`WHERE p.deleted_at IS NOT NULL AND p.owner_id = $1 ORDER BY p.deleted_at DESC`, user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, plugins)
}

// loadPluginByName returns a plugin that is currently active (not soft-deleted).
// All write paths and the public-facing GET use this so deleted plugins are
// invisible without an explicit restore.
func (a *App) loadPluginByName(ctx context.Context, name string) (*Plugin, error) {
	plugins, err := a.queryPlugins(ctx, `WHERE p.name = $1 AND p.deleted_at IS NULL`, name)
	if err != nil {
		return nil, err
	}
	if len(plugins) == 0 {
		return nil, sql.ErrNoRows
	}
	return &plugins[0], nil
}

// loadPluginByNameAny returns a plugin regardless of soft-delete state. Used by
// the restore endpoint to locate the row before un-deleting it.
func (a *App) loadPluginByNameAny(ctx context.Context, name string) (*Plugin, error) {
	plugins, err := a.queryPlugins(ctx,
		`WHERE p.name = $1 ORDER BY (p.deleted_at IS NULL) DESC, p.deleted_at DESC LIMIT 1`, name)
	if err != nil {
		return nil, err
	}
	if len(plugins) == 0 {
		return nil, sql.ErrNoRows
	}
	return &plugins[0], nil
}

// loadSkillsForPlugin returns active (non-soft-deleted) skills with audit metadata.
func (a *App) loadSkillsForPlugin(ctx context.Context, pluginID string) ([]Skill, error) {
	return a.querySkills(ctx, `
		SELECT s.id, s.plugin_id, s.name, s.description, s.body, s.created_at, s.updated_at,
		       s.created_by, cu.username,
		       s.updated_by, uu.username,
		       s.deleted_at, s.deleted_by, du.username
		FROM skills s
		LEFT JOIN users cu ON cu.id = s.created_by
		LEFT JOIN users uu ON uu.id = s.updated_by
		LEFT JOIN users du ON du.id = s.deleted_by
		WHERE s.plugin_id = $1 AND s.deleted_at IS NULL
		ORDER BY s.name ASC
	`, pluginID)
}

// loadDeletedSkillsForPlugin returns soft-deleted skills, used by the restore UI.
func (a *App) loadDeletedSkillsForPlugin(ctx context.Context, pluginID string) ([]Skill, error) {
	return a.querySkills(ctx, `
		SELECT s.id, s.plugin_id, s.name, s.description, s.body, s.created_at, s.updated_at,
		       s.created_by, cu.username,
		       s.updated_by, uu.username,
		       s.deleted_at, s.deleted_by, du.username
		FROM skills s
		LEFT JOIN users cu ON cu.id = s.created_by
		LEFT JOIN users uu ON uu.id = s.updated_by
		LEFT JOIN users du ON du.id = s.deleted_by
		WHERE s.plugin_id = $1 AND s.deleted_at IS NOT NULL
		ORDER BY s.deleted_at DESC
	`, pluginID)
}

func (a *App) querySkills(ctx context.Context, query string, args ...interface{}) ([]Skill, error) {
	rows, err := a.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	skills := []Skill{}
	for rows.Next() {
		var s Skill
		var createdBy, updatedBy, deletedBy sql.NullString
		var createdByName, updatedByName, deletedByName sql.NullString
		var deletedAt sql.NullTime
		if err := rows.Scan(&s.ID, &s.PluginID, &s.Name, &s.Description, &s.Body, &s.CreatedAt, &s.UpdatedAt,
			&createdBy, &createdByName,
			&updatedBy, &updatedByName,
			&deletedAt, &deletedBy, &deletedByName); err != nil {
			return nil, err
		}
		if createdBy.Valid {
			v := createdBy.String
			s.CreatedBy = &v
		}
		if createdByName.Valid {
			v := createdByName.String
			s.CreatedByName = &v
		}
		if updatedBy.Valid {
			v := updatedBy.String
			s.UpdatedBy = &v
		}
		if updatedByName.Valid {
			v := updatedByName.String
			s.UpdatedByName = &v
		}
		if deletedAt.Valid {
			t := deletedAt.Time
			s.DeletedAt = &t
		}
		if deletedBy.Valid {
			v := deletedBy.String
			s.DeletedBy = &v
		}
		if deletedByName.Valid {
			v := deletedByName.String
			s.DeletedByName = &v
		}
		skills = append(skills, s)
	}
	return skills, nil
}

// loadActiveSkill fetches a single non-deleted skill by (plugin, name).
func (a *App) loadActiveSkill(ctx context.Context, pluginID, name string) (*Skill, error) {
	skills, err := a.querySkills(ctx, `
		SELECT s.id, s.plugin_id, s.name, s.description, s.body, s.created_at, s.updated_at,
		       s.created_by, cu.username,
		       s.updated_by, uu.username,
		       s.deleted_at, s.deleted_by, du.username
		FROM skills s
		LEFT JOIN users cu ON cu.id = s.created_by
		LEFT JOIN users uu ON uu.id = s.updated_by
		LEFT JOIN users du ON du.id = s.deleted_by
		WHERE s.plugin_id = $1 AND s.name = $2 AND s.deleted_at IS NULL
	`, pluginID, name)
	if err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return nil, sql.ErrNoRows
	}
	return &skills[0], nil
}

// loadSkillByID fetches a skill regardless of deletion state.
func (a *App) loadSkillByID(ctx context.Context, id string) (*Skill, error) {
	skills, err := a.querySkills(ctx, `
		SELECT s.id, s.plugin_id, s.name, s.description, s.body, s.created_at, s.updated_at,
		       s.created_by, cu.username,
		       s.updated_by, uu.username,
		       s.deleted_at, s.deleted_by, du.username
		FROM skills s
		LEFT JOIN users cu ON cu.id = s.created_by
		LEFT JOIN users uu ON uu.id = s.updated_by
		LEFT JOIN users du ON du.id = s.deleted_by
		WHERE s.id = $1
	`, id)
	if err != nil {
		return nil, err
	}
	if len(skills) == 0 {
		return nil, sql.ErrNoRows
	}
	return &skills[0], nil
}

// recordSkillVersion appends an entry to skill_versions for the given skill,
// auto-incrementing the per-skill version number.
func (a *App) recordSkillVersion(ctx context.Context, tx dbExec, skillID, action, name, description, body string, editedBy string) error {
	var nextVersion int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM skill_versions WHERE skill_id = $1`, skillID).
		Scan(&nextVersion); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO skill_versions (skill_id, version, action, name, description, body, edited_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, skillID, nextVersion, action, name, description, body, editedBy)
	return err
}

// dbExec is the subset of *sql.DB / *sql.Tx we use; lets recordSkillVersion run
// inside or outside a transaction.
type dbExec interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
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

	// Version is auto-managed: first plugin per owner stays at 0.1.0; every
	// subsequent plugin starts with the major bumped to 1.0.0. Any version the
	// client sends is ignored.
	var ownerPluginCount int
	if err := a.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM plugins WHERE owner_id = $1`, user.ID).Scan(&ownerPluginCount); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	version := "0.1.0"
	if ownerPluginCount > 0 {
		version = "1.0.0"
	}

	var id string
	err := a.db.QueryRowContext(r.Context(), `
		INSERT INTO plugins (owner_id, name, description, version, author_name, author_email, homepage, license)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id
	`, user.ID, req.Name, req.Description, version, req.AuthorName, req.AuthorEmail, req.Homepage, req.License).Scan(&id)
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

// handleDeletePlugin soft-deletes the plugin: the row stays in the database but
// the plugin is hidden from listings, the marketplace feed, and `git clone`
// (the bare repo is wiped on disk and re-materialized on restore).
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
	if _, err := a.db.ExecContext(r.Context(), `
		UPDATE plugins SET deleted_at = now(), deleted_by = $1, updated_at = now()
		WHERE id = $2
	`, user.ID, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	a.removeRepo(name)
	w.WriteHeader(http.StatusNoContent)
}

// handleRestorePlugin un-deletes a soft-deleted plugin owned by the caller and
// re-materializes its git repo. Fails if another active plugin already uses
// the same name (covered by the partial unique index).
func (a *App) handleRestorePlugin(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	name := chi.URLParam(r, "name")
	p, err := a.loadPluginByNameAny(r.Context(), name)
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
	if p.DeletedAt == nil {
		writeErr(w, http.StatusBadRequest, "plugin is not deleted")
		return
	}
	if _, err := a.db.ExecContext(r.Context(), `
		UPDATE plugins SET deleted_at = NULL, deleted_by = NULL, updated_at = now()
		WHERE id = $1
	`, p.ID); err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "an active plugin with that name already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	restored, err := a.loadPluginByName(r.Context(), name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.materializePlugin(r.Context(), restored); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	writeJSON(w, http.StatusOK, restored)
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

	// Count skills (including soft-deleted) before insert so the very first
	// skill ever added to this plugin doesn't bump the version.
	var existingSkillCount int
	if err := a.db.QueryRowContext(r.Context(),
		`SELECT COUNT(*) FROM skills WHERE plugin_id = $1`, p.ID).Scan(&existingSkillCount); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	var id string
	err = a.db.QueryRowContext(r.Context(), `
		INSERT INTO skills (plugin_id, name, description, body, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $5) RETURNING id
	`, p.ID, req.Name, req.Description, req.Body, user.ID).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "skill with that name already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), a.db, id, "create", req.Name, req.Description, req.Body, user.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if existingSkillCount > 0 {
		p.Version = bumpVersion(p.Version, bumpMinor)
	}
	if _, err := a.db.ExecContext(r.Context(),
		`UPDATE plugins SET version = $1, updated_at = now() WHERE id = $2`, p.Version, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	if err := a.materializePlugin(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	if s, err := a.loadSkillByID(r.Context(), id); err == nil {
		writeJSON(w, http.StatusOK, s)
		return
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

	var req skillReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	existing, err := a.loadActiveSkill(r.Context(), p.ID, skillName)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	if _, err := a.db.ExecContext(r.Context(), `
		UPDATE skills SET description = $1, body = $2, updated_at = now(), updated_by = $3
		WHERE id = $4
	`, req.Description, req.Body, user.ID, existing.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), a.db, existing.ID, "update", existing.Name, req.Description, req.Body, user.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	p.Version = bumpVersion(p.Version, bumpPatch)
	if _, err := a.db.ExecContext(r.Context(),
		`UPDATE plugins SET version = $1, updated_at = now() WHERE id = $2`, p.Version, p.ID); err != nil {
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

	existing, err := a.loadActiveSkill(r.Context(), p.ID, skillName)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	if _, err := a.db.ExecContext(r.Context(), `
		UPDATE skills SET deleted_at = now(), deleted_by = $1, updated_at = now(), updated_by = $1
		WHERE id = $2
	`, user.ID, existing.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), a.db, existing.ID, "delete", existing.Name, existing.Description, existing.Body, user.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	p.Version = bumpVersion(p.Version, bumpMinor)
	if _, err := a.db.ExecContext(r.Context(),
		`UPDATE plugins SET version = $1, updated_at = now() WHERE id = $2`, p.Version, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.materializePlugin(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListDeletedSkills returns soft-deleted skills for a plugin so the UI
// can offer "restore".
func (a *App) handleListDeletedSkills(w http.ResponseWriter, r *http.Request) {
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
	skills, err := a.loadDeletedSkillsForPlugin(r.Context(), p.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

// handleRestoreSkill un-deletes a soft-deleted skill. Fails if another active
// skill in the same plugin already uses the same name.
func (a *App) handleRestoreSkill(w http.ResponseWriter, r *http.Request) {
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

	// Pick the most recently deleted skill with this name (in case the same
	// name was deleted multiple times across history).
	var skillID, desc, body string
	err = a.db.QueryRowContext(r.Context(), `
		SELECT id, description, body FROM skills
		WHERE plugin_id = $1 AND name = $2 AND deleted_at IS NOT NULL
		ORDER BY deleted_at DESC LIMIT 1
	`, p.ID, skillName).Scan(&skillID, &desc, &body)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "no deleted skill with that name")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	if _, err := a.db.ExecContext(r.Context(), `
		UPDATE skills SET deleted_at = NULL, deleted_by = NULL, updated_at = now(), updated_by = $1
		WHERE id = $2
	`, user.ID, skillID); err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "an active skill with that name already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), a.db, skillID, "restore", skillName, desc, body, user.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	p.Version = bumpVersion(p.Version, bumpMinor)
	if _, err := a.db.ExecContext(r.Context(),
		`UPDATE plugins SET version = $1, updated_at = now() WHERE id = $2`, p.Version, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.materializePlugin(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	if s, err := a.loadSkillByID(r.Context(), skillID); err == nil {
		writeJSON(w, http.StatusOK, s)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListSkillVersions returns the full edit history for a skill (active or
// soft-deleted), newest first.
func (a *App) handleListSkillVersions(w http.ResponseWriter, r *http.Request) {
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

	var skillID string
	err = a.db.QueryRowContext(r.Context(), `
		SELECT id FROM skills WHERE plugin_id = $1 AND name = $2
		ORDER BY (deleted_at IS NULL) DESC, updated_at DESC LIMIT 1
	`, p.ID, skillName).Scan(&skillID)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	rows, err := a.db.QueryContext(r.Context(), `
		SELECT v.id, v.skill_id, v.version, v.action, v.name, v.description, v.body,
		       v.edited_by, u.username, v.edited_at
		FROM skill_versions v
		LEFT JOIN users u ON u.id = v.edited_by
		WHERE v.skill_id = $1
		ORDER BY v.version DESC
	`, skillID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	defer rows.Close()
	versions := []SkillVersion{}
	for rows.Next() {
		var v SkillVersion
		var editedBy, editedByName sql.NullString
		if err := rows.Scan(&v.ID, &v.SkillID, &v.Version, &v.Action, &v.Name, &v.Description, &v.Body,
			&editedBy, &editedByName, &v.EditedAt); err != nil {
			writeErr(w, http.StatusInternalServerError, "scan error")
			return
		}
		if editedBy.Valid {
			s := editedBy.String
			v.EditedBy = &s
		}
		if editedByName.Valid {
			s := editedByName.String
			v.EditedByName = &s
		}
		versions = append(versions, v)
	}
	writeJSON(w, http.StatusOK, versions)
}

// handleRevertSkill restores a skill's content (description+body) to the
// snapshot stored in skill_versions. Acts as both un-delete (if currently soft-
// deleted) and content-rollback in one operation, and writes a new version row
// of action=revert.
func (a *App) handleRevertSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	pluginName := chi.URLParam(r, "name")
	skillName := chi.URLParam(r, "skill")
	versionStr := chi.URLParam(r, "version")
	p, err := a.loadPluginByName(r.Context(), pluginName)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "plugin not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	var skillID string
	err = a.db.QueryRowContext(r.Context(), `
		SELECT id FROM skills WHERE plugin_id = $1 AND name = $2
		ORDER BY (deleted_at IS NULL) DESC, updated_at DESC LIMIT 1
	`, p.ID, skillName).Scan(&skillID)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	var (
		targetDesc, targetBody string
		targetVersion          int
	)
	err = a.db.QueryRowContext(r.Context(), `
		SELECT version, description, body FROM skill_versions
		WHERE skill_id = $1 AND version = $2
	`, skillID, versionStr).Scan(&targetVersion, &targetDesc, &targetBody)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "version not found")
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	if _, err := a.db.ExecContext(r.Context(), `
		UPDATE skills SET description = $1, body = $2, updated_at = now(), updated_by = $3,
		                  deleted_at = NULL, deleted_by = NULL
		WHERE id = $4
	`, targetDesc, targetBody, user.ID, skillID); err != nil {
		if strings.Contains(err.Error(), "duplicate") || strings.Contains(err.Error(), "unique") {
			writeErr(w, http.StatusConflict, "an active skill with that name already exists")
			return
		}
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), a.db, skillID, "revert", skillName, targetDesc, targetBody, user.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	p.Version = bumpVersion(p.Version, bumpPatch)
	if _, err := a.db.ExecContext(r.Context(),
		`UPDATE plugins SET version = $1, updated_at = now() WHERE id = $2`, p.Version, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.materializePlugin(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	if s, err := a.loadSkillByID(r.Context(), skillID); err == nil {
		writeJSON(w, http.StatusOK, s)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
