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
	"sync"
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

// ensureWorkTree makes sure a plugin has a work tree wired to its bare repo
// and seeded onto the published tip whenever that tip exists.
//
// Seeding is what keeps published history durable: without it a re-created
// work tree starts from a fresh root commit, and a non-force push is rejected
// (or, historically, a force-push would orphan every commit clients have
// already fetched or pinned). The work tree is disposable state (a pod restart
// or a lost volume is enough to remove it), so this path is routine rather
// than exceptional.
//
// Seeding runs on every call — not only when the work tree is first created —
// so a half-finished init (ls-remote/fetch/reset failed after git init) or a
// work tree that drifted from bare recovers on the next materialize instead of
// wedging on a permanent non-fast-forward push.
//
// If /data itself is ephemeral (helm: persistence disabled + rematerialize on
// startup) the bare repo dies alongside the work tree and there is nothing to
// seed from — history genuinely restarts there, and that mode should not be
// presented as offering durable git history.
func (a *App) ensureWorkTree(ctx context.Context, name string) error {
	work := a.workPath(name)
	bare := a.repoPath(name)

	if _, err := os.Stat(filepath.Join(work, ".git")); err != nil {
		if err := a.initWorkTree(ctx, work, bare); err != nil {
			return err
		}
	} else if err := a.ensureWorkTreeOrigin(ctx, work, bare); err != nil {
		return err
	}

	return a.seedWorkTreeFromBare(ctx, work, bare)
}

// initWorkTree creates a fresh work tree with origin pointing at the bare repo.
// On any failure after partial creation the directory is removed so the next
// call can start clean instead of treating a broken tree as ready.
func (a *App) initWorkTree(ctx context.Context, work, bare string) error {
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	if _, err := runGit(ctx, work, "init", "-b", "main"); err != nil {
		_ = os.RemoveAll(work)
		return err
	}
	if _, err := runGit(ctx, work, "remote", "add", "origin", bare); err != nil {
		_ = os.RemoveAll(work)
		return err
	}
	return nil
}

// ensureWorkTreeOrigin makes sure origin points at the bare path. Uses set-url
// when the remote already exists (normal case) and falls back to add.
func (a *App) ensureWorkTreeOrigin(ctx context.Context, work, bare string) error {
	if _, err := runGit(ctx, work, "remote", "set-url", "origin", bare); err == nil {
		return nil
	}
	if _, err := runGit(ctx, work, "remote", "add", "origin", bare); err != nil {
		return err
	}
	return nil
}

// seedWorkTreeFromBare resets the work tree onto the bare repo's main tip when
// that ref exists. No-ops on a brand-new bare repo with no history so the first
// commit is still a legitimate root.
func (a *App) seedWorkTreeFromBare(ctx context.Context, work, bare string) error {
	// ls-remote separates "no history yet" (exit 0, empty output) from a real
	// git failure (non-zero exit), which rev-parse on a missing ref does not.
	out, err := runGit(ctx, work, "ls-remote", "--heads", bare, "main")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if _, err := runGit(ctx, work, "fetch", "origin", "main"); err != nil {
		return err
	}
	// DB is the source of truth for file content; materialize always wipe+renders
	// after this, so a hard reset only repositions the branch tip. It recovers
	// unborn main, incomplete seeds, and diverged local commits alike.
	if _, err := runGit(ctx, work, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return err
	}
	return nil
}

// pluginMaterializeLock returns the per-plugin mutex used to serialize
// materialize and remove of that plugin's git state.
//
// Entries are never deleted, including on plugin removal: dropping one while a
// goroutine still holds it would hand the next caller a fresh mutex and lose
// the serialization exactly when it matters. The map is bounded by the number
// of distinct plugin names the process has seen, which is small enough that
// leaking a mutex per name is the cheaper trade.
func (a *App) pluginMaterializeLock(name string) *sync.Mutex {
	v, _ := a.materializeLocks.LoadOrStore(name, &sync.Mutex{})
	return v.(*sync.Mutex)
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
	mu := a.pluginMaterializeLock(name)
	mu.Lock()
	defer mu.Unlock()

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
	if err := a.publishWorkTree(ctx, p.Name, func(work string) error {
		return a.renderPluginInto(ctx, p, work)
	}); err != nil {
		return err
	}
	return a.syncExternalPushPlugin(ctx, p)
}

// publishWorkTree runs the git half of a materialize under the plugin's lock:
// it makes sure the bare repo and a seeded work tree exist, lets render rewrite
// the emptied tree from the database, then commits and pushes.
//
// render receives the work tree path and is called with the per-plugin lock
// held; it must be self-contained (no further git calls on the same plugin).
func (a *App) publishWorkTree(ctx context.Context, name string, render func(work string) error) error {
	mu := a.pluginMaterializeLock(name)
	mu.Lock()
	defer mu.Unlock()

	if err := a.ensureBareRepo(ctx, name); err != nil {
		return err
	}
	if err := a.ensureWorkTree(ctx, name); err != nil {
		return err
	}
	work := a.workPath(name)

	if err := wipeWorkTree(work); err != nil {
		return err
	}
	if err := render(work); err != nil {
		return err
	}

	if _, err := runGit(ctx, work, "add", "-A"); err != nil {
		return err
	}
	out, err := runGit(ctx, work, "status", "--porcelain")
	if err != nil {
		return err
	}
	switch {
	case strings.TrimSpace(out) != "":
		if _, err := runGit(ctx, work, "commit", "-m", "Update plugin contents"); err != nil {
			return err
		}
	case !workTreeHasCommit(ctx, work):
		// Nothing rendered and no history at all — there is nothing to publish.
		return nil
	}
	// Pushed even when the content was unchanged: bare main can be missing or
	// behind while the work tree still holds the content (a lost or partially
	// removed bare repo), and that state is otherwise unrecoverable — every
	// later materialize would short-circuit on the same clean status.
	//
	// Deliberately not --force. ensureWorkTree reseeds from bare every call and
	// materialize is serialized per plugin, so HEAD is bare's tip or a
	// descendant of it and legitimate pushes fast-forward. A rejection here
	// means something outside this process rewrote the bare tip (or a bug left
	// the work tree diverged mid-flight). Failing loudly surfaces that;
	// --force silently discarded published history instead.
	if _, err := runGit(ctx, work, "push", "origin", "HEAD:refs/heads/main"); err != nil {
		return err
	}
	return nil
}

// workTreeHasCommit reports whether the work tree has any commit on HEAD, i.e.
// whether there is a tip a push could publish.
func workTreeHasCommit(ctx context.Context, work string) bool {
	_, err := runGit(ctx, work, "rev-parse", "--verify", "HEAD")
	return err == nil
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
			// Locked skills are withdrawn from every git surface (internal repo
			// and the external mirror, which both render through here). They stay
			// in the DB and the web UI, just never get committed.
			if s.Locked {
				continue
			}
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
