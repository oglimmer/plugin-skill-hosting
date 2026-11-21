package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"time"

	"marketplace/internal/metrics"
	"marketplace/internal/skillvalidation"
)

// auditFileContentCap bounds how much of each supporting file's text we send
// to the auditor, so a single huge file can't blow the token budget. Malicious
// payloads are typically small; this keeps the call affordable while still
// surfacing the dangerous bits.
const auditFileContentCap = 8000

// auditMaxTokens is the response budget for one skill audit. The verdict JSON
// is small (score + a handful of findings), so this is generous headroom.
const auditMaxTokens = 2048

// AuditResult is the API/UI shape for one stored audit verdict, joined with the
// owning plugin and skill names for display.
//
// Kind says what was audited: "skill" or "agent". A skill result fills
// SkillID/SkillName; an agent result fills AgentID/AgentName instead.
type AuditResult struct {
	Kind       string                         `json:"kind"`
	SkillID    string                         `json:"skillId,omitempty"`
	PluginName string                         `json:"pluginName"`
	SkillName  string                         `json:"skillName,omitempty"`
	AgentID    string                         `json:"agentId,omitempty"`
	AgentName  string                         `json:"agentName,omitempty"`
	AuditedAt  time.Time                      `json:"auditedAt"`
	Model      string                         `json:"model"`
	RiskScore  int                            `json:"riskScore"`
	RiskLevel  string                         `json:"riskLevel"`
	Categories []string                       `json:"categories"`
	Summary    string                         `json:"summary"`
	Findings   []skillvalidation.AuditFinding `json:"findings"`
	Error      string                         `json:"error,omitempty"`
}

// Audit kinds. The zero value of auditTarget.Kind means a skill.
const (
	auditKindSkill = "skill"
	auditKindAgent = "agent"
)

// displayName is the "<plugin> / <item>" label used in alerts. Agents are
// marked so a reader can tell them from a skill with the same name.
func (r AuditResult) displayName() string {
	if r.Kind == auditKindAgent {
		return r.PluginName + " / agent " + r.AgentName
	}
	return r.PluginName + " / " + r.SkillName
}

// auditTarget is a skill or an agent selected for auditing, carrying just the
// fields the audit prompt and storage need.
type auditTarget struct {
	// Kind is auditKindAgent for an agent; anything else means a skill.
	Kind        string
	SkillID     string
	PluginName  string
	SkillName   string
	Description string
	Body        string
	// Agent-only fields.
	AgentID          string
	AgentName        string
	Model            string
	Tools            string
	ExtraFrontmatter string
	// PendingToken is the item's audit-gate token as of when this target was
	// loaded; "" when the item is not pending. A verdict may only release or
	// lock the item if this token is still the current one.
	PendingToken string
}

func (t auditTarget) isAgent() bool { return t.Kind == auditKindAgent }

// itemID is the id of the skill or agent behind the target.
func (t auditTarget) itemID() string {
	if t.isAgent() {
		return t.AgentID
	}
	return t.SkillID
}

// key identifies the item across both kinds (for in-memory bookkeeping).
func (t auditTarget) key() string { return auditTable(t.Kind) + ":" + t.itemID() }

// label names the target in log lines.
func (t auditTarget) label() string {
	if t.isAgent() {
		return t.PluginName + "/agent:" + t.AgentName
	}
	return t.PluginName + "/" + t.SkillName
}

// metricName is the value of the "skill" label on the per-item risk gauge.
// Agents get an "agent:" prefix so they cannot collide with a skill name.
func (t auditTarget) metricName() string {
	if t.isAgent() {
		return "agent:" + t.AgentName
	}
	return t.SkillName
}

