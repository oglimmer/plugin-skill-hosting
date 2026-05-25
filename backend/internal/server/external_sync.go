package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"marketplace/internal/config"
)

// externalImportTimeout caps a single webhook-triggered import. Generous
// because a fetch + per-plugin DB reconcile against a large catalogue can
// genuinely take a minute or two; smaller than the 5-minute Prometheus
// scrape interval so a hung import still releases the mutex.
const externalImportTimeout = 2 * time.Minute

// externalSync mirrors the marketplace contents to a single external git
// repository (GitHub, GitLab, …) one-way: every plugin write or delete
// re-renders the affected plugins/<name>/ subtree in a checked-out clone of
// the remote, commits, and pushes. Disabled when cfg.ExternalGitRemoteURL is
// empty (App.ExternalSync is then nil).
//
// All operations are serialised behind mu — push and (future) pull never
// interleave, so callers don't need to coordinate.
type externalSync struct {
	mu      sync.Mutex
	cfg     config.Config
	workDir string // local clone of the external repo

	// rootWriter, when non-nil, is invoked on every push / delete (and at
	// the end of an import that changed the plugin set) to re-render
	// repo-root artefacts that depend on global state — currently the
	// `.claude-plugin/marketplace.json` snapshot. App sets it during
	// InitExternalSync so the external_sync.go layer stays DB-free.
	rootWriter func(ctx context.Context) error
}

const externalSyncReadmePreamble = "# Marketplace mirror\n\n" +
	"This repository is kept in sync with a self-hosted Claude Code plugin\n" +
	"marketplace. Each subdirectory under `plugins/` contains a materialised\n" +
	"plugin: `.claude-plugin/plugin.json`, `skills/<name>/SKILL.md` and optional\n" +
	"supporting files under `scripts/`, `references/`, or `assets/`.\n\n" +
	"## How sync works\n\n" +
	"- **Outbound** (marketplace → here): every plugin create, update, or delete\n" +
	"  in the marketplace UI / API / MCP rewrites the affected `plugins/<name>/`\n" +
	"  subtree, commits as `marketplace <marketplace@local>`, and pushes.\n" +
	"- **Inbound** (here → marketplace): a push webhook fires the marketplace\n" +
	"  backend, which fetches the new commits and reconciles each changed\n" +
	"  `plugins/<name>/` subtree into its database. Imported edits show up in\n" +
	"  the skill's edit history attributed to the commit author (matched by\n" +
	"  email to a marketplace user, or to the `external-git-sync` system user).\n\n" +
	"## Editing\n\n" +
	"You can edit `SKILL.md` files, the `plugin.json` manifest, or supporting\n" +
	"files directly in this repo and `git push`. The webhook will import the\n" +
	"change. **Conflict policy is remote-wins**: if a marketplace user edited\n" +
	"the same skill between syncs, your push overwrites their version (the\n" +
	"earlier state remains restorable from the skill's edit history).\n\n" +
	"This README is regenerated on every sync; edits to it will be overwritten.\n"

// newExternalSync constructs the syncer if the feature is enabled in cfg.
// Returns (nil, nil) when disabled.
func newExternalSync(cfg config.Config, dataDir string) *externalSync {
	if strings.TrimSpace(cfg.ExternalGitRemoteURL) == "" {
		return nil
	}
	return &externalSync{
		cfg:     cfg,
		workDir: filepath.Join(dataDir, "external", "marketplace"),
	}
}

// initialize prepares the local clone. If the workDir already has a .git, it
// fetches and hard-resets to origin/<branch>. Otherwise it clones; if the
// remote is empty (no branch yet) it initialises a fresh repo and pushes an
// initial commit so subsequent operations have something to track.
func (es *externalSync) initialize(ctx context.Context) error {
	es.mu.Lock()
	defer es.mu.Unlock()

	if es.cfg.ExternalGitToken == "" && isHTTPRemote(es.cfg.ExternalGitRemoteURL) {
		log.Printf("WARN: external git sync is enabled for %s but EXTERNAL_GIT_TOKEN is empty — push will likely fail",
			scrubGitCredentials(es.cfg.ExternalGitRemoteURL))
	}

	if _, err := os.Stat(filepath.Join(es.workDir, ".git")); err == nil {
		return es.refreshFromRemote()
	}
	if err := os.MkdirAll(filepath.Dir(es.workDir), 0o755); err != nil {
		return fmt.Errorf("create external workdir parent: %w", err)
	}

	if err := es.cloneFromRemote(); err == nil {
		log.Printf("external git: cloned %s into %s", scrubGitCredentials(es.cfg.ExternalGitRemoteURL), es.workDir)
		return nil
	} else {
		log.Printf("external git: clone failed (%v); initialising empty repo", err)
	}
	return es.initEmptyRepo()
}

