package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// A locked skill is withdrawn from every machine-facing surface (internal git,
// the external git mirror, and the MCP server) but stays visible — flagged as
// locked — in the web UI. A lock is set either manually by an admin or
// automatically by the security audit when a skill scores over the alert
// threshold. Only an admin can set or clear a lock; the lock/unlock routes are
// gated behind requireAdminMiddleware. The git/MCP withdrawal lives in
// renderPluginInto (git + external mirror) and the MCP resolvers (mcp.go); the
// helpers here own the state transitions and the REST mutation guard.

// rejectIfLocked writes a 403 and returns true when the skill is locked, so a
// REST mutation handler can bail before changing a withdrawn skill. Reads are
// intentionally unaffected — a locked skill must stay viewable in the UI.
func rejectIfLocked(w http.ResponseWriter, s *Skill) bool {
	if s.Locked {
		writeErr(w, http.StatusForbidden,
			"skill is locked — an admin must unlock it before it can be changed")
		return true
	}
	return false
}

// rejectIfLockedForNonAdmin is like rejectIfLocked but lets an admin proceed.
// An admin may delete a locked skill outright — e.g. to purge one the audit
// auto-locked — without first unlocking it; a non-admin still gets the 403.
// Reserved for removal (delete): content-mutating handlers use rejectIfLocked
// so a withdrawn skill can't be edited back into a published state.
func rejectIfLockedForNonAdmin(w http.ResponseWriter, s *Skill, user *User) bool {
	if user != nil && user.IsAdmin {
		return false
	}
	return rejectIfLocked(w, s)
}

type lockSkillReq struct {
	Reason string `json:"reason"`
}

// handleLockSkill locks a skill (admin-only). An admin lock overrides any prior
// audit lock and clears the audit suppression flag so the audit owns it again
// only after a future admin unlock. Re-materializes the plugin so the skill
// drops out of git and the external mirror immediately.
func (a *App) handleLockSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	s := a.loadActiveSkillOrRespond(w, r, p.ID)
	if s == nil {
		return
	}

	// Reason is optional; an empty body is fine.
	var req lockSkillReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	reason := strings.TrimSpace(req.Reason)

	if _, err := a.DB.ExecContext(r.Context(), `
		UPDATE skills
		SET locked_at = now(), locked_by = $1, lock_source = 'admin',
		    lock_reason = $2, audit_lock_suppressed = FALSE
		WHERE id = $3
	`, user.ID, reason, s.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	if locked, err := a.loadSkillByID(r.Context(), s.ID); err == nil {
		writeJSON(w, http.StatusOK, locked)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleUnlockSkill clears a lock (admin-only). When the lock was applied by the
// audit, unlocking sets audit_lock_suppressed so a later sweep won't re-lock the
// same skill even if it still scores over the threshold — the admin unlock is an
// acknowledgement. Re-materializes the plugin so the skill returns to git and
// the external mirror.
func (a *App) handleUnlockSkill(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	s := a.loadActiveSkillOrRespond(w, r, p.ID)
	if s == nil {
		return
	}
	if !s.Locked {
		writeErr(w, http.StatusConflict, "skill is not locked")
		return
	}

	wasAudit := s.LockSource != nil && *s.LockSource == "audit"
	if _, err := a.DB.ExecContext(r.Context(), `
		UPDATE skills
		SET locked_at = NULL, locked_by = NULL, lock_source = NULL, lock_reason = '',
		    audit_lock_suppressed = (audit_lock_suppressed OR $1)
		WHERE id = $2
	`, wasAudit, s.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	if unlocked, err := a.loadSkillByID(r.Context(), s.ID); err == nil {
		writeJSON(w, http.StatusOK, unlocked)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// autoLockSkill applies an audit lock to a single skill, used by the scheduled
// sweep. The WHERE clause makes it a no-op for a skill that is already locked or
// whose audit lock an admin has suppressed, so it never fights a manual lock or
// re-locks an acknowledged finding. Reports whether a row was actually locked so
// the caller knows it must re-materialize the owning plugin.
func (a *App) autoLockSkill(ctx context.Context, skillID, reason string) (bool, error) {
	res, err := a.DB.ExecContext(ctx, `
		UPDATE skills
		SET locked_at = now(), locked_by = NULL, lock_source = 'audit', lock_reason = $1
		WHERE id = $2 AND locked_at IS NULL AND audit_lock_suppressed = FALSE
	`, reason, skillID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}