// autoLockTarget applies the audit lock to the skill or agent behind t. Like
// autoLockSkill it is a no-op for an item that is already locked or whose audit
// lock an admin acknowledged. It is also a no-op when the item's content
// changed since t was loaded (the pending token moved): the verdict is for old
// content then, and the check of the new content decides. Reports whether a
// row was locked.
func (a *App) autoLockTarget(ctx context.Context, t auditTarget, reason string) (bool, error) {
	var token any
	if t.PendingToken != "" {
		token = t.PendingToken
	}
	res, err := a.DB.ExecContext(ctx, `
		UPDATE `+auditTable(t.Kind)+`
		SET locked_at = now(), locked_by = NULL, lock_source = 'audit', lock_reason = $1
		WHERE id = $2 AND locked_at IS NULL AND audit_lock_suppressed = FALSE
		  AND audit_pending_token IS NOT DISTINCT FROM $3::uuid
	`, reason, t.itemID(), token)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// applyAuditVerdict acts on one stored verdict: it locks a flagged item and
// settles the audit gate for a pending one. It reports whether the plugin must
// be re-published (an item was locked or released) and whether the verdict
// reached the alert threshold.
func (a *App) applyAuditVerdict(ctx context.Context, t auditTarget, res AuditResult, trigger string) (republish, flagged bool) {
	if res.Error != "" {
		// The check itself failed. A pending item stays pending — held, fail
		// closed — and the admins hear about it once.
		if t.PendingToken != "" {
			if err := a.setAuditPendingError(ctx, t, res.Error); err != nil {
				log.Printf("ERROR: skill audit (%s): record hold for %s: %v", trigger, t.label(), err)
			}
			log.Printf("skill audit (%s): check failed, change to %s is held unpublished: %s", trigger, t.label(), res.Error)
			a.alertAuditHold(t, res.Error)
		}
		return false, false
	}

	flagged = res.RiskScore >= a.Cfg.AuditThreshold
	if flagged {
		reason := res.Summary
		if reason == "" {
			reason = fmt.Sprintf("auto-locked by security audit: risk score %d (%s)", res.RiskScore, res.RiskLevel)
		}
		locked, err := a.autoLockTarget(ctx, t, reason)
		if err != nil {
			// Could not lock a flagged item: do not release it either.
			log.Printf("ERROR: skill audit (%s): auto-lock %s: %v", trigger, t.label(), err)
			return false, true
		}
		if locked {
			republish = true
			log.Printf("skill audit (%s): auto-locked %s (risk %d)", trigger, t.label(), res.RiskScore)
		}
	}

	if t.PendingToken != "" {
		// The verdict is in, so the gate is settled. A clean item is released
		// for publishing. A flagged one was locked just above (so it stays out
		// of git whatever its pending state), or was already locked, or an
		// admin has acknowledged this finding — in which case it publishes.
		cleared, err := a.clearAuditPending(ctx, t)
		if err != nil {
			log.Printf("ERROR: skill audit (%s): release %s: %v", trigger, t.label(), err)
		} else if cleared {
			republish = true
		}
	}
	return republish, flagged
}

// StartSkillAudit launches the scheduled security-audit goroutine. It is a
// no-op unless the feature is enabled AND an Anthropic API key is configured
// (the audit can't run without the model). On boot it audits immediately only
// if the most recent stored audit is older than one interval (or none exists),
// so frequent restarts don't re-audit on every launch; otherwise it waits for
// the next tick. Mirrors the StartOAuthGC pattern.
func (a *App) StartSkillAudit(ctx context.Context, wg *sync.WaitGroup) {
	// The audit gate rides on the on-change auditor and, like it, is
	// independent of the scheduled sweep below.
	a.StartAuditGate(ctx, wg)
	// The on-change auditor has no goroutine of its own (it fires from skill
	// mutation handlers), but log its status here alongside the scheduled one
	// so the boot log states clearly which audit paths are live. It is
	// independent of AuditEnabled, so report it before that early return.
	if a.Cfg.AuditOnChange {
		if strings.TrimSpace(a.Cfg.AnthropicAPIKey) == "" {
			log.Printf("WARN: AUDIT_ON_CHANGE=true but ANTHROPIC_API_KEY is empty — on-change audit disabled")
		} else {
			log.Printf("on-change skill audit enabled: threshold=%d", a.Cfg.AuditThreshold)
		}
	}
	if !a.Cfg.AuditEnabled {
		return
	}
	if strings.TrimSpace(a.Cfg.AnthropicAPIKey) == "" {
		log.Printf("WARN: AUDIT_ENABLED=true but ANTHROPIC_API_KEY is empty — skill audit disabled")
		return
	}
	log.Printf("skill audit enabled: interval=%s threshold=%d recipients=%d",
		a.Cfg.AuditInterval, a.Cfg.AuditThreshold, len(a.Cfg.AuditAlertEmails))

	wg.Add(1)
	go func() {
		defer wg.Done()
		if a.auditDueAtStartup(ctx) {
			a.auditAllSkills(ctx, "scheduled")
		}
		ticker := time.NewTicker(a.Cfg.AuditInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				a.auditAllSkills(ctx, "scheduled")
			}
		}
	}()
}