// cloneFromRemote performs `git clone --branch <branch> <pushURL> <workDir>`.
// On success it rewrites origin to the credential-free URL so it doesn't end
// up in `git remote -v` or accidental log output.
func (es *externalSync) cloneFromRemote() error {
	pushURL := es.credentialedURL()
	if _, err := os.Stat(es.workDir); err == nil {
		if err := os.RemoveAll(es.workDir); err != nil {
			return fmt.Errorf("remove stale workdir: %w", err)
		}
	}
	_, err := es.runGitRedacted("",
		[]string{"clone", "--branch", es.cfg.ExternalGitBranch, scrubGitCredentials(pushURL), es.workDir},
		"clone", "--branch", es.cfg.ExternalGitBranch, pushURL, es.workDir,
	)
	if err != nil {
		return err
	}
	if _, err := runGit(es.workDir, "remote", "set-url", "origin", es.cfg.ExternalGitRemoteURL); err != nil {
		return fmt.Errorf("set-url origin: %w", err)
	}
	return nil
}

// initEmptyRepo creates a fresh git repo with an initial README commit and
// pushes it to the remote branch. Used when the external remote exists but
// has no branch yet (e.g. brand-new GitHub repo).
func (es *externalSync) initEmptyRepo() error {
	if err := os.MkdirAll(es.workDir, 0o755); err != nil {
		return err
	}
	if _, err := runGit(es.workDir, "init", "-b", es.cfg.ExternalGitBranch); err != nil {
		return fmt.Errorf("init external repo: %w", err)
	}
	if _, err := runGit(es.workDir, "remote", "add", "origin", es.cfg.ExternalGitRemoteURL); err != nil {
		return fmt.Errorf("remote add origin: %w", err)
	}
	if err := es.writeRootReadme(); err != nil {
		return err
	}
	if _, err := runGit(es.workDir, "add", "-A"); err != nil {
		return err
	}
	if _, err := es.runGitAsConfigured(es.workDir, nil, "commit", "-m", "Initial commit"); err != nil {
		return fmt.Errorf("initial commit: %w", err)
	}
	return es.pushCurrentBranch()
}

// refreshFromRemote fetches the configured branch and hard-resets the local
// HEAD to it. Any uncommitted or unpushed local state is discarded — DB is
// the source of truth, so we re-render from there. Using FETCH_HEAD avoids
// having to maintain a remote-tracking ref when the remote was added with a
// credential-free URL but operations use an explicit credentialed URL.
func (es *externalSync) refreshFromRemote() error {
	pushURL := es.credentialedURL()
	branch := es.cfg.ExternalGitBranch
	_, err := es.runGitRedacted(es.workDir,
		[]string{"fetch", scrubGitCredentials(pushURL), branch},
		"fetch", pushURL, branch,
	)
	if err != nil {
		return fmt.Errorf("fetch external: %w", err)
	}
	if _, err := runGit(es.workDir, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout external branch: %w", err)
	}
	if _, err := runGit(es.workDir, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("reset external: %w", err)
	}
	return nil
}

