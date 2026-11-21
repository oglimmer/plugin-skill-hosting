package server

import (
	"context"
	"net/http"
)

// handleListAuditResults returns the latest security-audit verdict per skill,
// ordered by risk score descending. Admin-only.
func (a *App) handleListAuditResults(w http.ResponseWriter, r *http.Request) {
	results, err := a.latestAuditResults(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load audit results")
		return
	}
	pending, err := a.listAuditPending(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not load pending changes")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":  a.Cfg.AuditEnabled,
		"onChange": a.Cfg.AuditOnChange,
		// gate: changes are published only after they pass the audit.
		"gate": a.auditGateEnabled(),
		// pending: changes that are saved but not published yet. An entry with
		// a non-empty error is held because its check failed.
		"pending":   pending,
		"threshold": a.Cfg.AuditThreshold,
		"running":   a.auditRunning.Load(),
		"results":   results,
	})
}

// handleRunAudit kicks off an on-demand audit sweep in the background and
// returns 202 immediately. Rejected with 409 if a sweep is already running or
// 400 if the feature/API key is not configured. Admin-only.
func (a *App) handleRunAudit(w http.ResponseWriter, r *http.Request) {
	if !a.Cfg.AuditEnabled {
		writeErr(w, http.StatusBadRequest, "skill audit is disabled (set AUDIT_ENABLED=true)")
		return
	}
	if a.Cfg.AnthropicAPIKey == "" {
		writeErr(w, http.StatusBadRequest, "Claude API not configured (set ANTHROPIC_API_KEY)")
		return
	}
	if a.auditRunning.Load() {
		writeErr(w, http.StatusConflict, "an audit sweep is already running")
		return
	}
	// Detached context: the sweep outlives this request. The process-lifetime
	// context isn't threaded here, so use Background — the goroutine checks
	// auditRunning and ctx.Err() defensively either way.
	go a.auditAllSkills(context.Background(), "manual")
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
}