// auditDueAtStartup reports whether an audit should run immediately on boot:
// true when there is no prior audit or the latest one is older than the
// configured interval. A query error fails open (returns true) so a transient
// DB hiccup doesn't silently skip the first sweep.
func (a *App) auditDueAtStartup(ctx context.Context) bool {
	var last sql.NullTime
	// GREATEST ignores NULLs, so this is the newest audit of either kind.
	err := a.DB.QueryRowContext(ctx, `
		SELECT GREATEST(
			(SELECT max(audited_at) FROM skill_audit_results),
			(SELECT max(audited_at) FROM agent_audit_results))`).Scan(&last)
	if err != nil {
		log.Printf("WARN: skill audit: could not read last audit time: %v", err)
		return true
	}
	if !last.Valid {
		return true
	}
	return time.Since(last.Time) >= a.Cfg.AuditInterval
}

// auditAllSkills audits every active skill once, stores each verdict, and sends
// a single alert email summarizing skills whose score reached the threshold.
// trigger is "scheduled" or "manual" for metrics/logging. It refuses to run if
// another sweep is already in flight.
func (a *App) auditAllSkills(ctx context.Context, trigger string) {
	if !a.auditRunning.CompareAndSwap(false, true) {
		log.Printf("skill audit: a sweep is already running, skipping %s trigger", trigger)
		return
	}
	defer a.auditRunning.Store(false)

	targets, err := a.loadAuditTargets(ctx)
	if err != nil {
		log.Printf("ERROR: skill audit: load skills: %v", err)
		return
	}
	agentTargets, err := a.loadAgentAuditTargets(ctx)
	if err != nil {
		log.Printf("ERROR: skill audit: load agents: %v", err)
		return
	}
	targets = append(targets, agentTargets...)
	log.Printf("skill audit (%s): auditing %d skills and %d agents",
		trigger, len(targets)-len(agentTargets), len(agentTargets))

	// Clear last sweep's per-skill gauges so deleted/renamed skills don't linger
	// as stale series; we repopulate below for each successfully-audited skill.
	metrics.SkillAuditRiskScore.Reset()

	var flagged []AuditResult
	for _, t := range targets {
		if ctx.Err() != nil {
			log.Printf("skill audit: context cancelled, stopping sweep")
			return
		}
		res := a.auditOneSkill(ctx, t)
		if err := a.storeAuditResult(ctx, t, res); err != nil {
			log.Printf("ERROR: skill audit: store result for %s: %v", t.label(), err)
		}
		if res.Error == "" {
			metrics.SkillAuditRiskScore.
				WithLabelValues(t.PluginName, t.metricName(), res.RiskLevel).
				Set(float64(res.RiskScore))
		}
		republish, isFlagged := a.applyAuditVerdict(ctx, t, res, trigger)
		if isFlagged {
			flagged = append(flagged, res)
		}
		// The served tree changed: an item was auto-locked (it drops out of git
		// and the mirror) or a pending item was released. Rebuild right away
		// rather than at the end of the sweep, so neither waits on the audits
		// still to come and a cancelled sweep cannot skip it.
		if republish {
			a.republishPluginOf(t, trigger)
		}
	}

	metrics.SkillAuditRunsTotal.WithLabelValues(trigger).Inc()
	// Publish the sweep's flagged-skill state for monitoring — the /metrics-side
	// equivalent of the alert email, usable even when SMTP is unconfigured.
	metrics.SkillAuditFlaggedSkills.Set(float64(len(flagged)))
	metrics.SkillAuditLastRunTimestamp.SetToCurrentTime()

	if len(flagged) > 0 {
		a.sendAuditAlert(flagged)
	}
	log.Printf("skill audit (%s): done — %d skills and agents, %d at/above threshold %d",
		trigger, len(targets), len(flagged), a.Cfg.AuditThreshold)
}