// pushPlugin re-renders plugins/<pluginName>/ in the local clone via the
// provided render callback, commits, and pushes. On a non-fast-forward
// rejection it refreshes from the remote and retries once. Taking a callback
// (rather than App+Plugin) keeps this layer DB-free and testable.
func (es *externalSync) pushPlugin(ctx context.Context, pluginName string, render func(targetDir string) error) error {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.withRetry(func() error {
		pluginDir := filepath.Join(es.workDir, "plugins", pluginName)
		if err := os.RemoveAll(pluginDir); err != nil {
			return fmt.Errorf("clean external plugin dir: %w", err)
		}
		if err := render(pluginDir); err != nil {
			return fmt.Errorf("render plugin into external: %w", err)
		}
		if err := es.writeRootArtefacts(ctx); err != nil {
			return err
		}
		return es.commitAndPush(fmt.Sprintf("Update plugin %s", pluginName))
	})
}

// importFromRemote fetches the configured branch, identifies plugins whose
// files changed since the last local HEAD, hard-resets the work tree to the
// new tip, and invokes reconcile() for each affected plugin. Taking a
// callback keeps this layer DB-free and testable. Caller must NOT hold the
// mutex — importFromRemote acquires it.
func (es *externalSync) importFromRemote(ctx context.Context, reconcile func(ctx context.Context, pluginName string, author commitAuthor) error) error {
	es.mu.Lock()
	defer es.mu.Unlock()

	localHEAD, err := runGit(es.workDir, "rev-parse", "HEAD")
	if err != nil {
		// Brand-new repo with no commits yet; treat the import as
		// "reconcile everything that arrives".
		localHEAD = ""
	}
	localHEAD = strings.TrimSpace(localHEAD)

	pushURL := es.credentialedURL()
	branch := es.cfg.ExternalGitBranch
	if _, err := es.runGitRedacted(es.workDir,
		[]string{"fetch", scrubGitCredentials(pushURL), branch},
		"fetch", pushURL, branch,
	); err != nil {
		return fmt.Errorf("fetch for import: %w", err)
	}

	fetchHEAD, err := runGit(es.workDir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return fmt.Errorf("rev-parse FETCH_HEAD: %w", err)
	}
	fetchHEAD = strings.TrimSpace(fetchHEAD)

	if localHEAD == fetchHEAD {
		log.Printf("external git import: already up to date at %s", shortSHA(fetchHEAD))
		return nil
	}

	affected, err := es.affectedPluginsSince(localHEAD, fetchHEAD)
	if err != nil {
		return fmt.Errorf("compute affected plugins: %w", err)
	}

	if _, err := runGit(es.workDir, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout FETCH_HEAD: %w", err)
	}
	if _, err := runGit(es.workDir, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("reset to FETCH_HEAD: %w", err)
	}

	if len(affected) == 0 {
		log.Printf("external git import: fast-forward to %s with no plugin-affecting changes", shortSHA(fetchHEAD))
		return nil
	}

	log.Printf("external git import: reconciling %d plugin(s) from %s..%s",
		len(affected), shortSHA(localHEAD), shortSHA(fetchHEAD))
	for pluginName := range affected {
		author, _ := es.latestCommitAuthorForPath(localHEAD, fetchHEAD, "plugins/"+pluginName)
		if err := reconcile(ctx, pluginName, author); err != nil {
			log.Printf("external git import: plugin %q: %v", pluginName, err)
		}
	}
	// Plugin set may have grown or shrunk — refresh marketplace.json so the
	// external repo remains usable as a standalone marketplace. commitAndPush
	// is a no-op when the rewrite produced no diff (steady state after a
	// content-only edit).
	if err := es.writeRootArtefacts(ctx); err != nil {
		log.Printf("external git import: refresh root artefacts: %v", err)
	} else if err := es.commitAndPush("Sync marketplace catalog"); err != nil {
		log.Printf("external git import: push catalog refresh: %v", err)
	}
	return nil
}

// RunExternalImport is the App-level entry point: triggers a webhook-style
// import using the App's own reconcileImportedPlugin. Returns nil quickly
// when external sync is disabled.
func (a *App) RunExternalImport(ctx context.Context) error {
	if a.ExternalSync == nil {
		return nil
	}
	return a.ExternalSync.importFromRemote(ctx, a.reconcileImportedPlugin)
}

