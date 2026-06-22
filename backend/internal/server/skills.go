package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"marketplace/internal/db"
	"marketplace/internal/metrics"
	"marketplace/internal/semver"
)

type skillReq struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	Body             string  `json:"body"`
	ExtraFrontmatter *string `json:"extraFrontmatter,omitempty"`
}

type moveSkillReq struct {
	TargetPlugin string `json:"targetPlugin"`
}

// loadSkillsForPlugin returns active (non-soft-deleted) skills with audit metadata.
func (a *App) loadSkillsForPlugin(ctx context.Context, pluginID string) ([]Skill, error) {
	return a.querySkills(ctx, `
		SELECT s.id, s.plugin_id, s.name, s.description, s.body, s.extra_frontmatter,
		       s.created_at, s.updated_at,
		       s.created_by, cu.username,
		       s.updated_by, uu.username,
		       s.deleted_at, s.deleted_by, du.username,
		       s.locked_at, s.locked_by, lu.username, s.lock_source, s.lock_reason
		FROM skills s
		LEFT JOIN users cu ON cu.id = s.created_by
		LEFT JOIN users uu ON uu.id = s.updated_by
		LEFT JOIN users du ON du.id = s.deleted_by
		LEFT JOIN users lu ON lu.id = s.locked_by
		WHERE s.plugin_id = $1 AND s.deleted_at IS NULL
		ORDER BY s.name ASC
	`, pluginID)
}

// loadDeletedSkillsForPlugin returns soft-deleted skills, used by the restore UI.
func (a *App) loadDeletedSkillsForPlugin(ctx context.Context, pluginID string) ([]Skill, error) {
	return a.querySkills(ctx, `
		SELECT s.id, s.plugin_id, s.name, s.description, s.body, s.extra_frontmatter,
		       s.created_at, s.updated_at,
		       s.created_by, cu.username,
		       s.updated_by, uu.username,
		       s.deleted_at, s.deleted_by, du.username,
		       s.locked_at, s.locked_by, lu.username, s.lock_source, s.lock_reason
		FROM skills s
		LEFT JOIN users cu ON cu.id = s.created_by
		LEFT JOIN users uu ON uu.id = s.updated_by
		LEFT JOIN users du ON du.id = s.deleted_by
		LEFT JOIN users lu ON lu.id = s.locked_by
		WHERE s.plugin_id = $1 AND s.deleted_at IS NOT NULL
		ORDER BY s.deleted_at DESC
	`, pluginID)
}

func (a *App) querySkills(ctx context.Context, query string, args ...interface{}) ([]Skill, error) {
	rows, err := a.DB.QueryContext(ctx, query, args...)
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
		var lockedAt sql.NullTime
		var lockedBy, lockedByName, lockSource sql.NullString
		if err := rows.Scan(&s.ID, &s.PluginID, &s.Name, &s.Description, &s.Body, &s.ExtraFrontmatter,
			&s.CreatedAt, &s.UpdatedAt,
			&createdBy, &createdByName,
			&updatedBy, &updatedByName,
			&deletedAt, &deletedBy, &deletedByName,
			&lockedAt, &lockedBy, &lockedByName, &lockSource, &s.LockReason); err != nil {
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
		if lockedAt.Valid {
			t := lockedAt.Time
			s.LockedAt = &t
			s.Locked = true
		}
		if lockedBy.Valid {
			v := lockedBy.String
			s.LockedBy = &v
		}
		if lockedByName.Valid {
			v := lockedByName.String
			s.LockedByName = &v
		}
		if lockSource.Valid {
			v := lockSource.String
			s.LockSource = &v
		}
		skills = append(skills, s)
	}
	return skills, rows.Err()
}