// auditOnChangeEnabled reports whether the event-driven (on-change) auditor is
// active: the feature flag is set AND an Anthropic API key is configured (the
// audit can't run without the model). Independent of AuditEnabled, which only
// governs the scheduled sweep.
func (a *App) auditOnChangeEnabled() bool {
	return a.Cfg.AuditOnChange && strings.TrimSpace(a.Cfg.AnthropicAPIKey) != ""
}

// auditSkillOnChange triggers a background security audit of a single skill in
// response to a create or change (its SKILL.md/metadata or any attached file).
// It is a no-op unless AUDIT_ON_CHANGE is enabled and an API key is set, so
// callers can invoke it unconditionally after a successful mutation. The audit
// runs detached from the request: it outlives the HTTP response so the user
// isn't blocked on a ~Claude round-trip, and any failure is logged, never
// surfaced to the (already-returned) request.
func (a *App) auditSkillOnChange(skillID string) {
	if !a.auditOnChangeEnabled() {
		return
	}
	a.onChangeAudits.Add(1)
	go func() {
		defer a.onChangeAudits.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		a.auditSingleSkill(ctx, skillID, "on-change")
	}()
}

// auditSingleSkill audits one skill, stores the verdict, and — when the score
// reaches the threshold — auto-locks it, re-materializes its plugin so the
// skill is withdrawn from git/MCP/the external mirror, and alerts. It mirrors
// the per-skill body of auditAllSkills but for a single target; it does NOT
// reset the per-skill risk gauges (that would wipe other skills' series) and
// does not touch the sweep-wide flagged-skills gauge. trigger labels metrics.
func (a *App) auditSingleSkill(ctx context.Context, skillID, trigger string) {
	t, err := a.loadAuditTargetByID(ctx, skillID)
	if err != nil {
		log.Printf("ERROR: skill audit (%s): load skill %s: %v", trigger, skillID, err)
		return
	}
	a.auditSingleTarget(ctx, t, trigger)
}

// auditAgentOnChange is auditSkillOnChange for an agent: a background audit of
// one agent after it was created or changed. No-op unless AUDIT_ON_CHANGE is
// enabled and an API key is set.
func (a *App) auditAgentOnChange(agentID string) {
	if !a.auditOnChangeEnabled() {
		return
	}
	a.onChangeAudits.Add(1)
	go func() {
		defer a.onChangeAudits.Done()
		ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
		defer cancel()
		t, err := a.loadAgentAuditTargetByID(ctx, agentID)
		if err != nil {
			log.Printf("ERROR: skill audit (on-change): load agent %s: %v", agentID, err)
			return
		}
		a.auditSingleTarget(ctx, t, "on-change")
	}()
}

// auditSingleTarget is the shared body of the single-item audit, for a skill
// or an agent.
func (a *App) auditSingleTarget(ctx context.Context, t auditTarget, trigger string) {
	a.auditAttempts.Store(t.key(), time.Now())
	res := a.auditOneSkill(ctx, t)
	if err := a.storeAuditResult(ctx, t, res); err != nil {
		log.Printf("ERROR: skill audit (%s): store result for %s: %v", trigger, t.label(), err)
	}
	metrics.SkillAuditRunsTotal.WithLabelValues(trigger).Inc()
	if res.Error == "" {
		metrics.SkillAuditRiskScore.
			WithLabelValues(t.PluginName, t.metricName(), res.RiskLevel).
			Set(float64(res.RiskScore))
	}

	// Lock a flagged item and settle the audit gate, then re-publish if the
	// served tree changed: a locked item drops out of git/MCP/the mirror, a
	// released one goes live.
	republish, flagged := a.applyAuditVerdict(ctx, t, res, trigger)
	if republish {
		// Detached from ctx on purpose: the DB state is already final, so the
		// git rebuild must not be cut short by this audit's deadline.
		a.republishPluginOf(t, trigger)
	}
	if flagged {
		a.sendAuditAlert([]AuditResult{res})
	}
}