// bootstrapFromRemote ignores the local HEAD and reconciles every plugin
// currently in the external tree, regardless of whether it has changed
// since the last sync. Used by the admin sync-in endpoint to populate an
// empty (or partially-populated) DB from an already-populated external
// repo. Returns the names of plugins that reconciled cleanly.
func (es *externalSync) bootstrapFromRemote(ctx context.Context, reconcile func(ctx context.Context, pluginName string, author commitAuthor) error) ([]string, error) {
	es.mu.Lock()
	defer es.mu.Unlock()

	pushURL := es.credentialedURL()
	branch := es.cfg.ExternalGitBranch
	if _, err := es.runGitRedacted(es.workDir,
		[]string{"fetch", scrubGitCredentials(pushURL), branch},
		"fetch", pushURL, branch,
	); err != nil {
		return nil, fmt.Errorf("fetch for bootstrap: %w", err)
	}
	fetchHEAD, err := runGit(es.workDir, "rev-parse", "FETCH_HEAD")
	if err != nil {
		return nil, fmt.Errorf("rev-parse FETCH_HEAD: %w", err)
	}
	fetchHEAD = strings.TrimSpace(fetchHEAD)

	if _, err := runGit(es.workDir, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
		return nil, fmt.Errorf("checkout FETCH_HEAD: %w", err)
	}
	if _, err := runGit(es.workDir, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return nil, fmt.Errorf("reset to FETCH_HEAD: %w", err)
	}

	affected, err := es.affectedPluginsSince("", fetchHEAD)
	if err != nil {
		return nil, fmt.Errorf("list plugins in tree: %w", err)
	}

	names := make([]string, 0, len(affected))
	for name := range affected {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]string, 0, len(names))
	for _, name := range names {
		author, _ := es.latestCommitAuthorForPath("", fetchHEAD, "plugins/"+name)
		if err := reconcile(ctx, name, author); err != nil {
			log.Printf("external git bootstrap: plugin %q: %v", name, err)
			continue
		}
		out = append(out, name)
	}
	if err := es.writeRootArtefacts(ctx); err != nil {
		log.Printf("external git bootstrap: refresh root artefacts: %v", err)
	} else if err := es.commitAndPush("Sync marketplace catalog"); err != nil {
		log.Printf("external git bootstrap: push catalog refresh: %v", err)
	}
	return out, nil
}

// RunExternalBootstrap is the App-level entry point for the admin sync-in
// endpoint. Returns (nil, nil) when external sync is disabled.
func (a *App) RunExternalBootstrap(ctx context.Context) ([]string, error) {
	if a.ExternalSync == nil {
		return nil, nil
	}
	return a.ExternalSync.bootstrapFromRemote(ctx, a.reconcileImportedPlugin)
}

// affectedPluginsSince returns the set of plugin names (the segment after
// "plugins/") that appear in `git diff --name-only oldSHA..newSHA`. When
// oldSHA is empty (no local history yet) every plugin in newSHA is returned.
func (es *externalSync) affectedPluginsSince(oldSHA, newSHA string) (map[string]struct{}, error) {
	var args []string
	if oldSHA == "" {
		// First-time sync: list everything currently in newSHA under plugins/.
		args = []string{"ls-tree", "-r", "--name-only", newSHA, "plugins/"}
	} else {
		args = []string{"diff", "--name-only", oldSHA, newSHA}
	}
	out, err := runGit(es.workDir, args...)
	if err != nil {
		return nil, err
	}
	plugins := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "plugins/") {
			continue
		}
		rest := strings.TrimPrefix(line, "plugins/")
		parts := strings.SplitN(rest, "/", 2)
		if parts[0] == "" {
			continue
		}
		plugins[parts[0]] = struct{}{}
	}
	return plugins, nil
}

// commitAuthor captures the bare minimum we need to attribute imported
// changes to a user: name and email from `git log --format`.
type commitAuthor struct {
	Name  string
	Email string
}

