package server

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode"

	"github.com/go-chi/chi/v5"

	"marketplace/internal/metrics"
	"marketplace/internal/semver"
)

// Agent is a plugin-level subagent definition. It is materialized as
// agents/<name>.md at the plugin root, where Claude Code picks it up as the
// agent type "<plugin>:<name>". Body is the subagent's system prompt.
type Agent struct {
	ID          string `json:"id"`
	PluginID    string `json:"pluginId"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Body        string `json:"body"`
	// Model is the `model` frontmatter value: an alias (opus, sonnet, haiku,
	// inherit) or a full model id. Empty means "not set" (Claude Code then
	// applies its own default).
	Model string `json:"model"`
	// Tools is the `tools` frontmatter value: a comma-separated allowlist.
	// Empty means "not set" (the agent inherits every tool).
	Tools string `json:"tools"`
	// ExtraFrontmatter holds any further frontmatter keys as raw YAML.
	ExtraFrontmatter string    `json:"extraFrontmatter"`
	CreatedAt        time.Time `json:"createdAt"`
	UpdatedAt        time.Time `json:"updatedAt"`
	CreatedBy        *string   `json:"createdBy,omitempty"`
	CreatedByName    *string   `json:"createdByName,omitempty"`
	UpdatedBy        *string   `json:"updatedBy,omitempty"`
	UpdatedByName    *string   `json:"updatedByName,omitempty"`
	// Lock state, same contract as Skill: a locked agent is withdrawn from git,
	// the external mirror and MCP but stays visible in the web UI. LockSource is
	// "admin" or "audit"; LockedBy/Name is nil when the audit locked it.
	Locked       bool       `json:"locked"`
	LockedAt     *time.Time `json:"lockedAt,omitempty"`
	LockedBy     *string    `json:"lockedBy,omitempty"`
	LockedByName *string    `json:"lockedByName,omitempty"`
	LockSource   *string    `json:"lockSource,omitempty"`
	LockReason   string     `json:"lockReason,omitempty"`
	// Audit gate state, same contract as on Skill.
	AuditPending      bool       `json:"auditPending"`
	AuditPendingAt    *time.Time `json:"auditPendingAt,omitempty"`
	AuditPendingError string     `json:"auditPendingError,omitempty"`
}

// Limits on agent content. The description cap is a sanity bound, not a Claude
// Code rule: agent descriptions often carry usage examples and run longer than
// skill descriptions.
const (
	maxAgentDescriptionChars = 4096
	maxAgentBodyBytes        = 256 * 1024
	maxAgentToolsChars       = 2000
	maxAgentExtraBytes       = 8 * 1024
)

// agentModelRe accepts the aliases (opus, sonnet, haiku, inherit) and full
// model ids, including provider-prefixed ones such as "us.anthropic.claude-…"
// and suffixed ones such as "…[1m]".
var agentModelRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]/-]{0,127}$`)

// agentManagedKeys are the frontmatter keys that have their own field. They
// must not appear again in extraFrontmatter, where they would produce a
// duplicate YAML key.
var agentManagedKeys = map[string]bool{
	"name":        true,
	"description": true,
	"model":       true,
	"tools":       true,
}

// errAgentExists is returned by insertAgent on a (plugin, name) collision.
var errAgentExists = errors.New("agent with that name already exists")

// agentFields is the editable content of an agent, shared by the REST and MCP
// write paths so both validate and store exactly the same way.
type agentFields struct {
	Name             string
	Description      string
	Body             string
	Model            string
	Tools            string
	ExtraFrontmatter string
}