// republishPluginOf re-materializes the plugin that holds the item behind t
// right now. The plugin is looked up again by item id rather than taken from
// t.PluginName, because a skill can be moved to another plugin while its check
// runs — and it is that plugin whose served tree has to change.
func (a *App) republishPluginOf(t auditTarget, trigger string) {
	ctx := context.Background()
	plugins, err := a.queryPlugins(ctx,
		`WHERE p.deleted_at IS NULL AND p.id = (SELECT plugin_id FROM `+auditTable(t.Kind)+` WHERE id = $1)`, t.itemID())
	if err != nil {
		log.Printf("ERROR: skill audit (%s): find plugin of %s to re-materialize: %v", trigger, t.label(), err)
		return
	}
	if len(plugins) == 0 {
		return // the item or its plugin is gone; nothing to publish
	}
	if err := a.materializePluginDetached(&plugins[0]); err != nil {
		log.Printf("ERROR: skill audit (%s): re-materialize plugin %q: %v", trigger, plugins[0].Name, err)
	}
}

// skillAuditTargetSelect lists the fields the auditor needs for a skill, for
// non-deleted skills of non-deleted plugins.
const skillAuditTargetSelect = `
	SELECT s.id, p.name, s.name, s.description, s.body, s.audit_pending_token::text
	FROM skills s
	JOIN plugins p ON p.id = s.plugin_id
	WHERE s.deleted_at IS NULL AND p.deleted_at IS NULL
`

func scanSkillAuditTarget(scan func(dest ...any) error) (auditTarget, error) {
	var t auditTarget
	var token sql.NullString
	err := scan(&t.SkillID, &t.PluginName, &t.SkillName, &t.Description, &t.Body, &token)
	t.PendingToken = token.String
	return t, err
}