// latestCommitAuthorForPath returns the name/email of the most recent commit
// in oldSHA..newSHA that touched any file under the given path prefix. When
// oldSHA is empty, walks the entire history of newSHA for that path. Empty
// strings on error — callers fall back to the system user.
func (es *externalSync) latestCommitAuthorForPath(oldSHA, newSHA, pathPrefix string) (commitAuthor, error) {
	rng := newSHA
	if oldSHA != "" {
		rng = oldSHA + ".." + newSHA
	}
	args := []string{"log", "-1", "--format=%an%n%ae", rng, "--", pathPrefix}
	out, err := runGit(es.workDir, args...)
	if err != nil {
		return commitAuthor{}, err
	}
	lines := strings.SplitN(strings.TrimSpace(out), "\n", 2)
	if len(lines) < 2 {
		return commitAuthor{}, nil
	}
	return commitAuthor{Name: lines[0], Email: lines[1]}, nil
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// deletePlugin removes plugins/<name>/ from the local clone, commits, and
// pushes. No-op if the directory is already absent AND no root artefact
// needs refreshing.
func (es *externalSync) deletePlugin(ctx context.Context, pluginName string) error {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.withRetry(func() error {
		pluginDir := filepath.Join(es.workDir, "plugins", pluginName)
		if _, err := os.Stat(pluginDir); errors.Is(err, os.ErrNotExist) {
			// Directory already gone, but marketplace.json may still list
			// the plugin — re-render and commit if needed.
			if err := es.writeRootArtefacts(ctx); err != nil {
				return err
			}
			return es.commitAndPush(fmt.Sprintf("Remove plugin %s", pluginName))
		}
		if err := os.RemoveAll(pluginDir); err != nil {
			return fmt.Errorf("remove external plugin dir: %w", err)
		}
		if err := es.writeRootArtefacts(ctx); err != nil {
			return err
		}
		return es.commitAndPush(fmt.Sprintf("Remove plugin %s", pluginName))
	})
}

// writeRootArtefacts writes the root README and (when registered) lets the
// App render `.claude-plugin/marketplace.json` from current DB state. Errors
// from rootWriter are returned to the caller so a failed re-render aborts
// the push rather than committing a stale catalog.
func (es *externalSync) writeRootArtefacts(ctx context.Context) error {
	if err := es.writeRootReadme(); err != nil {
		return err
	}
	if es.rootWriter == nil {
		return nil
	}
	return es.rootWriter(ctx)
}

// withRetry runs op, and if the resulting error looks like a push rejection
// (remote moved), refreshes from origin and runs op once more. All other
// errors are returned immediately.
func (es *externalSync) withRetry(op func() error) error {
	err := op()
	if err == nil || !isPushRejection(err) {
		return err
	}
	log.Printf("external git: push rejected, refreshing and retrying once: %v", err)
	if rErr := es.refreshFromRemote(); rErr != nil {
		return fmt.Errorf("refresh after push rejection: %w (original: %v)", rErr, err)
	}
	return op()
}

func (es *externalSync) commitAndPush(message string) error {
	if _, err := runGit(es.workDir, "add", "-A"); err != nil {
		return err
	}
	out, err := runGit(es.workDir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if _, err := es.runGitAsConfigured(es.workDir, nil, "commit", "-m", message); err != nil {
		return err
	}
	return es.pushCurrentBranch()
}

func (es *externalSync) pushCurrentBranch() error {
	pushURL := es.credentialedURL()
	branch := es.cfg.ExternalGitBranch
	refspec := "HEAD:refs/heads/" + branch
	_, err := es.runGitRedacted(es.workDir,
		[]string{"push", scrubGitCredentials(pushURL), refspec},
		"push", pushURL, refspec,
	)
	return err
}

// credentialedURL embeds the configured token as HTTP Basic Auth password in
// the remote URL. For non-HTTP remotes (git@host:..., ssh://) the URL is
// returned unchanged — auth is expected via ssh keys / agent.
func (es *externalSync) credentialedURL() string {
	remote := es.cfg.ExternalGitRemoteURL
	if es.cfg.ExternalGitToken == "" {
		return remote
	}
	u, err := url.Parse(remote)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return remote
	}
	username := es.cfg.ExternalGitUsername
	if username == "" {
		username = "x-access-token"
	}
	u.User = url.UserPassword(username, es.cfg.ExternalGitToken)
	return u.String()
}

func (es *externalSync) runGitAsConfigured(dir string, redactedArgs []string, args ...string) (string, error) {
	return runGitAs(dir, es.cfg.ExternalGitAuthorName, es.cfg.ExternalGitAuthorEmail, redactedArgs, args...)
}

// runGitRedacted is like runGitAsConfigured but always treats args as
// containing credentials — caller must provide the redacted equivalent for
// the error message.
func (es *externalSync) runGitRedacted(dir string, redactedArgs []string, args ...string) (string, error) {
	return runGitAs(dir, es.cfg.ExternalGitAuthorName, es.cfg.ExternalGitAuthorEmail, redactedArgs, args...)
}

func (es *externalSync) writeRootReadme() error {
	return os.WriteFile(filepath.Join(es.workDir, "README.md"), []byte(externalSyncReadmePreamble), 0o644)
}

// isPushRejection looks at the error message for the markers git uses when a
// non-fast-forward push is rejected. Best-effort: false negatives mean we
// just don't retry, which is safe.
func isPushRejection(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "rejected") ||
		strings.Contains(msg, "non-fast-forward") ||
		strings.Contains(msg, "fetch first")
}