// loadActiveSkill fetches a single non-deleted skill by (plugin, name).
func (a *App) loadActiveSkill(ctx context.Context, pluginID, name string) (*Skill, error) {
	skills, err := a.querySkills(ctx, `
		SELECT s.id, s.plugin_id, s.name, s.description, s.body, s.extra_frontmatter,
		       s.created_at, s.updated_at,
		       s.created_by, cu.username,
		       s.updated_by, uu.username,
		       s.deleted_at, s.deleted_by, du.username,
		       s.locked_at, s.locked_by, lu.username, s.lock_source, s.lock_reason
		FROM skills s
		LEFT JOIN users cu ON cu.id = s.created_by
		LEFT JOIN users uu ON uu.id = s.updated_by
		LEFT JOIN users du ON du.id = s.deleted_by
		LEFT JOIN users lu ON lu.id = s.locked_by
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
		SELECT s.id, s.plugin_id, s.name, s.description, s.body, s.extra_frontmatter,
		       s.created_at, s.updated_at,
		       s.created_by, cu.username,
		       s.updated_by, uu.username,
		       s.deleted_at, s.deleted_by, du.username,
		       s.locked_at, s.locked_by, lu.username, s.lock_source, s.lock_reason
		FROM skills s
		LEFT JOIN users cu ON cu.id = s.created_by
		LEFT JOIN users uu ON uu.id = s.updated_by
		LEFT JOIN users du ON du.id = s.deleted_by
		LEFT JOIN users lu ON lu.id = s.locked_by
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
// auto-incrementing the per-skill version number, and snapshots the current
// skill_files tree into skill_file_versions so revert can restore both halves
// of the skill (description+body and supporting files) atomically.
func (a *App) recordSkillVersion(ctx context.Context, tx db.Exec, skillID, action, name, description, body, extraFrontmatter string, editedBy string) error {
	var nextVersion int
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(version), 0) + 1 FROM skill_versions WHERE skill_id = $1`, skillID).
		Scan(&nextVersion); err != nil {
		return err
	}
	var versionID string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO skill_versions (skill_id, version, action, name, description, body, extra_frontmatter, edited_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id
	`, skillID, nextVersion, action, name, description, body, extraFrontmatter, editedBy).Scan(&versionID); err != nil {
		return err
	}
	return snapshotSkillFiles(ctx, tx, versionID, skillID)
}

// loadActiveSkillOrRespond fetches the skill named by the URL :skill param
// inside the given plugin, with the same respond-and-return-nil contract.
func (a *App) loadActiveSkillOrRespond(w http.ResponseWriter, r *http.Request, pluginID string) *Skill {
	s, err := a.loadActiveSkill(r.Context(), pluginID, chi.URLParam(r, "skill"))
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "skill not found")
		return nil
	}
	if err != nil {
		serverErr(w, r, err, "db error")
		return nil
	}
	return s
}

// pluginSkillCount returns the number of skills (including soft-deleted) ever
// stored for a plugin. Used to decide whether the next skill add is the
// "first" one (no version bump) or a subsequent one.
func (a *App) pluginSkillCount(ctx context.Context, pluginID string) (int, error) {
	var n int
	err := a.DB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM skills WHERE plugin_id = $1`, pluginID).Scan(&n)
	return n, err
}

func (a *App) handleCreateSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
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

	priorSkillCount, err := a.pluginSkillCount(r.Context(), p.ID)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}

	extra := ""
	if req.ExtraFrontmatter != nil {
		extra = *req.ExtraFrontmatter
	}

	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	var id string
	err = tx.QueryRowContext(r.Context(), `
		INSERT INTO skills (plugin_id, name, description, body, extra_frontmatter, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id
	`, p.ID, req.Name, req.Description, req.Body, extra, user.ID).Scan(&id)
	if err != nil {
		respondDBOrConflict(w, r, err, "skill with that name already exists")
		return
	}
	if err := a.recordSkillVersion(r.Context(), tx, id, "create", req.Name, req.Description, req.Body, extra, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	// First skill in the plugin: no version bump (the plugin's initial
	// version is its debut version), but still advance updated_at so listings
	// re-sort. Subsequent additions bump major.
	if priorSkillCount == 0 {
		if err := a.touchPluginUpdatedAt(r.Context(), tx, p.ID); err != nil {
			serverErr(w, r, err, "db error")
			return
		}
	} else {
		if err := a.bumpAndPersistPluginVersion(r.Context(), tx, p, semver.BumpMajor); err != nil {
			serverErr(w, r, err, "db error")
			return
		}
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	metrics.SkillMutationsTotal.WithLabelValues("create", "success").Inc()
	if s, err := a.loadSkillByID(r.Context(), id); err == nil {
		writeJSON(w, http.StatusOK, s)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}

func (a *App) handleUpdateSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}

	var req skillReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}

	existing := a.loadActiveSkillOrRespond(w, r, p.ID)
	if existing == nil {
		return
	}
	if rejectIfLocked(w, existing) {
		return
	}

	// Omitting extraFrontmatter from the payload preserves the existing value;
	// explicitly sending "" clears it.
	extra := existing.ExtraFrontmatter
	if req.ExtraFrontmatter != nil {
		extra = *req.ExtraFrontmatter
	}

	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(r.Context(), `
		UPDATE skills SET description = $1, body = $2, extra_frontmatter = $3,
		                  updated_at = now(), updated_by = $4,
		                  audit_lock_suppressed = FALSE
		WHERE id = $5
	`, req.Description, req.Body, extra, user.ID, existing.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), tx, existing.ID, "update", existing.Name, req.Description, req.Body, extra, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.bumpAndPersistPluginVersion(r.Context(), tx, p, semver.BumpKindForSizeChange(len(existing.Body), len(req.Body))); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	metrics.SkillMutationsTotal.WithLabelValues("update", "success").Inc()
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleDeleteSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	existing := a.loadActiveSkillOrRespond(w, r, p.ID)
	if existing == nil {
		return
	}
	if rejectIfLocked(w, existing) {
		return
	}

	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(r.Context(), `
		UPDATE skills SET deleted_at = now(), deleted_by = $1, updated_at = now(), updated_by = $1
		WHERE id = $2
	`, user.ID, existing.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), tx, existing.ID, "delete", existing.Name, existing.Description, existing.Body, existing.ExtraFrontmatter, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.bumpAndPersistPluginVersion(r.Context(), tx, p, semver.BumpMajor); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	metrics.SkillMutationsTotal.WithLabelValues("delete", "success").Inc()
	w.WriteHeader(http.StatusNoContent)
}