func collectAuditTargets(rows *sql.Rows, scan func(func(dest ...any) error) (auditTarget, error)) ([]auditTarget, error) {
	defer rows.Close()
	var out []auditTarget
	for rows.Next() {
		t, err := scan(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// loadAuditTargetByID returns the single non-deleted skill identified by id,
// with the fields the auditor needs. Returns sql.ErrNoRows if the skill (or its
// plugin) is missing or soft-deleted.
func (a *App) loadAuditTargetByID(ctx context.Context, skillID string) (auditTarget, error) {
	return scanSkillAuditTarget(a.DB.QueryRowContext(ctx, skillAuditTargetSelect+` AND s.id = $1`, skillID).Scan)
}

// loadAuditTargets returns every non-deleted skill (across all non-deleted
// plugins) with the fields the auditor needs.
func (a *App) loadAuditTargets(ctx context.Context) ([]auditTarget, error) {
	rows, err := a.DB.QueryContext(ctx, skillAuditTargetSelect+` ORDER BY p.name, s.name`)
	if err != nil {
		return nil, err
	}
	return collectAuditTargets(rows, scanSkillAuditTarget)
}

// loadPendingSkillAuditTargets returns the skills that have been audit-pending
// since before cutoff, oldest first.
func (a *App) loadPendingSkillAuditTargets(ctx context.Context, cutoff time.Time) ([]auditTarget, error) {
	rows, err := a.DB.QueryContext(ctx, skillAuditTargetSelect+`
		AND s.audit_pending_token IS NOT NULL AND s.audit_pending_at < $1
		ORDER BY s.audit_pending_at`, cutoff)
	if err != nil {
		return nil, err
	}
	return collectAuditTargets(rows, scanSkillAuditTarget)
}

// agentAuditTargetSelect lists the fields the auditor needs for an agent.
// Agents are hard-deleted, so only the plugin can be soft-deleted here.
const agentAuditTargetSelect = `
	SELECT g.id, p.name, g.name, g.description, g.body, g.model, g.tools, g.extra_frontmatter,
	       g.audit_pending_token::text
	FROM agents g
	JOIN plugins p ON p.id = g.plugin_id
	WHERE p.deleted_at IS NULL
`

func scanAgentAuditTarget(scan func(dest ...any) error) (auditTarget, error) {
	t := auditTarget{Kind: auditKindAgent}
	var token sql.NullString
	err := scan(&t.AgentID, &t.PluginName, &t.AgentName, &t.Description, &t.Body,
		&t.Model, &t.Tools, &t.ExtraFrontmatter, &token)
	t.PendingToken = token.String
	return t, err
}

// loadAgentAuditTargetByID returns one agent as an audit target, or
// sql.ErrNoRows if it (or its plugin) is gone.
func (a *App) loadAgentAuditTargetByID(ctx context.Context, agentID string) (auditTarget, error) {
	return scanAgentAuditTarget(a.DB.QueryRowContext(ctx, agentAuditTargetSelect+` AND g.id = $1`, agentID).Scan)
}

// loadAgentAuditTargets returns every agent of every non-deleted plugin.
func (a *App) loadAgentAuditTargets(ctx context.Context) ([]auditTarget, error) {
	rows, err := a.DB.QueryContext(ctx, agentAuditTargetSelect+` ORDER BY p.name, g.name`)
	if err != nil {
		return nil, err
	}
	return collectAuditTargets(rows, scanAgentAuditTarget)
}

// loadPendingAgentAuditTargets returns the agents that have been audit-pending
// since before cutoff, oldest first.
func (a *App) loadPendingAgentAuditTargets(ctx context.Context, cutoff time.Time) ([]auditTarget, error) {
	rows, err := a.DB.QueryContext(ctx, agentAuditTargetSelect+`
		AND g.audit_pending_token IS NOT NULL AND g.audit_pending_at < $1
		ORDER BY g.audit_pending_at`, cutoff)
	if err != nil {
		return nil, err
	}
	return collectAuditTargets(rows, scanAgentAuditTarget)
}

// auditOneSkill builds the prompt (including supporting-file contents), calls
// Claude, and parses the verdict. Any failure is captured in the returned
// result's Error field with a zero risk score, so the sweep keeps going.
func (a *App) auditOneSkill(ctx context.Context, t auditTarget) AuditResult {
	res := AuditResult{
		Kind:       auditKindSkill,
		SkillID:    t.SkillID,
		PluginName: t.PluginName,
		SkillName:  t.SkillName,
		Model:      a.Cfg.AnthropicModel,
		RiskLevel:  "low",
		Categories: []string{},
		Findings:   []skillvalidation.AuditFinding{},
	}

	systemPrompt := skillvalidation.SecurityAuditSystemPrompt
	var userMsg string
	if t.isAgent() {
		// An agent is a single file with no supporting files.
		res.Kind = auditKindAgent
		res.AgentID = t.AgentID
		res.AgentName = t.AgentName
		systemPrompt = skillvalidation.AgentSecurityAuditSystemPrompt
		userMsg = buildAgentAuditPromptMessage(t)
	} else {
		files, err := a.loadSkillFiles(ctx, t.SkillID)
		if err != nil {
			// Non-fatal: audit the SKILL.md alone, but note the gap.
			log.Printf("WARN: skill audit: load files for %s/%s: %v", t.PluginName, t.SkillName, err)
			files = nil
		}
		userMsg = buildAuditPromptMessage(t, files)
	}

	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	raw, err := a.callClaude(callCtx, systemPrompt, userMsg, auditMaxTokens)
	if err != nil {
		metrics.SkillAuditTotal.WithLabelValues("error").Inc()
		res.Error = err.Error()
		return res
	}
	report, err := skillvalidation.ParseAudit(raw)
	if err != nil {
		metrics.SkillAuditTotal.WithLabelValues("error").Inc()
		logClaudeParseFailure("skill-audit", raw, err)
		res.Error = "could not parse audit response: " + err.Error()
		return res
	}
	metrics.SkillAuditTotal.WithLabelValues("success").Inc()
	res.RiskScore = report.RiskScore
	res.RiskLevel = report.RiskLevel
	res.Categories = report.Categories
	res.Summary = report.Summary
	res.Findings = report.Findings
	return res
}

// buildAuditPromptMessage formats the user-side message for the security audit.
// Unlike the authoring validator, it DOES include supporting-file contents
// (text files only, capped), since malicious code usually lives in scripts.
// Binary files are listed by path/size only.
func buildAuditPromptMessage(t auditTarget, files []SkillFile) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin: %s\nSkill name: %s\n\n--- Description ---\n%s\n\n--- SKILL.md body ---\n%s",
		t.PluginName, strings.TrimSpace(t.SkillName), strings.TrimSpace(t.Description), t.Body)

	if len(files) > 0 {
		sb.WriteString("\n\n--- Supporting files ---\n")
		for _, f := range files {
			if f.IsBinary {
				fmt.Fprintf(&sb, "\n# %s (binary, %d bytes — contents not shown)\n", f.Path, f.SizeBytes)
				continue
			}
			content := f.Content
			truncated := ""
			if len(content) > auditFileContentCap {
				content = content[:auditFileContentCap]
				truncated = fmt.Sprintf("\n…(truncated, %d total bytes)", f.SizeBytes)
			}
			fmt.Fprintf(&sb, "\n# %s (%d bytes)\n```\n%s\n```%s\n", f.Path, f.SizeBytes, content, truncated)
		}
	}
	return sb.String()
}

// buildAgentAuditPromptMessage formats the user-side message for the audit of
// an agent: its frontmatter values and its system prompt body. The body is
// sent whole — it is already capped at maxAgentBodyBytes on write.
func buildAgentAuditPromptMessage(t auditTarget) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Plugin: %s\nAgent name: %s\nAgent type: %s:%s\n", t.PluginName, t.AgentName, t.PluginName, t.AgentName)
	fmt.Fprintf(&sb, "Model: %s\nTools: %s\n", valueOrNotSet(t.Model), valueOrNotSet(t.Tools))
	if extra := strings.TrimSpace(t.ExtraFrontmatter); extra != "" {
		fmt.Fprintf(&sb, "\n--- Further frontmatter (raw YAML) ---\n%s\n", extra)
	}
	fmt.Fprintf(&sb, "\n--- Description ---\n%s\n\n--- System prompt body ---\n%s",
		strings.TrimSpace(t.Description), t.Body)
	return sb.String()
}