// normalizeAndValidate trims the fields and checks every rule. The returned
// error text is safe to hand straight back to the caller.
func (f *agentFields) normalizeAndValidate() error {
	f.Name = strings.TrimSpace(strings.ToLower(f.Name))
	if !slugRe.MatchString(f.Name) {
		return errors.New("agent name must be 3-64 chars, lowercase, [a-z0-9-]")
	}

	// The description is a single YAML scalar: fold line breaks to spaces.
	f.Description = strings.TrimSpace(strings.Join(strings.Fields(f.Description), " "))
	if f.Description == "" {
		return errors.New("description is required")
	}
	if n := descriptionLen(f.Description); n > maxAgentDescriptionChars {
		return fmt.Errorf("description must be at most %d characters (got %d)", maxAgentDescriptionChars, n)
	}
	if hasControlChars(f.Description) {
		return errors.New("description must not contain control characters")
	}

	if strings.TrimSpace(f.Body) == "" {
		return errors.New("body is required: it is the agent's system prompt")
	}
	if len(f.Body) > maxAgentBodyBytes {
		return fmt.Errorf("body exceeds %d byte limit", maxAgentBodyBytes)
	}

	f.Model = strings.TrimSpace(f.Model)
	if f.Model != "" && !agentModelRe.MatchString(f.Model) {
		return errors.New("model must be an alias (opus, sonnet, haiku, inherit) or a model id")
	}

	f.Tools = strings.TrimSpace(f.Tools)
	if len(f.Tools) > maxAgentToolsChars {
		return fmt.Errorf("tools must be at most %d characters", maxAgentToolsChars)
	}
	if hasControlChars(f.Tools) {
		return errors.New("tools must be a single line: a comma-separated list of tool names")
	}

	extra, err := validateAgentExtraFrontmatter(f.ExtraFrontmatter)
	if err != nil {
		return err
	}
	f.ExtraFrontmatter = extra
	return nil
}

// hasControlChars reports whether s contains any control character, including
// line breaks and tabs.
func hasControlChars(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// validateAgentExtraFrontmatter checks the raw YAML that is appended verbatim
// to the generated frontmatter. It rejects anything that would end the
// frontmatter block early or duplicate a key that has its own field.
func validateAgentExtraFrontmatter(extra string) (string, error) {
	extra = strings.TrimSpace(strings.ReplaceAll(extra, "\r\n", "\n"))
	if extra == "" {
		return "", nil
	}
	if len(extra) > maxAgentExtraBytes {
		return "", fmt.Errorf("extraFrontmatter exceeds %d byte limit", maxAgentExtraBytes)
	}
	for _, line := range strings.Split(extra, "\n") {
		trimmedRight := strings.TrimRight(line, " \t")
		if trimmedRight == "---" || trimmedRight == "..." {
			return "", errors.New("extraFrontmatter must not contain a '---' or '...' document marker")
		}
		if line == "" || line[0] == ' ' || line[0] == '\t' || line[0] == '#' {
			continue
		}
		if idx := strings.Index(line, ":"); idx > 0 {
			key := strings.Trim(strings.TrimSpace(line[:idx]), `"'`)
			if agentManagedKeys[key] {
				return "", fmt.Errorf("extraFrontmatter must not set %q: use the %s field", key, key)
			}
		}
	}
	return extra, nil
}

// yamlPlainRe matches strings that are safe to write as a plain (unquoted)
// YAML scalar in a block mapping: they start with a letter and hold no
// character that YAML could read as syntax.
var yamlPlainRe = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9 _./()*+,-]*$`)

// yamlReserved are plain scalars YAML resolves to a non-string type.
var yamlReserved = map[string]bool{
	"true": true, "false": true, "null": true,
	"yes": true, "no": true, "on": true, "off": true, "y": true, "n": true,
}

// yamlScalar renders s as a single-line YAML scalar. Simple values stay plain
// for readability; everything else is double-quoted. A JSON string is a valid
// YAML double-quoted scalar, so the JSON encoder does the escaping.
func yamlScalar(s string) string {
	if yamlPlainRe.MatchString(s) && !strings.HasSuffix(s, " ") && !yamlReserved[strings.ToLower(s)] {
		return s
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(s); err != nil {
		return `""`
	}
	return strings.TrimRight(buf.String(), "\n")
}

// buildAgentMarkdown renders the agents/<name>.md file for an agent.
func buildAgentMarkdown(ag Agent) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + ag.Name + "\n")
	b.WriteString("description: " + yamlScalar(strings.Join(strings.Fields(ag.Description), " ")) + "\n")
	if ag.Model != "" {
		b.WriteString("model: " + yamlScalar(ag.Model) + "\n")
	}
	if ag.Tools != "" {
		b.WriteString("tools: " + yamlScalar(ag.Tools) + "\n")
	}
	if extra := strings.TrimSpace(ag.ExtraFrontmatter); extra != "" {
		b.WriteString(extra)
		b.WriteString("\n")
	}
	b.WriteString("---\n\n")
	body := ag.Body
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	b.WriteString(body)
	return b.String()
}

// writeAgentsToWorkTree writes one agents/<name>.md per agent under targetDir.
// With no publishable agents it creates nothing, so plugins without agents
// render exactly as they did before agents existed.
//
// prevDir is the previously published tree (may be ""). An audit-pending agent
// is not rendered from the database: its last published file is carried over
// from prevDir instead, or left out when it was never published.
func writeAgentsToWorkTree(targetDir, prevDir string, agents []Agent) error {
	dir := filepath.Join(targetDir, "agents")
	for _, ag := range agents {
		// Locked agents are withdrawn from every git surface (internal repo and
		// the external mirror both render through here).
		if ag.Locked {
			continue
		}
		// Names are slug-validated on write; re-check here as defence in depth
		// because the name becomes a file name.
		if !slugRe.MatchString(ag.Name) {
			return fmt.Errorf("agent %q has an invalid name", ag.Name)
		}
		file := filepath.Join(dir, ag.Name+".md")
		if ag.AuditPending {
			if prevDir == "" {
				continue
			}
			if _, err := copyPublished(filepath.Join(prevDir, "agents", ag.Name+".md"), file); err != nil {
				return err
			}
			continue
		}
		// The directory is created on first use, so a plugin with nothing to
		// publish gets no agents/ directory at all.
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(file, []byte(buildAgentMarkdown(ag)), 0o644); err != nil {
			return err
		}
	}
	return nil
}

// --- queries ---------------------------------------------------------------

const agentSelect = `
	SELECT g.id, g.plugin_id, g.name, g.description, g.body, g.model, g.tools, g.extra_frontmatter,
	       g.created_at, g.updated_at,
	       g.created_by, cu.username,
	       g.updated_by, uu.username,
	       g.locked_at, g.locked_by, lu.username, g.lock_source, g.lock_reason,
	       g.audit_pending_token IS NOT NULL, g.audit_pending_at, g.audit_pending_error
	FROM agents g
	LEFT JOIN users cu ON cu.id = g.created_by
	LEFT JOIN users uu ON uu.id = g.updated_by
	LEFT JOIN users lu ON lu.id = g.locked_by