func isHTTPRemote(remote string) bool {
	return strings.HasPrefix(remote, "http://") || strings.HasPrefix(remote, "https://")
}

// InitExternalSync constructs and initialises the external git mirror if
// configured. Safe to call when the feature is disabled — it returns nil
// after a one-line "disabled" log entry.
func (a *App) InitExternalSync(ctx context.Context) error {
	es := newExternalSync(a.Cfg, a.Cfg.DataDir)
	if es == nil {
		log.Printf("external git sync: disabled (EXTERNAL_GIT_REMOTE_URL not set)")
		return nil
	}
	if err := es.initialize(ctx); err != nil {
		err = fmt.Errorf("initialise external git sync: %w", err)
		if a.Cfg.ExternalGitRequired {
			return err
		}
		log.Printf("WARN: %v — continuing with sync disabled for this process", err)
		return nil
	}
	es.rootWriter = func(ctx context.Context) error {
		return a.renderExternalMarketplaceJSON(ctx, es.workDir)
	}
	a.ExternalSync = es
	log.Printf("external git sync: enabled, remote=%s branch=%s",
		scrubGitCredentials(a.Cfg.ExternalGitRemoteURL), a.Cfg.ExternalGitBranch)
	return nil
}

// contextSkipExternalPushKey gates re-pushing during a materialize that was
// triggered BY an external import. Without it, importFromRemote would push
// the just-imported state straight back to the remote (no diff, so harmless,
// but it'd needlessly contend on the sync mutex).
type contextSkipExternalPushKey struct{}

func withSkipExternalPush(ctx context.Context) context.Context {
	return context.WithValue(ctx, contextSkipExternalPushKey{}, true)
}

func shouldSkipExternalPush(ctx context.Context) bool {
	v, _ := ctx.Value(contextSkipExternalPushKey{}).(bool)
	return v
}

// App-level entry points: thin wrappers that no-op when external sync is
// disabled. Materialize and removeRepo call these so the sync gate lives in
// one place.

func (a *App) syncExternalPushPlugin(ctx context.Context, p *Plugin) error {
	if a.ExternalSync == nil || shouldSkipExternalPush(ctx) {
		return nil
	}
	render := func(targetDir string) error {
		return a.renderPluginInto(ctx, p, targetDir)
	}
	if err := a.ExternalSync.pushPlugin(ctx, p.Name, render); err != nil {
		err = fmt.Errorf("external git sync %q: %w", p.Name, err)
		if a.Cfg.ExternalGitRequired {
			return err
		}
		log.Printf("WARN: %v", err)
	}
	return nil
}

func (a *App) syncExternalDeletePlugin(ctx context.Context, pluginName string) error {
	if a.ExternalSync == nil || shouldSkipExternalPush(ctx) {
		return nil
	}
	if err := a.ExternalSync.deletePlugin(ctx, pluginName); err != nil {
		err = fmt.Errorf("external git delete %q: %w", pluginName, err)
		if a.Cfg.ExternalGitRequired {
			return err
		}
		log.Printf("WARN: %v", err)
	}
	return nil
}