func valueOrNotSet(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(not set)"
	}
	return s
}

// storeAuditResult inserts one verdict row. categories/findings are stored as
// JSONB. alerted is set when the score reached the threshold (the sweep emails
// in a single batch afterward; this flag records which rows drove that alert).
func (a *App) storeAuditResult(ctx context.Context, t auditTarget, res AuditResult) error {
	cats, err := json.Marshal(res.Categories)
	if err != nil {
		return err
	}
	finds, err := json.Marshal(res.Findings)
	if err != nil {
		return err
	}
	alerted := res.Error == "" && res.RiskScore >= a.Cfg.AuditThreshold
	if t.isAgent() {
		_, err = a.DB.ExecContext(ctx, `
			INSERT INTO agent_audit_results
				(agent_id, model, risk_score, risk_level, categories, summary, findings, error, alerted)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
		`, t.AgentID, res.Model, res.RiskScore, res.RiskLevel, string(cats), res.Summary,
			string(finds), res.Error, alerted)
		return err
	}
	_, err = a.DB.ExecContext(ctx, `
		INSERT INTO skill_audit_results
			(skill_id, model, risk_score, risk_level, categories, summary, findings, raw, error, alerted)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`, t.SkillID, res.Model, res.RiskScore, res.RiskLevel, string(cats), res.Summary,
		string(finds), "", res.Error, alerted)
	return err
}

// sendAuditAlert emails the configured recipients a summary of flagged skills.
// When email is unconfigured or no recipients are set, it logs the alert
// instead so the signal is never lost.
func (a *App) sendAuditAlert(flagged []AuditResult) {
	sort.Slice(flagged, func(i, j int) bool { return flagged[i].RiskScore > flagged[j].RiskScore })

	subject := fmt.Sprintf("[%s] Skill audit: %d item(s) flagged at/above risk %d",
		a.Cfg.MarketplaceName, len(flagged), a.Cfg.AuditThreshold)
	body := buildAuditAlertBody(a.Cfg.MarketplaceName, a.Cfg.PublicBaseURL, a.Cfg.AuditThreshold, flagged)

	if !a.Email.Configured() || len(a.Cfg.AuditAlertEmails) == 0 {
		log.Printf("skill audit alert (email not sent — %s):\n%s",
			emailDisabledReason(a.Email.Configured(), len(a.Cfg.AuditAlertEmails)), body)
		return
	}
	if err := a.Email.Send(a.Cfg.AuditAlertEmails, subject, body); err != nil {
		log.Printf("ERROR: skill audit: send alert email: %v\nalert body:\n%s", err, body)
		return
	}
	log.Printf("skill audit: alert email sent to %d recipient(s)", len(a.Cfg.AuditAlertEmails))
}

func emailDisabledReason(configured bool, recipients int) string {
	if !configured {
		return "SMTP not configured"
	}
	if recipients == 0 {
		return "no AUDIT_ALERT_EMAILS recipients"
	}
	return "unknown"
}

