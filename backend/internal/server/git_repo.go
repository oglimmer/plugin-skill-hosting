package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/sosedoff/gitkit"

	"marketplace/internal/metrics"
)

// gitCredentialURLRe matches the userinfo segment of an HTTP(S) URL so we can
// scrub it from log lines and error messages.
var gitCredentialURLRe = regexp.MustCompile(`(https?://)[^/\s@]+@`)

func scrubGitCredentials(s string) string {
	return gitCredentialURLRe.ReplaceAllString(s, "${1}REDACTED@")
}

// PluginManifestSchemaURL points at the SchemaStore manifest for the
// claude-code plugin.json file. Embedded as "$schema" so editors can
// validate and autocomplete the generated manifest.
const PluginManifestSchemaURL = "https://json.schemastore.org/claude-code-plugin-manifest.json"

type pluginManifest struct {
	Schema      string             `json:"$schema,omitempty"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	Version     string             `json:"version,omitempty"`
	Author      *marketplaceAuthor `json:"author,omitempty"`
	Homepage    string             `json:"homepage,omitempty"`
	License     string             `json:"license,omitempty"`
	Repository  string             `json:"repository,omitempty"`
}

func (a *App) repoPath(name string) string {
	return filepath.Join(a.Cfg.DataDir, "repos", name+".git")
}

// pluginRepoURL returns the public clone URL for a plugin's git repo,
// without any embedded auth token. Used in generated plugin.json manifests
// so the manifest stays user-agnostic and safe to commit.
func (a *App) pluginRepoURL(name string) string {
	base := strings.TrimRight(a.Cfg.PublicBaseURL, "/")
	if base == "" {
		return ""
	}
	return base + "/git/" + name + ".git"
}

func (a *App) workPath(name string) string {
	return filepath.Join(a.Cfg.DataDir, "work", name)
}

func runGit(ctx context.Context, dir string, args ...string) (string, error) {
	return runGitInternal(ctx, dir, nil, args...)
}

// runGitRedacted is like runGit but treats args as containing credentials:
// the error message uses redactedArgs in place of args and the git stderr
// is scrubbed before being returned.
func runGitRedacted(ctx context.Context, dir string, redactedArgs []string, args ...string) (string, error) {
	return runGitInternal(ctx, dir, redactedArgs, args...)
}

// gitOpTimeout bounds any single git invocation so a hung clone/fetch/push
// (e.g. an unreachable external remote) can't block a request goroutine or
// shutdown forever, even when the caller's context never cancels on its own.
const gitOpTimeout = 2 * time.Minute

func runGitInternal(ctx context.Context, dir string, redactedArgs []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitOpTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=marketplace",
		"GIT_AUTHOR_EMAIL=marketplace@local",
		"GIT_COMMITTER_NAME=marketplace",
		"GIT_COMMITTER_EMAIL=marketplace@local",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		shown := args
		outStr := string(out)
		if redactedArgs != nil {
			shown = redactedArgs
			outStr = scrubGitCredentials(outStr)
		}
		return string(out), fmt.Errorf("git %s: %w: %s", strings.Join(shown, " "), err, outStr)
	}
	return string(out), nil
}

func (a *App) ensureBareRepo(ctx context.Context, name string) error {
	bare := a.repoPath(name)
	if _, err := os.Stat(filepath.Join(bare, "HEAD")); err == nil {
		return nil
	}
	if err := os.MkdirAll(bare, 0o755); err != nil {
		return err
	}
	if _, err := runGit(ctx, "", "init", "--bare", "-b", "main", bare); err != nil {
		return err
	}
	if _, err := runGit(ctx, bare, "config", "http.receivepack", "false"); err != nil {
		return err
	}
	if _, err := runGit(ctx, bare, "config", "http.uploadpack", "true"); err != nil {
		return err
	}
	return nil
}

func (a *App) ensureWorkTree(ctx context.Context, name string) error {
	work := a.workPath(name)
	bare := a.repoPath(name)
	if _, err := os.Stat(filepath.Join(work, ".git")); err == nil {
		return nil
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	if _, err := runGit(ctx, work, "init", "-b", "main"); err != nil {
		return err
	}
	if _, err := runGit(ctx, work, "remote", "add", "origin", bare); err != nil {
		return err
	}
	return nil
}

func (a *App) removeRepo(name string) {
	if err := a.removeInternalRepo(name); err != nil {
		log.Printf("WARN: remove internal git repo %q: %v", name, err)
	}
	if err := a.syncExternalDeletePlugin(context.Background(), name); err != nil {
		log.Printf("WARN: external git delete %q: %v", name, err)
	}
}

func (a *App) removeInternalRepo(name string) error {
	var errs []error
	if err := os.RemoveAll(a.repoPath(name)); err != nil {
		errs = append(errs, err)
	}
	if err := os.RemoveAll(a.workPath(name)); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (a *App) materializePlugin(ctx context.Context, p *Plugin) error {
	start := time.Now()
	err := a.materializePluginInner(ctx, p)
	metrics.GitMaterializeDuration.Observe(time.Since(start).Seconds())
	metrics.GitMaterializeTotal.WithLabelValues(metrics.ResultLabel(err)).Inc()
	return err
}

// materializeTimeout bounds a single post-commit git rebuild. Generous because
// a rebuild runs several git ops (each already capped by gitOpTimeout) plus an
// optional external push; the cap only exists so the detached work can't run
// forever.
const materializeTimeout = 5 * time.Minute

// materializePluginDetached rebuilds a plugin's git repo on a context that is
// independent of the caller's request/MCP context. Use this for the
// materialization that happens AFTER a DB transaction commits: the DB change is
// already durable, so a client disconnect or tool-call timeout must not be able
// to abort the git rebuild and leave /git/... and marketplace.json diverged
// from the database. The bounded timeout still guarantees forward progress.
func (a *App) materializePluginDetached(p *Plugin) error {
	ctx, cancel := context.WithTimeout(context.Background(), materializeTimeout)
	defer cancel()
	return a.materializePlugin(ctx, p)
}

func (a *App) materializePluginInner(ctx context.Context, p *Plugin) error {
	if err := a.ensureBareRepo(ctx, p.Name); err != nil {
		return err
	}
	if err := a.ensureWorkTree(ctx, p.Name); err != nil {
		return err
	}
	work := a.workPath(p.Name)

	if err := wipeWorkTree(work); err != nil {
		return err
	}
	if err := a.renderPluginInto(ctx, p, work); err != nil {
		return err
	}

	if _, err := runGit(ctx, work, "add", "-A"); err != nil {
		return err
	}
	out, err := runGit(ctx, work, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return a.syncExternalPushPlugin(ctx, p)
	}
	if _, err := runGit(ctx, work, "commit", "-m", "Update plugin contents"); err != nil {
		return err
	}
	if _, err := runGit(ctx, work, "push", "origin", "HEAD:refs/heads/main", "--force"); err != nil {
		return err
	}
	return a.syncExternalPushPlugin(ctx, p)
}

// renderPluginInto writes the full plugin file tree (manifest, skills,
// supporting files, README) into targetDir. The caller is responsible for
// emptying targetDir first if a clean slate is desired — renderPluginInto
// only creates and overwrites, it does not delete stale files.
func (a *App) renderPluginInto(ctx context.Context, p *Plugin, targetDir string) error {
	if err := os.MkdirAll(targetDir, 0o755); err != nil {
		return err
	}
	manifestDir := filepath.Join(targetDir, ".claude-plugin")
	if err := os.MkdirAll(manifestDir, 0o755); err != nil {
		return err
	}
	manifest := pluginManifest{
		Schema:      PluginManifestSchemaURL,
		Name:        p.Name,
		Description: p.Description,
		Version:     p.Version,
		Homepage:    p.Homepage,
		License:     p.License,
		Repository:  a.pluginRepoURL(p.Name),
	}
	if p.AuthorName != "" || p.AuthorEmail != "" {
		manifest.Author = &marketplaceAuthor{Name: p.AuthorName, Email: p.AuthorEmail}
	}
	mb, _ := json.MarshalIndent(manifest, "", "  ")
	if err := os.WriteFile(filepath.Join(manifestDir, "plugin.json"), append(mb, '\n'), 0o644); err != nil {
		return err
	}

	skills, err := a.loadSkillsForPlugin(ctx, p.ID)
	if err != nil {
		return err
	}
	if len(skills) > 0 {
		skillsRoot := filepath.Join(targetDir, "skills")
		if err := os.MkdirAll(skillsRoot, 0o755); err != nil {
			return err
		}
		for _, s := range skills {
			dir := filepath.Join(skillsRoot, s.Name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			content := buildSkillMarkdown(s)
			if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
				return err
			}
			files, err := a.loadSkillFiles(ctx, s.ID)
			if err != nil {
				return err
			}
			for _, f := range files {
				if err := writeSkillFileToWorkTree(dir, f); err != nil {
					return err
				}
			}
		}
	}

	readme := fmt.Sprintf("# %s\n\n%s\n\nGenerated by self-hosted marketplace.\n", p.Name, p.Description)
	return os.WriteFile(filepath.Join(targetDir, "README.md"), []byte(readme), 0o644)
}

func wipeWorkTree(work string) error {
	entries, err := os.ReadDir(work)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == ".git" {
			continue
		}
		if err := os.RemoveAll(filepath.Join(work, e.Name())); err != nil {
			return err
		}
	}
	return nil
}

// writeSkillFileToWorkTree decodes a SkillFile (text or base64-binary) and
// writes it under skillDir at its relative path, creating intermediate dirs
// as needed. Path safety has already been enforced at upload time, but we
// re-anchor under skillDir here as a defence in depth.
func writeSkillFileToWorkTree(skillDir string, f SkillFile) error {
	rel := filepath.FromSlash(f.Path)
	full := filepath.Join(skillDir, rel)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	var data []byte
	if f.IsBinary {
		decoded, err := base64.StdEncoding.DecodeString(f.Content)
		if err != nil {
			return fmt.Errorf("decode %s: %w", f.Path, err)
		}
		data = decoded
	} else {
		data = []byte(f.Content)
	}
	return os.WriteFile(full, data, 0o644)
}

func buildSkillMarkdown(s Skill) string {
	var b strings.Builder
	b.WriteString("---\n")
	b.WriteString("name: " + s.Name + "\n")
	desc := strings.ReplaceAll(s.Description, "\n", " ")
	b.WriteString("description: " + desc + "\n")
	if extra := strings.TrimSpace(s.ExtraFrontmatter); extra != "" {
		b.WriteString(extra)
		b.WriteString("\n")
	}
	b.WriteString("---\n\n")
	body := s.Body
	if body == "" {
		body = "## " + s.Name + "\n\n" + s.Description + "\n"
	}
	if !strings.HasSuffix(body, "\n") {
		body += "\n"
	}
	b.WriteString(body)
	return b.String()
}

// RematerializeAll re-builds the git repo for every non-deleted plugin from
// the database. It is intended to be called in a background goroutine on
// startup when the data dir is ephemeral (REMATERIALIZE_ON_STARTUP=true).
func (a *App) RematerializeAll(ctx context.Context) {
	// Always flip readiness on the way out: a failure to list or rebuild must
	// not leave /readyz wedged at false until a restart. Individual plugin
	// failures are logged below; the process is still able to serve traffic.
	defer a.MarkReady()

	plugins, err := a.queryPlugins(ctx, `WHERE p.deleted_at IS NULL`)
	if err != nil {
		log.Printf("rematerialize: list plugins: %v", err)
		return
	}
	log.Printf("rematerialize: rebuilding %d plugin repo(s)", len(plugins))
	start := time.Now()
	for i := range plugins {
		if err := a.materializePlugin(ctx, &plugins[i]); err != nil {
			log.Printf("rematerialize: plugin %q: %v", plugins[i].Name, err)
		}
	}
	log.Printf("rematerialize: done in %s", time.Since(start).Round(time.Millisecond))
}

func (a *App) gitHandler() http.Handler {
	reposDir := filepath.Join(a.Cfg.DataDir, "repos")
	service := gitkit.New(gitkit.Config{
		Dir:        reposDir,
		AutoCreate: false,
		Auth:       false,
	})
	if err := service.Setup(); err != nil {
		panic(fmt.Sprintf("gitkit setup: %v", err))
	}
	return http.StripPrefix("/git", service)
}