// handleMoveSkill relocates a skill from its current plugin to another one. The
// skill row keeps its id, so its attached files and version history (both keyed
// off skill_id) travel with it — only plugin_id changes. Both the source and
// target plugins bump major and re-materialize, since each one's published
// skill set changed. This is a client-visible relocation: anything referencing
// the skill at <source>/<name> must switch to <target>/<name>, which is why the
// UI gates it behind an explicit confirmation.
func (a *App) handleMoveSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	src := a.loadActivePluginOrRespond(w, r)
	if src == nil {
		return
	}
	skill := a.loadActiveSkillOrRespond(w, r, src.ID)
	if skill == nil {
		return
	}
	if rejectIfLocked(w, skill) {
		return
	}

	var req moveSkillReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	target := strings.TrimSpace(strings.ToLower(req.TargetPlugin))
	if target == "" {
		writeErr(w, http.StatusBadRequest, "target plugin is required")
		return
	}
	if target == src.Name {
		writeErr(w, http.StatusBadRequest, "skill is already in that plugin")
		return
	}

	dst, err := a.loadPluginByName(r.Context(), target)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "target plugin not found")
		return
	}
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}

	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(r.Context(), `
		UPDATE skills SET plugin_id = $1, updated_at = now(), updated_by = $2
		WHERE id = $3
	`, dst.ID, user.ID, skill.ID); err != nil {
		respondDBOrConflict(w, r, err, "an active skill with that name already exists in the target plugin")
		return
	}
	if err := a.recordSkillVersion(r.Context(), tx, skill.ID, "move", skill.Name, skill.Description, skill.Body, skill.ExtraFrontmatter, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	// Source lost a skill, target gained one — both published surfaces changed,
	// so bump both major within the transaction.
	if err := a.bumpAndPersistPluginVersion(r.Context(), tx, src, semver.BumpMajor); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.bumpAndPersistPluginVersion(r.Context(), tx, dst, semver.BumpMajor); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	// Regenerate both git repos so each reflects the moved skill.
	if err := a.materializePluginDetached(src); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	if err := a.materializePluginDetached(dst); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	metrics.SkillMutationsTotal.WithLabelValues("move", "success").Inc()
	if s, err := a.loadSkillByID(r.Context(), skill.ID); err == nil {
		writeJSON(w, http.StatusOK, s)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleListDeletedSkills returns soft-deleted skills for a plugin so the UI
// can offer "restore".
func (a *App) handleListDeletedSkills(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	skills, err := a.loadDeletedSkillsForPlugin(r.Context(), p.ID)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, skills)
}

// findLatestDeletedSkill returns the id/description/body/extra of the most
// recently soft-deleted skill with this name in the plugin. The "most recent"
// filter matters because the same name can be deleted multiple times across
// history.
func (a *App) findLatestDeletedSkill(ctx context.Context, pluginID, name string) (skillID, desc, body, extra string, err error) {
	err = a.DB.QueryRowContext(ctx, `
		SELECT id, description, body, extra_frontmatter FROM skills
		WHERE plugin_id = $1 AND name = $2 AND deleted_at IS NOT NULL
		ORDER BY deleted_at DESC LIMIT 1
	`, pluginID, name).Scan(&skillID, &desc, &body, &extra)
	return
}

// handleRestoreSkill un-deletes a soft-deleted skill. Fails if another active
// skill in the same plugin already uses the same name.
func (a *App) handleRestoreSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	skillName := chi.URLParam(r, "skill")

	skillID, desc, body, extra, err := a.findLatestDeletedSkill(r.Context(), p.ID, skillName)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "no deleted skill with that name")
		return
	}
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}

	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(r.Context(), `
		UPDATE skills SET deleted_at = NULL, deleted_by = NULL, updated_at = now(), updated_by = $1
		WHERE id = $2
	`, user.ID, skillID); err != nil {
		respondDBOrConflict(w, r, err, "an active skill with that name already exists")
		return
	}
	if err := a.recordSkillVersion(r.Context(), tx, skillID, "restore", skillName, desc, body, extra, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.bumpAndPersistPluginVersion(r.Context(), tx, p, semver.BumpMajor); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	metrics.SkillMutationsTotal.WithLabelValues("restore", "success").Inc()
	if s, err := a.loadSkillByID(r.Context(), skillID); err == nil {
		writeJSON(w, http.StatusOK, s)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// findSkillIDByName resolves a skill's id from (plugin, name), preferring an
// active row over a soft-deleted one and the most-recently-updated row when
// multiple match. Used by the version-history and revert endpoints, which
// need to address a skill regardless of its current deletion state.
func (a *App) findSkillIDByName(ctx context.Context, pluginID, name string) (string, error) {
	var id string
	err := a.DB.QueryRowContext(ctx, `
		SELECT id FROM skills WHERE plugin_id = $1 AND name = $2
		ORDER BY (deleted_at IS NULL) DESC, updated_at DESC LIMIT 1
	`, pluginID, name).Scan(&id)
	return id, err
}

// handleListSkillVersions returns the full edit history for a skill (active or
// soft-deleted), newest first.
func (a *App) handleListSkillVersions(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	skillID, err := a.findSkillIDByName(r.Context(), p.ID, chi.URLParam(r, "skill"))
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}

	rows, err := a.DB.QueryContext(r.Context(), `
		SELECT v.id, v.skill_id, v.version, v.action, v.name, v.description, v.body, v.extra_frontmatter,
		       v.edited_by, u.username, v.edited_at
		FROM skill_versions v
		LEFT JOIN users u ON u.id = v.edited_by
		WHERE v.skill_id = $1
		ORDER BY v.version DESC
	`, skillID)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	defer rows.Close()
	versions := []SkillVersion{}
	for rows.Next() {
		var v SkillVersion
		var editedBy, editedByName sql.NullString
		if err := rows.Scan(&v.ID, &v.SkillID, &v.Version, &v.Action, &v.Name, &v.Description, &v.Body, &v.ExtraFrontmatter,
			&editedBy, &editedByName, &v.EditedAt); err != nil {
			serverErr(w, r, err, "scan error")
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
	if err := rows.Err(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, versions)
}

// handleRevertSkill restores a skill's content (description+body) to the
// snapshot stored in skill_versions. Acts as both un-delete (if currently soft-
// deleted) and content-rollback in one operation, and writes a new version row
// of action=revert.
//
// loadSkillVersionSnapshot fetches the row id, description, body, and extra
// frontmatter of a specific skill_versions entry. The id is used to look up
// the paired skill_file_versions snapshot when reverting.
func (a *App) loadSkillVersionSnapshot(ctx context.Context, skillID, version string) (versionID, desc, body, extra string, err error) {
	err = a.DB.QueryRowContext(ctx, `
		SELECT id, description, body, extra_frontmatter FROM skill_versions
		WHERE skill_id = $1 AND version = $2
	`, skillID, version).Scan(&versionID, &desc, &body, &extra)
	return
}

func (a *App) handleRevertSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	skillName := chi.URLParam(r, "skill")
	versionStr := chi.URLParam(r, "version")

	skillID, err := a.findSkillIDByName(r.Context(), p.ID, skillName)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "skill not found")
		return
	}
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if sk, err := a.loadSkillByID(r.Context(), skillID); err == nil && rejectIfLocked(w, sk) {
		return
	}

	targetVersionID, targetDesc, targetBody, targetExtra, err := a.loadSkillVersionSnapshot(r.Context(), skillID, versionStr)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "version not found")
		return
	}
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}

	var currentBody string
	if err := a.DB.QueryRowContext(r.Context(),
		`SELECT body FROM skills WHERE id = $1`, skillID).Scan(&currentBody); err != nil {
		serverErr(w, r, err, "db error")
		return
	}

	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(r.Context(), `
		UPDATE skills SET description = $1, body = $2, extra_frontmatter = $3,
		                  updated_at = now(), updated_by = $4,
		                  deleted_at = NULL, deleted_by = NULL
		WHERE id = $5
	`, targetDesc, targetBody, targetExtra, user.ID, skillID); err != nil {
		respondDBOrConflict(w, r, err, "an active skill with that name already exists")
		return
	}
	// Restore the file tree from the snapshot before recording the new version,
	// so the new "revert" version row captures the just-restored state.
	if err := restoreSkillFilesFromVersion(r.Context(), tx, skillID, targetVersionID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), tx, skillID, "revert", skillName, targetDesc, targetBody, targetExtra, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.bumpAndPersistPluginVersion(r.Context(), tx, p, semver.BumpKindForSizeChange(len(currentBody), len(targetBody))); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	metrics.SkillMutationsTotal.WithLabelValues("revert", "success").Inc()
	if s, err := a.loadSkillByID(r.Context(), skillID); err == nil {
		writeJSON(w, http.StatusOK, s)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