`

func (a *App) queryAgents(ctx context.Context, where string, args ...interface{}) ([]Agent, error) {
	rows, err := a.DB.QueryContext(ctx, agentSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	agents := []Agent{}
	for rows.Next() {
		var ag Agent
		var createdBy, createdByName, updatedBy, updatedByName sql.NullString
		var lockedAt sql.NullTime
		var lockedBy, lockedByName, lockSource sql.NullString
		var auditPendingAt sql.NullTime
		if err := rows.Scan(&ag.ID, &ag.PluginID, &ag.Name, &ag.Description, &ag.Body, &ag.Model, &ag.Tools, &ag.ExtraFrontmatter,
			&ag.CreatedAt, &ag.UpdatedAt,
			&createdBy, &createdByName,
			&updatedBy, &updatedByName,
			&lockedAt, &lockedBy, &lockedByName, &lockSource, &ag.LockReason,
			&ag.AuditPending, &auditPendingAt, &ag.AuditPendingError); err != nil {
			return nil, err
		}
		if auditPendingAt.Valid {
			t := auditPendingAt.Time
			ag.AuditPendingAt = &t
		}
		if lockedAt.Valid {
			t := lockedAt.Time
			ag.LockedAt = &t
			ag.Locked = true
		}
		ag.LockedBy = nullStringPtr(lockedBy)
		ag.LockedByName = nullStringPtr(lockedByName)
		ag.LockSource = nullStringPtr(lockSource)
		ag.CreatedBy = nullStringPtr(createdBy)
		ag.CreatedByName = nullStringPtr(createdByName)
		ag.UpdatedBy = nullStringPtr(updatedBy)
		ag.UpdatedByName = nullStringPtr(updatedByName)
		agents = append(agents, ag)
	}
	return agents, rows.Err()
}

func nullStringPtr(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	s := v.String
	return &s
}

// loadAgentsForPlugin returns a plugin's agents ordered by name.
func (a *App) loadAgentsForPlugin(ctx context.Context, pluginID string) ([]Agent, error) {
	return a.queryAgents(ctx, `WHERE g.plugin_id = $1 ORDER BY g.name ASC`, pluginID)
}

// loadAgent fetches one agent by (plugin, name), or sql.ErrNoRows.
func (a *App) loadAgent(ctx context.Context, pluginID, name string) (*Agent, error) {
	agents, err := a.queryAgents(ctx, `WHERE g.plugin_id = $1 AND g.name = $2`, pluginID, name)
	if err != nil {
		return nil, err
	}
	if len(agents) == 0 {
		return nil, sql.ErrNoRows
	}
	return &agents[0], nil
}

// --- writes ----------------------------------------------------------------
//
// Each write commits the agent change and the plugin version bump in one
// transaction. Adding or removing an agent changes the plugin's published
// surface, so it bumps the major, the same rule skills follow. An edit bumps
// minor or patch by how much the body changed. Callers re-materialize the git
// repo after a successful return.

// insertAgent stores a new agent. f must already be validated.
func (a *App) insertAgent(ctx context.Context, p *Plugin, f agentFields, userID string) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	var id string
	if err := tx.QueryRowContext(ctx, `
		INSERT INTO agents (plugin_id, name, description, body, model, tools, extra_frontmatter, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $8) RETURNING id
	`, p.ID, f.Name, f.Description, f.Body, f.Model, f.Tools, f.ExtraFrontmatter, userID).Scan(&id); err != nil {
		if isUniqueViolation(err) {
			return errAgentExists
		}
		return err
	}
	// Audit gate: mark pending in the same transaction as the content write.
	if err := a.markAuditPending(ctx, tx, auditKindAgent, id); err != nil {
		return err
	}
	// Bump a copy so p.Version only changes once the commit has succeeded.
	bumped := *p
	if err := a.bumpAndPersistPluginVersion(ctx, tx, &bumped, semver.BumpMajor); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	p.Version = bumped.Version
	return nil
}

// updateAgentRow replaces an agent's content. f must already be validated; its
// Name is ignored because the slug is permanent.
func (a *App) updateAgentRow(ctx context.Context, p *Plugin, existing *Agent, f agentFields, userID string) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		UPDATE agents SET description = $1, body = $2, model = $3, tools = $4, extra_frontmatter = $5,
		                  updated_at = now(), updated_by = $6,
		                  audit_lock_suppressed = FALSE
		WHERE id = $7
	`, f.Description, f.Body, f.Model, f.Tools, f.ExtraFrontmatter, userID, existing.ID); err != nil {
		return err
	}
	if err := a.markAuditPending(ctx, tx, auditKindAgent, existing.ID); err != nil {
		return err
	}
	bumped := *p
	if err := a.bumpAndPersistPluginVersion(ctx, tx, &bumped, semver.BumpKindForSizeChange(len(existing.Body), len(f.Body))); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	p.Version = bumped.Version
	return nil
}

