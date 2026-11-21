package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

// Agent locks follow the skill lock contract (see skill_lock.go): a locked
// agent is withdrawn from git, the external mirror and MCP, but stays visible —
// flagged as locked — in the web UI. A lock is set by an admin or automatically
// by the security audit; only an admin can set or clear one.

// rejectIfAgentLocked writes a 403 and returns true when the agent is locked.
func rejectIfAgentLocked(w http.ResponseWriter, ag *Agent) bool {
	if ag.Locked {
		writeErr(w, http.StatusForbidden,
			"agent is locked — an admin must unlock it before it can be changed")
		return true
	}
	return false
}

// handleLockAgent locks an agent (admin-only) and re-materializes the plugin so
// the agent drops out of git and the external mirror immediately.
func (a *App) handleLockAgent(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	ag := a.loadAgentOrRespond(w, r, p.ID)
	if ag == nil {
		return
	}

	// Reason is optional; an empty body is fine.
	var req lockSkillReq
	_ = json.NewDecoder(r.Body).Decode(&req)
	reason := strings.TrimSpace(req.Reason)

	if _, err := a.DB.ExecContext(r.Context(), `
		UPDATE agents
		SET locked_at = now(), locked_by = $1, lock_source = 'admin',
		    lock_reason = $2, audit_lock_suppressed = FALSE
		WHERE id = $3
	`, user.ID, reason, ag.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	a.respondWithAgent(w, r, p.ID, ag.Name)
}

// handleUnlockAgent clears a lock (admin-only). Unlocking an audit lock sets
// audit_lock_suppressed, so a later audit does not lock the same content again:
// the admin unlock is an acknowledgement. An edit clears that flag.
func (a *App) handleUnlockAgent(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	ag := a.loadAgentOrRespond(w, r, p.ID)
	if ag == nil {
		return
	}
	if !ag.Locked {
		writeErr(w, http.StatusConflict, "agent is not locked")
		return
	}

	wasAudit := ag.LockSource != nil && *ag.LockSource == "audit"
	if _, err := a.DB.ExecContext(r.Context(), `
		UPDATE agents
		SET locked_at = NULL, locked_by = NULL, lock_source = NULL, lock_reason = '',
		    audit_lock_suppressed = (audit_lock_suppressed OR $1)
		WHERE id = $2
	`, wasAudit, ag.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	a.respondWithAgent(w, r, p.ID, ag.Name)
}

func (a *App) respondWithAgent(w http.ResponseWriter, r *http.Request, pluginID, name string) {
	ag, err := a.loadAgent(r.Context(), pluginID, name)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, ag)
}
