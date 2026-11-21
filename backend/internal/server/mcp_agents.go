package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"marketplace/internal/metrics"
)

// --- shapes ----------------------------------------------------------------

type mcpAgentSummary struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Model       string    `json:"model,omitempty"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type mcpAgentDetail struct {
	Plugin           string `json:"plugin"`
	Name             string `json:"name"`
	Description      string `json:"description"`
	Body             string `json:"body"`
	Model            string `json:"model"`
	Tools            string `json:"tools"`
	ExtraFrontmatter string `json:"extraFrontmatter"`
	// AgentType is the value to pass as subagent_type in Claude Code.
	AgentType string `json:"agentType"`
}

type mcpAgentRefIn struct {
	Plugin string `json:"plugin" jsonschema:"the plugin name (slug)"`
	Agent  string `json:"agent" jsonschema:"the agent name (slug) within the plugin"`
}

type mcpCreateAgentIn struct {
	Plugin           string `json:"plugin" jsonschema:"the plugin to add the agent to"`
	Name             string `json:"name" jsonschema:"agent slug (3-64 chars, lowercase, [a-z0-9-]); Claude Code exposes the agent as <plugin>:<name>"`
	Description      string `json:"description" jsonschema:"when Claude should delegate to this agent"`
	Body             string `json:"body" jsonschema:"the agent's system prompt as markdown (no YAML frontmatter)"`
	Model            string `json:"model,omitempty" jsonschema:"optional model: an alias (opus, sonnet, haiku, inherit) or a full model id"`
	Tools            string `json:"tools,omitempty" jsonschema:"optional comma-separated tool allowlist, e.g. 'Read, Grep, Bash'; leave empty to inherit all tools"`
	ExtraFrontmatter string `json:"extraFrontmatter,omitempty" jsonschema:"optional further frontmatter keys as raw YAML (not name, description, model or tools)"`
}

type mcpUpdateAgentIn struct {
	Plugin           string  `json:"plugin"`
	Agent            string  `json:"agent"`
	Description      string  `json:"description" jsonschema:"new description"`
	Body             string  `json:"body" jsonschema:"new system prompt markdown"`
	Model            *string `json:"model,omitempty" jsonschema:"new model; omit to keep the current value, send an empty string to clear it"`
	Tools            *string `json:"tools,omitempty" jsonschema:"new comma-separated tool allowlist; omit to keep the current value, send an empty string to clear it"`
	ExtraFrontmatter *string `json:"extraFrontmatter,omitempty" jsonschema:"new raw YAML for further frontmatter keys; omit to keep the current value, send an empty string to clear it"`
}

// --- helpers ---------------------------------------------------------------

func (a *App) resolveAgent(ctx context.Context, pluginID, name string) (*Agent, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return nil, errors.New("agent is required")
	}
	ag, err := a.loadAgent(ctx, pluginID, name)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	if err != nil {
		return nil, fmt.Errorf("db error: %w", err)
	}
	// A locked agent is withdrawn from MCP entirely, like a locked skill: to
	// every read and write tool it looks like it does not exist.
	if ag.Locked {
		return nil, fmt.Errorf("agent %q not found", name)
	}
	return ag, nil
}

func toMCPAgentDetail(p *Plugin, ag *Agent) mcpAgentDetail {
	return mcpAgentDetail{
		Plugin:           p.Name,
		Name:             ag.Name,
		Description:      ag.Description,
		Body:             ag.Body,
		Model:            ag.Model,
		Tools:            ag.Tools,
		ExtraFrontmatter: ag.ExtraFrontmatter,
		AgentType:        p.Name + ":" + ag.Name,
	}
}

// --- tools -----------------------------------------------------------------

func (a *App) addToolGetAgent(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_agent",
		Title:       "Get agent",
		Description: "Read a plugin agent (subagent): its description, system prompt body, model, tools and further frontmatter.",
	}, instrumentMCP("get_agent", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpAgentRefIn) (*mcp.CallToolResult, mcpAgentDetail, error) {
		var zero mcpAgentDetail
		if userFromCtx(ctx) == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		ag, err := a.resolveAgent(ctx, p.ID, in.Agent)
		if err != nil {
			return nil, zero, err
		}
		return okResult(fmt.Sprintf("agent %q in %q", ag.Name, p.Name), toMCPAgentDetail(p, ag))
	}))
}

func (a *App) addToolCreateAgent(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "create_agent",
		Title: "Create agent",
		Description: "Add a new agent (subagent) to a plugin. It is published as agents/<name>.md at the plugin root, " +
			"and Claude Code exposes it as the agent type <plugin>:<name>. Bumps the plugin version and rewrites the git repo.",
	}, instrumentMCP("create_agent", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpCreateAgentIn) (*mcp.CallToolResult, mcpStatusOut, error) {
		var zero mcpStatusOut
		user := userFromCtx(ctx)
		if user == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		f := agentFields{
			Name:             in.Name,
			Description:      in.Description,
			Body:             in.Body,
			Model:            in.Model,
			Tools:            in.Tools,
			ExtraFrontmatter: in.ExtraFrontmatter,
		}
		if err := f.normalizeAndValidate(); err != nil {
			return nil, zero, err
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		if err := a.insertAgent(ctx, p, f, user.ID); err != nil {
			if errors.Is(err, errAgentExists) {
				return nil, zero, errAgentExists
			}
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.materializePluginDetached(p); err != nil {
			return nil, zero, fmt.Errorf("git materialize: %w", err)
		}
		metrics.AgentMutationsTotal.WithLabelValues("create", "success").Inc()
		if created, err := a.loadAgent(ctx, p.ID, f.Name); err == nil {
			a.auditAgentOnChange(created.ID)
		}
		return okResult(
			fmt.Sprintf("created agent %q in %q, agent type %s:%s (plugin now v%s)%s", f.Name, p.Name, p.Name, f.Name, p.Version, a.publishNote()),
			mcpStatusOut{OK: true, Version: p.Version, Message: "agent created"},
		)
	}))
}

func (a *App) addToolUpdateAgent(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:  "update_agent",
		Title: "Update agent",
		Description: "Replace an agent's description and system prompt body, and optionally its model, tools and further frontmatter. " +
			"Bumps the plugin version and rewrites the git repo.",
	}, instrumentMCP("update_agent", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpUpdateAgentIn) (*mcp.CallToolResult, mcpStatusOut, error) {
		var zero mcpStatusOut
		user := userFromCtx(ctx)
		if user == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		existing, err := a.resolveAgent(ctx, p.ID, in.Agent)
		if err != nil {
			return nil, zero, err
		}
		f := agentFields{
			Name:             existing.Name,
			Description:      in.Description,
			Body:             in.Body,
			Model:            existing.Model,
			Tools:            existing.Tools,
			ExtraFrontmatter: existing.ExtraFrontmatter,
		}
		if in.Model != nil {
			f.Model = *in.Model
		}
		if in.Tools != nil {
			f.Tools = *in.Tools
		}
		if in.ExtraFrontmatter != nil {
			f.ExtraFrontmatter = *in.ExtraFrontmatter
		}
		if err := f.normalizeAndValidate(); err != nil {
			return nil, zero, err
		}
		if err := a.updateAgentRow(ctx, p, existing, f, user.ID); err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.materializePluginDetached(p); err != nil {
			return nil, zero, fmt.Errorf("git materialize: %w", err)
		}
		metrics.AgentMutationsTotal.WithLabelValues("update", "success").Inc()
		a.auditAgentOnChange(existing.ID)
		return okResult(
			fmt.Sprintf("updated agent %q in %q (plugin now v%s)%s", existing.Name, p.Name, p.Version, a.publishNote()),
			mcpStatusOut{OK: true, Version: p.Version, Message: "agent updated"},
		)
	}))
}