// deleteAgentRow removes an agent permanently.
func (a *App) deleteAgentRow(ctx context.Context, p *Plugin, existing *Agent) error {
	tx, err := a.DB.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM agents WHERE id = $1`, existing.ID); err != nil {
		return err
	}
	bumped := *p
	if err := a.bumpAndPersistPluginVersion(ctx, tx, &bumped, semver.BumpMajor); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	p.Version = bumped.Version
	return nil
}

// --- REST handlers ---------------------------------------------------------

// agentReq is the create/update payload. On update, omitting model, tools or
// extraFrontmatter keeps the stored value; sending "" clears it.
type agentReq struct {
	Name             string  `json:"name"`
	Description      string  `json:"description"`
	Body             string  `json:"body"`
	Model            *string `json:"model,omitempty"`
	Tools            *string `json:"tools,omitempty"`
	ExtraFrontmatter *string `json:"extraFrontmatter,omitempty"`
}

// loadAgentOrRespond fetches the agent named by the URL :agent param inside
// the given plugin. On a miss or DB failure it writes the HTTP error and
// returns nil.
func (a *App) loadAgentOrRespond(w http.ResponseWriter, r *http.Request, pluginID string) *Agent {
	ag, err := a.loadAgent(r.Context(), pluginID, chi.URLParam(r, "agent"))
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "agent not found")
		return nil
	}
	if err != nil {
		serverErr(w, r, err, "db error")
		return nil
	}
	return ag
}

func (a *App) handleListAgents(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	agents, err := a.loadAgentsForPlugin(r.Context(), p.ID)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, agents)
}

func (a *App) handleGetAgent(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	ag := a.loadAgentOrRespond(w, r, p.ID)
	if ag == nil {
		return
	}
	writeJSON(w, http.StatusOK, ag)
}

func (a *App) handleCreateAgent(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	var req agentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	f := agentFields{Name: req.Name, Description: req.Description, Body: req.Body}
	if req.Model != nil {
		f.Model = *req.Model
	}
	if req.Tools != nil {
		f.Tools = *req.Tools
	}
	if req.ExtraFrontmatter != nil {
		f.ExtraFrontmatter = *req.ExtraFrontmatter
	}
	if err := f.normalizeAndValidate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := a.insertAgent(r.Context(), p, f, user.ID); err != nil {
		if errors.Is(err, errAgentExists) {
			writeErr(w, http.StatusConflict, errAgentExists.Error())
			return
		}
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	metrics.AgentMutationsTotal.WithLabelValues("create", "success").Inc()

	ag, err := a.loadAgent(r.Context(), p.ID, f.Name)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	a.auditAgentOnChange(ag.ID)
	writeJSON(w, http.StatusOK, ag)
}

func (a *App) handleUpdateAgent(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	var req agentReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	existing := a.loadAgentOrRespond(w, r, p.ID)
	if existing == nil {
		return
	}
	if rejectIfAgentLocked(w, existing) {
		return
	}

	f := agentFields{
		Name:             existing.Name,
		Description:      req.Description,
		Body:             req.Body,
		Model:            existing.Model,
		Tools:            existing.Tools,
		ExtraFrontmatter: existing.ExtraFrontmatter,
	}
	if req.Model != nil {
		f.Model = *req.Model
	}
	if req.Tools != nil {
		f.Tools = *req.Tools
	}
	if req.ExtraFrontmatter != nil {
		f.ExtraFrontmatter = *req.ExtraFrontmatter
	}
	if err := f.normalizeAndValidate(); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	if err := a.updateAgentRow(r.Context(), p, existing, f, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	metrics.AgentMutationsTotal.WithLabelValues("update", "success").Inc()
	a.auditAgentOnChange(existing.ID)

	ag, err := a.loadAgent(r.Context(), p.ID, existing.Name)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, ag)
}

// handleDeleteAgent removes an agent for good. Because there is no undo, it is
// limited to the plugin owner, an admin, or the user who created the agent.
func (a *App) handleDeleteAgent(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	existing := a.loadAgentOrRespond(w, r, p.ID)
	if existing == nil {
		return
	}
	isCreator := existing.CreatedBy != nil && *existing.CreatedBy == user.ID
	if p.OwnerID != user.ID && !user.IsAdmin && !isCreator {
		writeErr(w, http.StatusForbidden, "only the plugin owner, an admin, or the agent's creator can delete it")
		return
	}
	// Same rule as skills: only an admin may delete a locked agent directly.
	if existing.Locked && !user.IsAdmin {
		rejectIfAgentLocked(w, existing)
		return
	}

	if err := a.deleteAgentRow(r.Context(), p, existing); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	metrics.AgentMutationsTotal.WithLabelValues("delete", "success").Inc()
	w.WriteHeader(http.StatusNoContent)
}