// buildAuditAlertBody renders the plain-text alert email.
func buildAuditAlertBody(marketplace, baseURL string, threshold int, flagged []AuditResult) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "The security audit on %s flagged %d skill(s) or agent(s) with a risk score at or above %d.\n\n",
		marketplace, len(flagged), threshold)
	for _, r := range flagged {
		fmt.Fprintf(&sb, "• %s — risk %d (%s)\n", r.displayName(), r.RiskScore, strings.ToUpper(r.RiskLevel))
		if r.Summary != "" {
			fmt.Fprintf(&sb, "    %s\n", r.Summary)
		}
		if len(r.Categories) > 0 {
			fmt.Fprintf(&sb, "    categories: %s\n", strings.Join(r.Categories, ", "))
		}
		for _, f := range r.Findings {
			fmt.Fprintf(&sb, "    - [%s] %s: %s\n", strings.ToUpper(f.Severity), f.Category, f.Detail)
		}
		sb.WriteString("\n")
	}
	if base := strings.TrimRight(baseURL, "/"); base != "" {
		fmt.Fprintf(&sb, "Review full results: %s/audit\n", base)
	}
	sb.WriteString("\nThis is an automated message from the plugin-skill-hosting security audit.\n")
	return sb.String()
}

// latestAuditResults returns the most recent verdict per skill, joined with
// plugin and skill names, ordered by risk score descending. Soft-deleted
// skills/plugins are excluded.
func (a *App) latestAuditResults(ctx context.Context) ([]AuditResult, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT DISTINCT ON (r.skill_id)
			r.skill_id, p.name, s.name, r.audited_at, r.model,
			r.risk_score, r.risk_level, r.categories, r.summary, r.findings, r.error
		FROM skill_audit_results r
		JOIN skills s ON s.id = r.skill_id
		JOIN plugins p ON p.id = s.plugin_id
		WHERE s.deleted_at IS NULL AND p.deleted_at IS NULL
		ORDER BY r.skill_id, r.audited_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []AuditResult{}
	for rows.Next() {
		r := AuditResult{Kind: auditKindSkill}
		var cats, finds []byte
		if err := rows.Scan(&r.SkillID, &r.PluginName, &r.SkillName, &r.AuditedAt, &r.Model,
			&r.RiskScore, &r.RiskLevel, &cats, &r.Summary, &finds, &r.Error); err != nil {
			return nil, err
		}
		decodeAuditJSON(&r, cats, finds)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	agentRows, err := a.DB.QueryContext(ctx, `
		SELECT DISTINCT ON (r.agent_id)
			r.agent_id, p.name, g.name, r.audited_at, r.model,
			r.risk_score, r.risk_level, r.categories, r.summary, r.findings, r.error
		FROM agent_audit_results r
		JOIN agents g ON g.id = r.agent_id
		JOIN plugins p ON p.id = g.plugin_id
		WHERE p.deleted_at IS NULL
		ORDER BY r.agent_id, r.audited_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer agentRows.Close()
	for agentRows.Next() {
		r := AuditResult{Kind: auditKindAgent}
		var cats, finds []byte
		if err := agentRows.Scan(&r.AgentID, &r.PluginName, &r.AgentName, &r.AuditedAt, &r.Model,
			&r.RiskScore, &r.RiskLevel, &cats, &r.Summary, &finds, &r.Error); err != nil {
			return nil, err
		}
		decodeAuditJSON(&r, cats, finds)
		out = append(out, r)
	}
	if err := agentRows.Err(); err != nil {
		return nil, err
	}
	// DISTINCT ON forces ordering by id; re-sort by risk for display.
	sort.SliceStable(out, func(i, j int) bool { return out[i].RiskScore > out[j].RiskScore })
	return out, nil
}

// decodeAuditJSON fills Categories and Findings from their JSONB columns,
// leaving empty (never nil) slices so the API always returns arrays.
func decodeAuditJSON(r *AuditResult, cats, finds []byte) {
	if len(cats) > 0 {
		_ = json.Unmarshal(cats, &r.Categories)
	}
	if r.Categories == nil {
		r.Categories = []string{}
	}
	if len(finds) > 0 {
		_ = json.Unmarshal(finds, &r.Findings)
	}
	if r.Findings == nil {
		r.Findings = []skillvalidation.AuditFinding{}
	}
}
