package server

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"marketplace/internal/db"
)

// The audit gate: check first, publish after.
//
// With on-change auditing enabled (AUDIT_ON_CHANGE=true and an API key), a new
// or changed skill/agent is stored but not published until the security audit
// has passed it:
//
//  1. The write marks the item "audit pending" in the same transaction as the
//     content change (markAuditPending).
//  2. Rendering (renderPluginInto) never renders a pending item from the
//     database. It carries the last published copy over instead, so git, the
//     external mirror and Claude Code keep the old version — or nothing, for a
//     new item. Everything else in the plugin publishes as usual.
//  3. The audit runs in the background. A clean verdict clears the pending
//     state and re-publishes the plugin; a harmful one locks the item.
//  4. If the check itself fails (the model API is down, the reply cannot be
//     parsed) the item stays pending — it is held, fail closed. A retry loop
//     tries again, admins are alerted once, and an admin can approve the change
//     by hand.
//
// With the gate off nothing is ever marked pending and publishing is immediate,
// exactly as before.

var (
	// auditPendingGrace is how long an item must have been pending before the
	// retry loop touches it. The on-change audit normally settles an item well
	// inside this window; the loop is for what that missed (a restart mid-check,
	// a failed check).
	auditPendingGrace = 2 * time.Minute
	// auditRetryInterval is the minimum gap between two check attempts for the
	// same item, so an API outage is not hammered.
	auditRetryInterval = 5 * time.Minute
	// auditRetryTick is how often the retry loop looks for work.
	auditRetryTick = time.Minute
)

// auditGateEnabled reports whether changes are held back until audited. It is
// the same condition as on-change auditing: without the audit there is nothing
// that could ever release a pending item.
func (a *App) auditGateEnabled() bool {
	return a.auditOnChangeEnabled()
}

// auditTable maps an audit kind to its table. The result is a constant, never
// user input, so it is safe to splice into SQL.
func auditTable(kind string) string {
	if kind == auditKindAgent {
		return "agents"
	}
	return "skills"
}

