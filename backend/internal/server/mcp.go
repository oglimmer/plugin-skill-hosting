package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"marketplace/internal/metrics"
	"marketplace/internal/semver"
)

// userFromCtx pulls the *User stashed by tokenGateMiddleware. Tool handlers
// run with the http request context, so the value is reachable here.
func userFromCtx(ctx context.Context) *User {
	v, _ := ctx.Value(ctxUserKey).(*User)
	return v
}

func (a *App) mcpHandler() http.Handler {
	server := mcp.NewServer(
		&mcp.Implementation{
			Name:    "plugin-skill-hosting",
			Title:   "Plugin & Skill Hosting",
			Version: "0.1.0",
		},
		nil,
	)
	a.registerMCPTools(server)
	// Stateless: no session map kept across requests. All tools here are
	// stateless reads/writes against Postgres, so there is nothing worth
	// preserving per session — and stateful mode would otherwise return 404
	// "session not found" to every client whose session ID predates the last
	// backend restart, forcing them to manually reconnect after each deploy.
	return mcp.NewStreamableHTTPHandler(
		func(r *http.Request) *mcp.Server { return server },
		&mcp.StreamableHTTPOptions{Stateless: true},
	)
}

// --- output shapes ---------------------------------------------------------

type mcpPluginSummary struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Version     string    `json:"version"`
	OwnerName   string    `json:"ownerName"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type mcpListPluginsOut struct {
	Plugins []mcpPluginSummary `json:"plugins"`
}

type mcpSkillSummary struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type mcpPluginDetail struct {
	Name        string            `json:"name"`
	Description string            `json:"description"`
	Version     string            `json:"version"`
	OwnerName   string            `json:"ownerName"`
	License     string            `json:"license"`
	Homepage    string            `json:"homepage"`
	Skills      []mcpSkillSummary `json:"skills"`
}

type mcpSkillFileBrief struct {
	Path      string `json:"path"`
	IsBinary  bool   `json:"isBinary"`
	SizeBytes int    `json:"sizeBytes"`
}

type mcpSkillDetail struct {
	Plugin      string              `json:"plugin"`
	Name        string              `json:"name"`
	Description string              `json:"description"`
	Body        string              `json:"body"`
	Files       []mcpSkillFileBrief `json:"files"`
}

type mcpListSkillFilesOut struct {
	Plugin string              `json:"plugin"`
	Skill  string              `json:"skill"`
	Files  []mcpSkillFileBrief `json:"files"`
}

type mcpSkillFileOut struct {
	Path      string `json:"path"`
	IsBinary  bool   `json:"isBinary"`
	SizeBytes int    `json:"sizeBytes"`
	Content   string `json:"content"`
}

type mcpStatusOut struct {
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
	Version string `json:"version,omitempty"`
}

// --- input shapes ----------------------------------------------------------

type mcpEmptyIn struct{}

type mcpPluginRefIn struct {
	Plugin string `json:"plugin" jsonschema:"the plugin name (slug)"`
}

type mcpSkillRefIn struct {
	Plugin string `json:"plugin" jsonschema:"the plugin name (slug)"`
	Skill  string `json:"skill" jsonschema:"the skill name (slug) within the plugin"`
}

type mcpCreateSkillIn struct {
	Plugin      string `json:"plugin" jsonschema:"the plugin to add the skill to"`
	Name        string `json:"name" jsonschema:"skill slug (3-64 chars, lowercase, [a-z0-9-])"`
	Description string `json:"description" jsonschema:"one-line summary used to decide when to apply this skill"`
	Body        string `json:"body" jsonschema:"SKILL.md body markdown (no YAML frontmatter)"`
}

type mcpUpdateSkillIn struct {
	Plugin      string `json:"plugin"`
	Skill       string `json:"skill"`
	Description string `json:"description" jsonschema:"new one-line summary"`
	Body        string `json:"body" jsonschema:"new SKILL.md body markdown"`
}

type mcpSkillFileRefIn struct {
	Plugin string `json:"plugin"`
	Skill  string `json:"skill"`
	Path   string `json:"path" jsonschema:"file path: a bare filename at the skill root (e.g. config.json), or under a folder (e.g. scripts/, references/, assets/, or any custom folder name). SKILL.md is reserved."`
}

type mcpUpsertSkillFileIn struct {
	Plugin   string `json:"plugin"`
	Skill    string `json:"skill"`
	Path     string `json:"path" jsonschema:"file path: a bare filename at the skill root (e.g. config.json), or under a folder (e.g. scripts/, references/, assets/, or any custom folder name). SKILL.md is reserved."`
	Content  string `json:"content" jsonschema:"raw UTF-8 text, or base64 bytes when isBinary is true"`
	IsBinary bool   `json:"isBinary,omitempty" jsonschema:"set true to send base64-encoded binary content"`
}

// --- helpers ---------------------------------------------------------------

func toolText(text string) []mcp.Content {
	return []mcp.Content{&mcp.TextContent{Text: text}}
}

func okResult[T any](text string, out T) (*mcp.CallToolResult, T, error) {
	return &mcp.CallToolResult{Content: toolText(text)}, out, nil
}

// instrumentMCP wraps a tool handler so each call records a count + duration
// labelled by tool name. Result label is success when the handler returns
// nil error, otherwise error.
func instrumentMCP[In any, Out any](
	name string,
	h func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error),
) func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (*mcp.CallToolResult, Out, error) {
		start := time.Now()
		res, out, err := h(ctx, req, in)
		metrics.MCPToolCallDuration.WithLabelValues(name).Observe(time.Since(start).Seconds())
		metrics.MCPToolCallsTotal.WithLabelValues(name, metrics.ResultLabel(err)).Inc()
		return res, out, err
	}
}

// resolvePlugin loads an active plugin by name, normalising "not found" to a
// stable error string the LLM can act on.
func (a *App) resolvePlugin(ctx context.Context, name string) (*Plugin, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return nil, errors.New("plugin is required")
	}
	p, err := a.loadPluginByName(ctx, name)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("plugin %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (a *App) resolveSkill(ctx context.Context, pluginID, name string) (*Skill, error) {
	name = strings.TrimSpace(strings.ToLower(name))
	if name == "" {
		return nil, errors.New("skill is required")
	}
	s, err := a.loadActiveSkill(ctx, pluginID, name)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("skill %q not found", name)
	}
	if err != nil {
		return nil, err
	}
	return s, nil
}

// --- tools -----------------------------------------------------------------

func (a *App) registerMCPTools(s *mcp.Server) {
	a.addToolListPlugins(s)
	a.addToolGetPlugin(s)
	a.addToolGetSkill(s)
	a.addToolCreateSkill(s)
	a.addToolUpdateSkill(s)
	a.addToolListSkillFiles(s)
	a.addToolGetSkillFile(s)
	a.addToolUpsertSkillFile(s)
}

func (a *App) addToolListPlugins(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_plugins",
		Title:       "List plugins",
		Description: "List all active plugins in the marketplace.",
	}, instrumentMCP("list_plugins", func(ctx context.Context, _ *mcp.CallToolRequest, _ mcpEmptyIn) (*mcp.CallToolResult, mcpListPluginsOut, error) {
		var zero mcpListPluginsOut
		if userFromCtx(ctx) == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		plugins, err := a.queryPlugins(ctx,
			`WHERE p.deleted_at IS NULL ORDER BY p.updated_at DESC`)
		if err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		out := mcpListPluginsOut{Plugins: make([]mcpPluginSummary, 0, len(plugins))}
		for _, p := range plugins {
			out.Plugins = append(out.Plugins, mcpPluginSummary{
				Name:        p.Name,
				Description: p.Description,
				Version:     p.Version,
				OwnerName:   p.OwnerName,
				UpdatedAt:   p.UpdatedAt,
			})
		}
		return okResult(fmt.Sprintf("%d plugin(s)", len(out.Plugins)), out)
	}))
}

func (a *App) addToolGetPlugin(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_plugin",
		Title:       "Get plugin",
		Description: "Read a plugin's metadata and the list of its skills (names + descriptions, no bodies).",
	}, instrumentMCP("get_plugin", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpPluginRefIn) (*mcp.CallToolResult, mcpPluginDetail, error) {
		var zero mcpPluginDetail
		if userFromCtx(ctx) == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		skills, err := a.loadSkillsForPlugin(ctx, p.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		out := mcpPluginDetail{
			Name:        p.Name,
			Description: p.Description,
			Version:     p.Version,
			OwnerName:   p.OwnerName,
			License:     p.License,
			Homepage:    p.Homepage,
			Skills:      make([]mcpSkillSummary, 0, len(skills)),
		}
		for _, sk := range skills {
			out.Skills = append(out.Skills, mcpSkillSummary{
				Name:        sk.Name,
				Description: sk.Description,
				UpdatedAt:   sk.UpdatedAt,
			})
		}
		return okResult(fmt.Sprintf("plugin %q v%s, %d skill(s)", p.Name, p.Version, len(skills)), out)
	}))
}

func (a *App) addToolGetSkill(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_skill",
		Title:       "Get skill",
		Description: "Read a skill's description, SKILL.md body, and the list of its supporting files.",
	}, instrumentMCP("get_skill", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSkillRefIn) (*mcp.CallToolResult, mcpSkillDetail, error) {
		var zero mcpSkillDetail
		if userFromCtx(ctx) == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		sk, err := a.resolveSkill(ctx, p.ID, in.Skill)
		if err != nil {
			return nil, zero, err
		}
		files, err := a.loadSkillFileSummaries(ctx, sk.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		out := mcpSkillDetail{
			Plugin:      p.Name,
			Name:        sk.Name,
			Description: sk.Description,
			Body:        sk.Body,
			Files:       make([]mcpSkillFileBrief, 0, len(files)),
		}
		for _, f := range files {
			out.Files = append(out.Files, mcpSkillFileBrief{Path: f.Path, IsBinary: f.IsBinary, SizeBytes: f.SizeBytes})
		}
		return okResult(fmt.Sprintf("skill %q in %q (%d file(s))", sk.Name, p.Name, len(files)), out)
	}))
}

func (a *App) addToolCreateSkill(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "create_skill",
		Title:       "Create skill",
		Description: "Add a new skill to a plugin. Bumps the plugin version and rewrites the git repo.",
	}, instrumentMCP("create_skill", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpCreateSkillIn) (*mcp.CallToolResult, mcpStatusOut, error) {
		var zero mcpStatusOut
		user := userFromCtx(ctx)
		if user == nil {
			return nil, zero, errors.New("unauthenticated")
		}

		name := strings.TrimSpace(strings.ToLower(in.Name))
		if !slugRe.MatchString(name) {
			return nil, zero, errors.New("name must be 3-64 chars, lowercase, [a-z0-9-]")
		}
		if strings.TrimSpace(in.Description) == "" {
			return nil, zero, errors.New("description is required")
		}

		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}

		priorSkillCount, err := a.pluginSkillCount(ctx, p.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}

		var id string
		err = a.DB.QueryRowContext(ctx, `
			INSERT INTO skills (plugin_id, name, description, body, created_by, updated_by)
			VALUES ($1, $2, $3, $4, $5, $5) RETURNING id
		`, p.ID, name, in.Description, in.Body, user.ID).Scan(&id)
		if err != nil {
			if isUniqueViolation(err) {
				return nil, zero, errors.New("skill with that name already exists")
			}
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.recordSkillVersion(ctx, a.DB, id, "create", name, in.Description, in.Body, "", user.ID); err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if priorSkillCount == 0 {
			if err := a.touchPluginUpdatedAt(ctx, p.ID); err != nil {
				return nil, zero, fmt.Errorf("db error: %w", err)
			}
		} else {
			if err := a.bumpAndPersistPluginVersion(ctx, p, semver.BumpMajor); err != nil {
				return nil, zero, fmt.Errorf("db error: %w", err)
			}
		}
		if err := a.materializePlugin(ctx, p); err != nil {
			return nil, zero, fmt.Errorf("git materialize: %w", err)
		}
		metrics.SkillMutationsTotal.WithLabelValues("create", "success").Inc()
		return okResult(
			fmt.Sprintf("created skill %q in %q (plugin now v%s)", name, p.Name, p.Version),
			mcpStatusOut{OK: true, Version: p.Version, Message: "skill created"},
		)
	}))
}

func (a *App) addToolUpdateSkill(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "update_skill",
		Title:       "Update skill",
		Description: "Replace a skill's description and SKILL.md body. Bumps the plugin version and rewrites the git repo.",
	}, instrumentMCP("update_skill", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpUpdateSkillIn) (*mcp.CallToolResult, mcpStatusOut, error) {
		var zero mcpStatusOut
		user := userFromCtx(ctx)
		if user == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		existing, err := a.resolveSkill(ctx, p.ID, in.Skill)
		if err != nil {
			return nil, zero, err
		}
		if _, err := a.DB.ExecContext(ctx, `
			UPDATE skills SET description = $1, body = $2, updated_at = now(), updated_by = $3
			WHERE id = $4
		`, in.Description, in.Body, user.ID, existing.ID); err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.recordSkillVersion(ctx, a.DB, existing.ID, "update", existing.Name, in.Description, in.Body, existing.ExtraFrontmatter, user.ID); err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.bumpAndPersistPluginVersion(ctx, p, semver.BumpKindForSizeChange(len(existing.Body), len(in.Body))); err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.materializePlugin(ctx, p); err != nil {
			return nil, zero, fmt.Errorf("git materialize: %w", err)
		}
		metrics.SkillMutationsTotal.WithLabelValues("update", "success").Inc()
		return okResult(
			fmt.Sprintf("updated skill %q in %q (plugin now v%s)", existing.Name, p.Name, p.Version),
			mcpStatusOut{OK: true, Version: p.Version, Message: "skill updated"},
		)
	}))
}

func (a *App) addToolListSkillFiles(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "list_skill_files",
		Title:       "List skill files",
		Description: "List supporting files attached to a skill (paths + sizes, no content).",
	}, instrumentMCP("list_skill_files", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSkillRefIn) (*mcp.CallToolResult, mcpListSkillFilesOut, error) {
		var zero mcpListSkillFilesOut
		if userFromCtx(ctx) == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		sk, err := a.resolveSkill(ctx, p.ID, in.Skill)
		if err != nil {
			return nil, zero, err
		}
		files, err := a.loadSkillFileSummaries(ctx, sk.ID)
		if err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		out := mcpListSkillFilesOut{
			Plugin: p.Name,
			Skill:  sk.Name,
			Files:  make([]mcpSkillFileBrief, 0, len(files)),
		}
		for _, f := range files {
			out.Files = append(out.Files, mcpSkillFileBrief{Path: f.Path, IsBinary: f.IsBinary, SizeBytes: f.SizeBytes})
		}
		return okResult(fmt.Sprintf("%d file(s) in %s/%s", len(files), p.Name, sk.Name), out)
	}))
}

func (a *App) addToolGetSkillFile(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_skill_file",
		Title:       "Get skill file",
		Description: "Read one supporting file from a skill. Binary files are returned as base64 (isBinary=true).",
	}, instrumentMCP("get_skill_file", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpSkillFileRefIn) (*mcp.CallToolResult, mcpSkillFileOut, error) {
		var zero mcpSkillFileOut
		if userFromCtx(ctx) == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		sk, err := a.resolveSkill(ctx, p.ID, in.Skill)
		if err != nil {
			return nil, zero, err
		}
		pth, err := validateSkillFilePath(in.Path)
		if err != nil {
			return nil, zero, err
		}
		f, err := a.loadSingleSkillFile(ctx, sk.ID, pth)
		if err == sql.ErrNoRows {
			return nil, zero, fmt.Errorf("file %q not found", pth)
		}
		if err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		out := mcpSkillFileOut{
			Path:      f.Path,
			IsBinary:  f.IsBinary,
			SizeBytes: f.SizeBytes,
			Content:   f.Content,
		}
		return okResult(fmt.Sprintf("%s (%d bytes)", f.Path, f.SizeBytes), out)
	}))
}

func (a *App) addToolUpsertSkillFile(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "upsert_skill_file",
		Title:       "Create or update skill file",
		Description: "Write a supporting file. Files may live at the skill root (bare filename, e.g. config.json) or under a folder; conventional folders are scripts/, references/, and assets/, but arbitrary folder names are also accepted. SKILL.md is reserved. Bumps the plugin patch version and rewrites the git repo.",
	}, instrumentMCP("upsert_skill_file", func(ctx context.Context, _ *mcp.CallToolRequest, in mcpUpsertSkillFileIn) (*mcp.CallToolResult, mcpStatusOut, error) {
		var zero mcpStatusOut
		user := userFromCtx(ctx)
		if user == nil {
			return nil, zero, errors.New("unauthenticated")
		}
		p, err := a.resolvePlugin(ctx, in.Plugin)
		if err != nil {
			return nil, zero, err
		}
		sk, err := a.resolveSkill(ctx, p.ID, in.Skill)
		if err != nil {
			return nil, zero, err
		}
		pth, err := validateSkillFilePath(in.Path)
		if err != nil {
			return nil, zero, err
		}

		isBin := in.IsBinary
		req := skillFileUpsertReq{Content: in.Content, IsBinary: &isBin}
		data, isBinary, err := decodeFileContent(&req)
		if err != nil {
			return nil, zero, err
		}
		if len(data) > maxSkillFileBytes {
			return nil, zero, fmt.Errorf("file exceeds %d byte limit", maxSkillFileBytes)
		}

		priorTotal, priorCount, err := a.skillTotalBytes(ctx, sk.ID, pth)
		if err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if priorTotal+len(data) > maxSkillTotalBytes {
			return nil, zero, fmt.Errorf("skill total would exceed %d bytes", maxSkillTotalBytes)
		}
		exists := true
		if _, err := a.loadSingleSkillFile(ctx, sk.ID, pth); err == sql.ErrNoRows {
			exists = false
		} else if err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if !exists && priorCount+1 > maxSkillFileCount {
			return nil, zero, fmt.Errorf("skill exceeds file count limit (%d)", maxSkillFileCount)
		}

		var contentText sql.NullString
		var contentBlob []byte
		if isBinary {
			contentBlob = data
		} else {
			contentText = sql.NullString{String: string(data), Valid: true}
		}

		if _, err := a.DB.ExecContext(ctx, `
			INSERT INTO skill_files (skill_id, path, content_text, content_blob, is_binary, size_bytes)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (skill_id, path) DO UPDATE SET
				content_text = EXCLUDED.content_text,
				content_blob = EXCLUDED.content_blob,
				is_binary = EXCLUDED.is_binary,
				size_bytes = EXCLUDED.size_bytes,
				updated_at = now()
		`, sk.ID, pth, contentText, contentBlob, isBinary, len(data)); err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.recordSkillVersion(ctx, a.DB, sk.ID, "update", sk.Name, sk.Description, sk.Body, sk.ExtraFrontmatter, user.ID); err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.bumpAndPersistPluginVersion(ctx, p, semver.BumpPatch); err != nil {
			return nil, zero, fmt.Errorf("db error: %w", err)
		}
		if err := a.materializePlugin(ctx, p); err != nil {
			return nil, zero, fmt.Errorf("git materialize: %w", err)
		}
		metrics.SkillFileMutationsTotal.WithLabelValues("upsert", "success").Inc()
		return okResult(
			fmt.Sprintf("wrote %s to %s/%s (plugin now v%s)", pth, p.Name, sk.Name, p.Version),
			mcpStatusOut{OK: true, Version: p.Version, Message: "file written"},
		)
	}))
}
