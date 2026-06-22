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
type AuditResult struct {
	SkillID    string                         `json:"skillId"`
	PluginName string                         `json:"pluginName"`
	SkillName  string                         `json:"skillName"`
	AuditedAt  time.Time                      `json:"auditedAt"`
	Model      string                         `json:"model"`
	RiskScore  int                            `json:"riskScore"`
	RiskLevel  string                         `json:"riskLevel"`
	Categories []string                       `json:"categories"`
	Summary    string                         `json:"summary"`
	Findings   []skillvalidation.AuditFinding `json:"findings"`
	Error      string                         `json:"error,omitempty"`
}

// auditTarget is a skill selected for auditing, carrying just the fields the
// audit prompt and storage need.
type auditTarget struct {
	SkillID     string
	PluginName  string
	SkillName   string
	Description string
	Body        string
}

// StartSkillAudit launches the scheduled security-audit goroutine. It is a
// no-op unless the feature is enabled AND an Anthropic API key is configured
// (the audit can't run without the model). On boot it audits immediately only
// if the most recent stored audit is older than one interval (or none exists),
// so frequent restarts don't re-audit on every launch; otherwise it waits for
// the next tick. Mirrors the StartOAuthGC pattern.
func (a *App) StartSkillAudit(ctx context.Context, wg *sync.WaitGroup) {
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
	err := a.DB.QueryRowContext(ctx,
		`SELECT max(audited_at) FROM skill_audit_results`).Scan(&last)
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
	log.Printf("skill audit (%s): auditing %d skills", trigger, len(targets))

	// Clear last sweep's per-skill gauges so deleted/renamed skills don't linger
	// as stale series; we repopulate below for each successfully-audited skill.
	metrics.SkillAuditRiskScore.Reset()

	var flagged []AuditResult
	// Plugins that gained a freshly auto-locked skill this sweep; re-materialized
	// once at the end so each locked skill drops out of git and the external
	// mirror. A set, so a plugin with several flagged skills rebuilds only once.
	lockedPlugins := map[string]struct{}{}
	for _, t := range targets {
		if ctx.Err() != nil {
			log.Printf("skill audit: context cancelled, stopping sweep")
			return
		}
		res := a.auditOneSkill(ctx, t)
		if err := a.storeAuditResult(ctx, t, res); err != nil {
			log.Printf("ERROR: skill audit: store result for %s/%s: %v", t.PluginName, t.SkillName, err)
		}
		if res.Error == "" {
			metrics.SkillAuditRiskScore.
				WithLabelValues(t.PluginName, t.SkillName, res.RiskLevel).
				Set(float64(res.RiskScore))
			if res.RiskScore >= a.Cfg.AuditThreshold {
				flagged = append(flagged, res)
				// Auto-lock the offending skill. autoLockSkill is a no-op when the
				// skill is already locked or an admin has acknowledged a prior
				// audit lock, so this never overrides a manual decision.
				reason := res.Summary
				if reason == "" {
					reason = fmt.Sprintf("auto-locked by security audit: risk score %d (%s)", res.RiskScore, res.RiskLevel)
				}
				if locked, err := a.autoLockSkill(ctx, t.SkillID, reason); err != nil {
					log.Printf("ERROR: skill audit: auto-lock %s/%s: %v", t.PluginName, t.SkillName, err)
				} else if locked {
					lockedPlugins[t.PluginName] = struct{}{}
					log.Printf("skill audit: auto-locked %s/%s (risk %d)", t.PluginName, t.SkillName, res.RiskScore)
				}
			}
		}
	}

	// Rebuild the git repos for plugins whose skills got auto-locked, so the
	// withdrawal is reflected in the served trees without waiting for the next
	// content change.
	for name := range lockedPlugins {
		p, err := a.loadPluginByName(ctx, name)
		if err != nil {
			log.Printf("ERROR: skill audit: reload plugin %q to re-materialize after auto-lock: %v", name, err)
			continue
		}
		if err := a.materializePluginDetached(p); err != nil {
			log.Printf("ERROR: skill audit: re-materialize plugin %q after auto-lock: %v", name, err)
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
	log.Printf("skill audit (%s): done — %d skills, %d at/above threshold %d",
		trigger, len(targets), len(flagged), a.Cfg.AuditThreshold)
}

// loadAuditTargets returns every non-deleted skill (across all non-deleted
// plugins) with the fields the auditor needs.
func (a *App) loadAuditTargets(ctx context.Context) ([]auditTarget, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT s.id, p.name, s.name, s.description, s.body
		FROM skills s
		JOIN plugins p ON p.id = s.plugin_id
		WHERE s.deleted_at IS NULL AND p.deleted_at IS NULL
		ORDER BY p.name, s.name
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []auditTarget
	for rows.Next() {
		var t auditTarget
		if err := rows.Scan(&t.SkillID, &t.PluginName, &t.SkillName, &t.Description, &t.Body); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// auditOneSkill builds the prompt (including supporting-file contents), calls
// Claude, and parses the verdict. Any failure is captured in the returned
// result's Error field with a zero risk score, so the sweep keeps going.
func (a *App) auditOneSkill(ctx context.Context, t auditTarget) AuditResult {
	res := AuditResult{
		SkillID:    t.SkillID,
		PluginName: t.PluginName,
		SkillName:  t.SkillName,
		Model:      a.Cfg.AnthropicModel,
		RiskLevel:  "low",
		Categories: []string{},
		Findings:   []skillvalidation.AuditFinding{},
	}

	files, err := a.loadSkillFiles(ctx, t.SkillID)
	if err != nil {
		// Non-fatal: audit the SKILL.md alone, but note the gap.
		log.Printf("WARN: skill audit: load files for %s/%s: %v", t.PluginName, t.SkillName, err)
		files = nil
	}
	userMsg := buildAuditPromptMessage(t, files)

	callCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()

	raw, err := a.callClaude(callCtx, skillvalidation.SecurityAuditSystemPrompt, userMsg, auditMaxTokens)
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

	subject := fmt.Sprintf("[%s] Skill audit: %d skill(s) flagged at/above risk %d",
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
	fmt.Fprintf(&sb, "The scheduled security audit on %s flagged %d skill(s) with a risk score at or above %d.\n\n",
		marketplace, len(flagged), threshold)
	for _, r := range flagged {
		fmt.Fprintf(&sb, "• %s / %s — risk %d (%s)\n", r.PluginName, r.SkillName, r.RiskScore, strings.ToUpper(r.RiskLevel))
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
		var r AuditResult
		var cats, finds []byte
		if err := rows.Scan(&r.SkillID, &r.PluginName, &r.SkillName, &r.AuditedAt, &r.Model,
			&r.RiskScore, &r.RiskLevel, &cats, &r.Summary, &finds, &r.Error); err != nil {
			return nil, err
		}
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
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// DISTINCT ON forces ordering by skill_id; re-sort by risk for display.
	sort.SliceStable(out, func(i, j int) bool { return out[i].RiskScore > out[j].RiskScore })
	return out, nil
}