// markAuditPending flags a skill or agent as not yet audited. It must run in
// the same transaction as the content write: marking after the commit would
// leave a window in which a concurrent materialize publishes the new content.
// A fresh token is written every time, which invalidates any check still
// running against the previous content. No-op while the gate is off.
func (a *App) markAuditPending(ctx context.Context, tx db.Exec, kind, id string) error {
	if !a.auditGateEnabled() {
		return nil
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE `+auditTable(kind)+`
		SET audit_pending_token = gen_random_uuid(), audit_pending_at = now(), audit_pending_error = ''
		WHERE id = $1
	`, id)
	return err
}

// clearAuditPending releases the item behind t for publishing, but only if the
// token t was loaded with is still current. It reports whether it did: false
// means the content changed while the check ran, and a newer check owns it.
func (a *App) clearAuditPending(ctx context.Context, t auditTarget) (bool, error) {
	res, err := a.DB.ExecContext(ctx, `
		UPDATE `+auditTable(t.Kind)+`
		SET audit_pending_token = NULL, audit_pending_at = NULL, audit_pending_error = ''
		WHERE id = $1 AND audit_pending_token = $2::uuid
	`, t.itemID(), t.PendingToken)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// setAuditPendingError records why the last check attempt failed, turning a
// pending item into a visibly held one.
func (a *App) setAuditPendingError(ctx context.Context, t auditTarget, msg string) error {
	_, err := a.DB.ExecContext(ctx, `
		UPDATE `+auditTable(t.Kind)+`
		SET audit_pending_error = $1
		WHERE id = $2 AND audit_pending_token = $3::uuid
	`, msg, t.itemID(), t.PendingToken)
	return err
}

// alertAuditHold tells the admins, once per held change, that a change could
// not be checked and is therefore not published.
func (a *App) alertAuditHold(t auditTarget, checkErr string) {
	if _, seen := a.auditHoldAlerted.LoadOrStore(t.PendingToken, struct{}{}); seen {
		return
	}
	subject := fmt.Sprintf("[%s] Skill audit: a change to %s is held", a.Cfg.MarketplaceName, t.label())
	var sb strings.Builder
	fmt.Fprintf(&sb, "A change to %s on %s could not be security-checked, so it is NOT published.\n\n", t.label(), a.Cfg.MarketplaceName)
	fmt.Fprintf(&sb, "Reason: %s\n\n", checkErr)
	sb.WriteString("The last published version stays live. The check is retried automatically.\n")
	sb.WriteString("An admin can publish the change without a check on the plugin page.\n")
	if base := strings.TrimRight(a.Cfg.PublicBaseURL, "/"); base != "" {
		fmt.Fprintf(&sb, "\n%s/plugins/%s\n", base, t.PluginName)
	}
	body := sb.String()

	if !a.Email.Configured() || len(a.Cfg.AuditAlertEmails) == 0 {
		log.Printf("skill audit hold (email not sent — %s):\n%s",
			emailDisabledReason(a.Email.Configured(), len(a.Cfg.AuditAlertEmails)), body)
		return
	}
	if err := a.Email.Send(a.Cfg.AuditAlertEmails, subject, body); err != nil {
		log.Printf("ERROR: skill audit: send hold alert: %v\nalert body:\n%s", err, body)
	}
}

// StartAuditGate starts the background half of the gate. With the gate on, a
// loop re-checks items that are still pending after auditPendingGrace — the
// ones whose on-change check was lost to a restart or failed. With the gate
// off, it releases anything left pending from a time when it was on, so
// switching the feature off can never strand a change unpublished.
func (a *App) StartAuditGate(ctx context.Context, wg *sync.WaitGroup) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		if !a.auditGateEnabled() {
			a.releaseAllAuditPending(ctx)
			return
		}
		log.Printf("audit gate enabled: changes to skills and agents are published only after the security check passes")
		ticker := time.NewTicker(auditRetryTick)
		defer ticker.Stop()
		for {
			a.retryPendingAudits(ctx)
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// retryPendingAudits runs one pass of the retry loop: every item that has been
// pending longer than the grace period, and was not attempted recently, is
// checked again. Sequential on purpose — this is the slow path.
func (a *App) retryPendingAudits(ctx context.Context) {
	cutoff := time.Now().Add(-auditPendingGrace)
	skills, err := a.loadPendingSkillAuditTargets(ctx, cutoff)
	if err != nil {
		log.Printf("ERROR: audit gate: load pending skills: %v", err)
		return
	}
	agents, err := a.loadPendingAgentAuditTargets(ctx, cutoff)
	if err != nil {
		log.Printf("ERROR: audit gate: load pending agents: %v", err)
		return
	}
	for _, t := range append(skills, agents...) {
		if ctx.Err() != nil {
			return
		}
		if last, ok := a.auditAttempts.Load(t.key()); ok && time.Since(last.(time.Time)) < auditRetryInterval {
			continue
		}
		a.auditSingleTarget(ctx, t, "retry")
	}
}

// releaseAllAuditPending clears every pending marker and re-publishes the
// affected plugins. Used at startup when the gate is off.
func (a *App) releaseAllAuditPending(ctx context.Context) {
	pluginIDs := map[string]struct{}{}
	for _, table := range []string{"skills", "agents"} {
		rows, err := a.DB.QueryContext(ctx, `
			UPDATE `+table+`
			SET audit_pending_token = NULL, audit_pending_at = NULL, audit_pending_error = ''
			WHERE audit_pending_token IS NOT NULL
			RETURNING plugin_id
		`)
		if err != nil {
			log.Printf("ERROR: audit gate: release pending %s: %v", table, err)
			continue
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err == nil {
				pluginIDs[id] = struct{}{}
			}
		}
		if err := rows.Err(); err != nil {
			log.Printf("ERROR: audit gate: release pending %s: %v", table, err)
		}
		rows.Close()
	}
	if len(pluginIDs) == 0 {
		return
	}
	log.Printf("audit gate is off: releasing held changes in %d plugin(s)", len(pluginIDs))
	for id := range pluginIDs {
		plugins, err := a.queryPlugins(ctx, `WHERE p.id = $1 AND p.deleted_at IS NULL`, id)
		if err != nil || len(plugins) == 0 {
			continue
		}
		if err := a.materializePlugin(ctx, &plugins[0]); err != nil {
			log.Printf("ERROR: audit gate: publish plugin %q after release: %v", plugins[0].Name, err)
		}
	}
}

// approveAuditPending is the admin override: publish a pending or held item
// without a passed check. Reports false when the item was not pending.
func (a *App) approveAuditPending(ctx context.Context, kind, id string) (bool, error) {
	res, err := a.DB.ExecContext(ctx, `
		UPDATE `+auditTable(kind)+`
		SET audit_pending_token = NULL, audit_pending_at = NULL, audit_pending_error = ''
		WHERE id = $1 AND audit_pending_token IS NOT NULL
	`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

// handleApproveSkillAudit publishes a skill's pending change without waiting
// for the security check. Admin-only (gated in the router).
func (a *App) handleApproveSkillAudit(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	s := a.loadActiveSkillOrRespond(w, r, p.ID)
	if s == nil {
		return
	}
	if !a.approveOrRespond(w, r, p, auditKindSkill, s.ID, "skill") {
		return
	}
	log.Printf("audit gate: admin %s approved pending skill %s/%s without a check", currentUser(r).Username, p.Name, s.Name)
	if updated, err := a.loadSkillByID(r.Context(), s.ID); err == nil {
		writeJSON(w, http.StatusOK, updated)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleApproveAgentAudit is handleApproveSkillAudit for an agent.
func (a *App) handleApproveAgentAudit(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	ag := a.loadAgentOrRespond(w, r, p.ID)
	if ag == nil {
		return
	}
	if !a.approveOrRespond(w, r, p, auditKindAgent, ag.ID, "agent") {
		return
	}
	log.Printf("audit gate: admin %s approved pending agent %s/%s without a check", currentUser(r).Username, p.Name, ag.Name)
	a.respondWithAgent(w, r, p.ID, ag.Name)
}

// approveOrRespond clears the pending state and re-publishes the plugin. On
// failure it writes the HTTP error and returns false.
func (a *App) approveOrRespond(w http.ResponseWriter, r *http.Request, p *Plugin, kind, id, noun string) bool {
	ok, err := a.approveAuditPending(r.Context(), kind, id)
	if err != nil {
		serverErr(w, r, err, "db error")
		return false
	}
	if !ok {
		writeErr(w, http.StatusConflict, noun+" has no pending change")
		return false
	}
	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return false
	}
	return true
}

// publishNote is appended to MCP write results so the caller knows the change
// is saved but not live yet. Empty while the gate is off.
func (a *App) publishNote() string {
	if !a.auditGateEnabled() {
		return ""
	}
	return " — saved, not published yet: it goes live after the security check passes"
}

// AuditPendingItem is one saved-but-unpublished change, for the admin audit view.
type AuditPendingItem struct {
	Kind       string    `json:"kind"` // "skill" or "agent"
	PluginName string    `json:"pluginName"`
	Name       string    `json:"name"`
	Since      time.Time `json:"since"`
	// Error is non-empty when the last check attempt failed: the change is held.
	Error string `json:"error,omitempty"`
}

// listAuditPending returns every change waiting on the audit gate, oldest first.
func (a *App) listAuditPending(ctx context.Context) ([]AuditPendingItem, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT 'skill', p.name, s.name, s.audit_pending_at, s.audit_pending_error
		FROM skills s JOIN plugins p ON p.id = s.plugin_id
		WHERE s.audit_pending_token IS NOT NULL AND s.deleted_at IS NULL AND p.deleted_at IS NULL
		UNION ALL
		SELECT 'agent', p.name, g.name, g.audit_pending_at, g.audit_pending_error
		FROM agents g JOIN plugins p ON p.id = g.plugin_id
		WHERE g.audit_pending_token IS NOT NULL AND p.deleted_at IS NULL
		ORDER BY 4
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditPendingItem{}
	for rows.Next() {
		var it AuditPendingItem
		if err := rows.Scan(&it.Kind, &it.PluginName, &it.Name, &it.Since, &it.Error); err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
