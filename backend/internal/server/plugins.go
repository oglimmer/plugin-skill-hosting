package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"marketplace/internal/metrics"
	"marketplace/internal/semver"
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

type updatePluginReq struct {
	Description string `json:"description"`
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
	rows, err := a.DB.QueryContext(ctx, q, args...)
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

// loadActivePluginOrRespond fetches the plugin named by the URL :name param.
// If the plugin is missing or the DB fails it writes the matching HTTP error
// and returns nil; the caller bails out on a nil result.
func (a *App) loadActivePluginOrRespond(w http.ResponseWriter, r *http.Request) *Plugin {
	p, err := a.loadPluginByName(r.Context(), chi.URLParam(r, "name"))
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "plugin not found")
		return nil
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return nil
	}
	return p
}

// bumpAndPersistPluginVersion bumps p.Version in-memory and writes the new
// value (plus updated_at) to the row. The in-memory bump is what
// materializePlugin reads when it regenerates the git repo.
func (a *App) bumpAndPersistPluginVersion(ctx context.Context, p *Plugin, kind semver.BumpKind) error {
	p.Version = semver.Bump(p.Version, kind)
	_, err := a.DB.ExecContext(ctx,
		`UPDATE plugins SET version = $1, updated_at = now() WHERE id = $2`, p.Version, p.ID)
	return err
}

// touchPluginUpdatedAt advances the plugin's updated_at without changing the
// version. Used when a skill change happens that the version-bump rules
// exempt (e.g. the very first skill added to a plugin) but the listing-sort
// timestamp should still reflect the activity.
func (a *App) touchPluginUpdatedAt(ctx context.Context, pluginID string) error {
	_, err := a.DB.ExecContext(ctx,
		`UPDATE plugins SET updated_at = now() WHERE id = $1`, pluginID)
	return err
}

func (a *App) handleGetPlugin(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
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

// initialPluginVersion returns the auto-managed starting version for a newly
// created plugin. The first plugin a user creates stays at 0.1.0; every
// subsequent plugin starts with the major bumped to 1.0.0.
func (a *App) initialPluginVersion(ctx context.Context, ownerID string) (string, error) {
	var existing int
	if err := a.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM plugins WHERE owner_id = $1`, ownerID).Scan(&existing); err != nil {
		return "", err
	}
	if existing == 0 {
		return "0.1.0", nil
	}
	return "1.0.0", nil
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

	version, err := a.initialPluginVersion(r.Context(), user.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}

	var id string
	err = a.DB.QueryRowContext(r.Context(), `
		INSERT INTO plugins (owner_id, name, description, version, author_name, author_email, homepage, license)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id
	`, user.ID, req.Name, req.Description, version, req.AuthorName, req.AuthorEmail, req.Homepage, req.License).Scan(&id)
	if err != nil {
		respondDBOrConflict(w, err, "plugin name already taken")
		return
	}

	p, _ := a.loadPluginByName(r.Context(), req.Name)
	if p != nil {
		if err := a.materializePlugin(r.Context(), p); err != nil {
			writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
			return
		}
	}
	metrics.PluginMutationsTotal.WithLabelValues("create", "success").Inc()
	writeJSON(w, http.StatusOK, p)
}

// handleUpdatePlugin lets the owner change the editable metadata fields
// (everything except name). The plugin's git repo is re-materialized so the
// generated marketplace.json and README pick up the new values immediately.
func (a *App) handleUpdatePlugin(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	if p.OwnerID != user.ID {
		writeErr(w, http.StatusForbidden, "not your plugin")
		return
	}
	var req updatePluginReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	req.Description = strings.TrimSpace(req.Description)
	if req.Description == "" {
		writeErr(w, http.StatusBadRequest, "description is required")
		return
	}
	if _, err := a.DB.ExecContext(r.Context(), `
		UPDATE plugins
		   SET description = $1,
		       author_name = $2, author_email = $3,
		       homepage = $4, license = $5,
		       updated_at = now()
		 WHERE id = $6
	`, req.Description, req.AuthorName, req.AuthorEmail, req.Homepage, req.License, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	updated, err := a.loadPluginByName(r.Context(), p.Name)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	if err := a.materializePlugin(r.Context(), updated); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	skills, err := a.loadSkillsForPlugin(r.Context(), updated.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	updated.Skills = skills
	metrics.PluginMutationsTotal.WithLabelValues("update", "success").Inc()
	writeJSON(w, http.StatusOK, updated)
}

// handleDeletePlugin soft-deletes the plugin: the row stays in the database but
// the plugin is hidden from listings, the marketplace feed, and `git clone`
// (the bare repo is wiped on disk and re-materialized on restore).
func (a *App) handleDeletePlugin(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	if p.OwnerID != user.ID {
		writeErr(w, http.StatusForbidden, "not your plugin")
		return
	}
	if _, err := a.DB.ExecContext(r.Context(), `
		UPDATE plugins SET deleted_at = now(), deleted_by = $1, updated_at = now()
		WHERE id = $2
	`, user.ID, p.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	a.removeRepo(p.Name)
	metrics.PluginMutationsTotal.WithLabelValues("delete", "success").Inc()
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
	if _, err := a.DB.ExecContext(r.Context(), `
		UPDATE plugins SET deleted_at = NULL, deleted_by = NULL, updated_at = now()
		WHERE id = $1
	`, p.ID); err != nil {
		respondDBOrConflict(w, err, "an active plugin with that name already exists")
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
	metrics.PluginMutationsTotal.WithLabelValues("restore", "success").Inc()
	writeJSON(w, http.StatusOK, restored)
}
