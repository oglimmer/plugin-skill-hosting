package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"marketplace/internal/config"
)

// externalSync mirrors the marketplace contents to a single external git
// repository (GitHub, GitLab, …) one-way: every plugin write or delete
// re-renders the affected plugins/<name>/ subtree in a checked-out clone of
// the remote, commits, and pushes. Disabled when cfg.ExternalGitRemoteURL is
// empty (App.ExternalSync is then nil).
//
// All operations are serialised behind mu so concurrent pushes never
// interleave on the working tree.
type externalSync struct {
	mu      sync.Mutex
	cfg     config.Config
	workDir string

	// rootWriter, when non-nil, is invoked on every push / delete to
	// re-render repo-root artefacts that depend on global state — currently
	// the `.claude-plugin/marketplace.json` snapshot. App sets it during
	// InitExternalSync so the external_sync.go layer stays DB-free.
	rootWriter func(ctx context.Context) error
}

const externalSyncReadmePreamble = "# Marketplace mirror\n\n" +
	"This repository is kept in sync with a self-hosted Claude Code plugin\n" +
	"marketplace. Each subdirectory under `plugins/` contains a materialised\n" +
	"plugin: `.claude-plugin/plugin.json`, `skills/<name>/SKILL.md` and optional\n" +
	"supporting files under `scripts/`, `references/`, or `assets/`.\n\n" +
	"The marketplace pushes here on every create / update / delete. Edits made\n" +
	"directly in this repo are NOT pulled back into the marketplace and will\n" +
	"be overwritten on the next outbound sync.\n\n" +
	"This README is regenerated on every sync; edits to it will be overwritten.\n"

// newExternalSync constructs the syncer if the feature is enabled in cfg.
// Returns nil when disabled.
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
// initial commit.
func (es *externalSync) initialize(ctx context.Context) error {
	es.mu.Lock()
	defer es.mu.Unlock()

	if es.cfg.ExternalGitToken == "" && isHTTPRemote(es.cfg.ExternalGitRemoteURL) {
		log.Printf("WARN: external git sync is enabled for %s but EXTERNAL_GIT_TOKEN is empty — push will likely fail",
			scrubGitCredentials(es.cfg.ExternalGitRemoteURL))
	}

	if _, err := os.Stat(filepath.Join(es.workDir, ".git")); err == nil {
		return es.refreshFromRemote(ctx)
	}
	if err := os.MkdirAll(filepath.Dir(es.workDir), 0o755); err != nil {
		return fmt.Errorf("create external workdir parent: %w", err)
	}

	if err := es.cloneFromRemote(ctx); err == nil {
		log.Printf("external git: cloned %s into %s", scrubGitCredentials(es.cfg.ExternalGitRemoteURL), es.workDir)
		return nil
	} else {
		log.Printf("external git: clone failed (%v); initialising empty repo", err)
	}
	return es.initEmptyRepo(ctx)
}

func (es *externalSync) cloneFromRemote(ctx context.Context) error {
	pushURL := es.credentialedURL()
	if _, err := os.Stat(es.workDir); err == nil {
		if err := os.RemoveAll(es.workDir); err != nil {
			return fmt.Errorf("remove stale workdir: %w", err)
		}
	}
	_, err := runGitRedacted(ctx, "",
		[]string{"clone", "--branch", es.cfg.ExternalGitBranch, scrubGitCredentials(pushURL), es.workDir},
		"clone", "--branch", es.cfg.ExternalGitBranch, pushURL, es.workDir,
	)
	if err != nil {
		return err
	}
	if _, err := runGit(ctx, es.workDir, "remote", "set-url", "origin", es.cfg.ExternalGitRemoteURL); err != nil {
		return fmt.Errorf("set-url origin: %w", err)
	}
	return nil
}

func (es *externalSync) initEmptyRepo(ctx context.Context) error {
	if err := os.MkdirAll(es.workDir, 0o755); err != nil {
		return err
	}
	if _, err := runGit(ctx, es.workDir, "init", "-b", es.cfg.ExternalGitBranch); err != nil {
		return fmt.Errorf("init external repo: %w", err)
	}
	if _, err := runGit(ctx, es.workDir, "remote", "add", "origin", es.cfg.ExternalGitRemoteURL); err != nil {
		return fmt.Errorf("remote add origin: %w", err)
	}
	if err := es.writeRootReadme(); err != nil {
		return err
	}
	if _, err := runGit(ctx, es.workDir, "add", "-A"); err != nil {
		return err
	}
	if _, err := runGit(ctx, es.workDir, "commit", "-m", "Initial commit"); err != nil {
		return fmt.Errorf("initial commit: %w", err)
	}
	return es.pushCurrentBranch(ctx)
}

// refreshFromRemote fetches the configured branch and hard-resets the local
// HEAD to it. Any uncommitted local state is discarded — DB is the source of
// truth, so we re-render from there.
func (es *externalSync) refreshFromRemote(ctx context.Context) error {
	pushURL := es.credentialedURL()
	branch := es.cfg.ExternalGitBranch
	if _, err := runGitRedacted(ctx, es.workDir,
		[]string{"fetch", scrubGitCredentials(pushURL), branch},
		"fetch", pushURL, branch,
	); err != nil {
		return fmt.Errorf("fetch external: %w", err)
	}
	if _, err := runGit(ctx, es.workDir, "checkout", "-B", branch, "FETCH_HEAD"); err != nil {
		return fmt.Errorf("checkout external branch: %w", err)
	}
	if _, err := runGit(ctx, es.workDir, "reset", "--hard", "FETCH_HEAD"); err != nil {
		return fmt.Errorf("reset external: %w", err)
	}
	return nil
}

// pushPlugin re-renders plugins/<pluginName>/ in the local clone via the
// provided render callback, commits, and pushes. On a non-fast-forward
// rejection it refreshes from the remote and retries once.
//
// render gets the emptied target dir and prevDir, the plugin's tree as the
// mirror last had it (an empty dir for a plugin that is new to the mirror), so
// it can carry audit-pending items over unchanged.
func (es *externalSync) pushPlugin(ctx context.Context, pluginName string, render func(targetDir, prevDir string) error) error {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.withRetry(ctx, func() error {
		pluginDir := filepath.Join(es.workDir, "plugins", pluginName)
		// Move the current tree out of the clone instead of deleting it, so
		// render can read the last published files from it.
		prevDir, err := os.MkdirTemp("", "ext-sync-prev-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(prevDir)
		if _, err := copyPublished(pluginDir, prevDir); err != nil {
			return fmt.Errorf("snapshot external plugin dir: %w", err)
		}
		if err := os.RemoveAll(pluginDir); err != nil {
			return fmt.Errorf("clean external plugin dir: %w", err)
		}
		if err := render(pluginDir, prevDir); err != nil {
			return fmt.Errorf("render plugin into external: %w", err)
		}
		if err := es.writeRootArtefacts(ctx); err != nil {
			return err
		}
		return es.commitAndPush(ctx, fmt.Sprintf("Update plugin %s", pluginName))
	})
}

// checkPlugins reports how the external mirror diverges from the expected set
// of plugins, without mutating the remote. It refreshes the local clone from
// origin, then for each expected plugin renders it into a temp dir (via the
// caller's DB-backed render) and byte-compares the result against the mirror's
// plugins/<name>/ subtree:
//
//   - missing:   expected plugin has no plugins/<name>/ dir in the mirror
//   - outOfDate: the dir exists but its contents differ from a fresh render
//   - extra:     a plugins/<name>/ dir exists with no matching expected plugin
//
// The comparison is exact because both the mirror and the temp render are
// produced by the same deterministic renderer, so an in-sync plugin hashes
// identically. render(name, dir) must materialise plugin `name` into `dir`.
//
// render also gets prevDir — the mirror's current tree for that plugin — so an
// audit-pending item, which the mirror correctly still holds in its last
// published form, compares equal instead of showing up as drift.
func (es *externalSync) checkPlugins(ctx context.Context, names []string, render func(name, dir, prevDir string) error) (missing, outOfDate, extra []string, err error) {
	es.mu.Lock()
	defer es.mu.Unlock()

	if err = es.refreshFromRemote(ctx); err != nil {
		return nil, nil, nil, err
	}

	expected := make(map[string]bool, len(names))
	for _, n := range names {
		expected[n] = true
	}

	for _, name := range names {
		extPath := filepath.Join(es.workDir, "plugins", name)
		info, statErr := os.Stat(extPath)
		if errors.Is(statErr, os.ErrNotExist) || (statErr == nil && !info.IsDir()) {
			missing = append(missing, name)
			continue
		}
		if statErr != nil {
			return nil, nil, nil, statErr
		}
		same, cmpErr := es.renderedMatches(extPath, func(dir string) error {
			return render(name, dir, extPath)
		})
		if cmpErr != nil {
			return nil, nil, nil, cmpErr
		}
		if !same {
			outOfDate = append(outOfDate, name)
		}
	}

	dirs, err := es.pluginDirNames()
	if err != nil {
		return nil, nil, nil, err
	}
	for _, d := range dirs {
		if !expected[d] {
			extra = append(extra, d)
		}
	}
	return missing, outOfDate, extra, nil
}

// pluginDirNames lists the immediate subdirectories of plugins/ in the local
// clone. Returns nil (not an error) when the plugins/ dir doesn't exist yet.
func (es *externalSync) pluginDirNames() ([]string, error) {
	root := filepath.Join(es.workDir, "plugins")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

// renderedMatches renders into a throwaway temp dir and reports whether the
// result is byte-for-byte identical to the tree already at extPath.
func (es *externalSync) renderedMatches(extPath string, render func(dir string) error) (bool, error) {
	tmp, err := os.MkdirTemp("", "ext-sync-check-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(tmp)
	if err := render(tmp); err != nil {
		return false, fmt.Errorf("render for compare: %w", err)
	}
	return dirsEqual(tmp, extPath)
}

// dirsEqual reports whether two directory trees contain the same set of files
// with identical contents. Directory entries and file modes are ignored; only
// the relative path set and per-file bytes matter.
func dirsEqual(a, b string) (bool, error) {
	ha, err := hashTree(a)
	if err != nil {
		return false, err
	}
	hb, err := hashTree(b)
	if err != nil {
		return false, err
	}
	if len(ha) != len(hb) {
		return false, nil
	}
	for path, sum := range ha {
		if hb[path] != sum {
			return false, nil
		}
	}
	return true, nil
}

// hashTree walks root and returns a map of each regular file's slash-relative
// path to the hex SHA-256 of its contents. A missing root yields an empty map.
func hashTree(root string) (map[string]string, error) {
	out := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if errors.Is(walkErr, fs.ErrNotExist) && p == root {
				return nil
			}
			return walkErr
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(b)
		out[filepath.ToSlash(rel)] = hex.EncodeToString(sum[:])
		return nil
	})
	return out, err
}

// deletePlugin removes plugins/<name>/ from the local clone, commits, and
// pushes. No-op if the directory is already absent and root artefacts are
// already in sync.
func (es *externalSync) deletePlugin(ctx context.Context, pluginName string) error {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.withRetry(ctx, func() error {
		pluginDir := filepath.Join(es.workDir, "plugins", pluginName)
		if _, err := os.Stat(pluginDir); errors.Is(err, os.ErrNotExist) {
			if err := es.writeRootArtefacts(ctx); err != nil {
				return err
			}
			return es.commitAndPush(ctx, fmt.Sprintf("Remove plugin %s", pluginName))
		}
		if err := os.RemoveAll(pluginDir); err != nil {
			return fmt.Errorf("remove external plugin dir: %w", err)
		}
		if err := es.writeRootArtefacts(ctx); err != nil {
			return err
		}
		return es.commitAndPush(ctx, fmt.Sprintf("Remove plugin %s", pluginName))
	})
}

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
func (es *externalSync) withRetry(ctx context.Context, op func() error) error {
	err := op()
	if err == nil || !isPushRejection(err) {
		return err
	}
	log.Printf("external git: push rejected, refreshing and retrying once: %v", err)
	if rErr := es.refreshFromRemote(ctx); rErr != nil {
		return fmt.Errorf("refresh after push rejection: %w (original: %v)", rErr, err)
	}
	return op()
}

func (es *externalSync) commitAndPush(ctx context.Context, message string) error {
	if _, err := runGit(ctx, es.workDir, "add", "-A"); err != nil {
		return err
	}
	out, err := runGit(ctx, es.workDir, "status", "--porcelain")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) == "" {
		return nil
	}
	if _, err := runGit(ctx, es.workDir, "commit", "-m", message); err != nil {
		return err
	}
	return es.pushCurrentBranch(ctx)
}

func (es *externalSync) pushCurrentBranch(ctx context.Context) error {
	pushURL := es.credentialedURL()
	branch := es.cfg.ExternalGitBranch
	refspec := "HEAD:refs/heads/" + branch
	_, err := runGitRedacted(ctx, es.workDir,
		[]string{"push", scrubGitCredentials(pushURL), refspec},
		"push", pushURL, refspec,
	)
	return err
}

// defaultTokenUsername picks the HTTP Basic Auth username that goes with a
// token when EXTERNAL_GIT_USERNAME is unset: "oauth2" for GitLab (works for
// personal, project and group access tokens), "x-access-token" otherwise
// (GitHub PAT / app-token convention).
func defaultTokenUsername(host, providerOverride string) string {
	if resolveProvider(host, providerOverride) == "gitlab" {
		return "oauth2"
	}
	return "x-access-token"
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
		username = defaultTokenUsername(u.Hostname(), es.cfg.ExternalGitProvider)
	}
	u.User = url.UserPassword(username, es.cfg.ExternalGitToken)
	return u.String()
}

func (es *externalSync) writeRootReadme() error {
	return os.WriteFile(filepath.Join(es.workDir, "README.md"), []byte(externalSyncReadmePreamble), 0o644)
}

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
// after a one-line "disabled" log entry. Initialisation failures are logged
// but not fatal: the process continues with sync disabled.
func (a *App) InitExternalSync(ctx context.Context) error {
	es := newExternalSync(a.Cfg, a.Cfg.DataDir)
	if es == nil {
		log.Printf("external git sync: disabled (EXTERNAL_GIT_REMOTE_URL not set)")
		return nil
	}
	if err := es.initialize(ctx); err != nil {
		log.Printf("WARN: initialise external git sync: %v — continuing with sync disabled for this process", err)
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

// syncExternalPushPlugin pushes a plugin's rendered tree to the external
// remote. No-op when the feature is disabled. Failures are logged but never
// propagated — the DB is the source of truth, so the internal write stands.
func (a *App) syncExternalPushPlugin(ctx context.Context, p *Plugin) error {
	if a.ExternalSync == nil {
		return nil
	}
	render := func(targetDir, prevDir string) error {
		return a.renderPluginInto(ctx, p, targetDir, prevDir)
	}
	if err := a.ExternalSync.pushPlugin(ctx, p.Name, render); err != nil {
		log.Printf("WARN: external git sync %q: %v", p.Name, err)
	}
	return nil
}

func (a *App) syncExternalDeletePlugin(ctx context.Context, pluginName string) error {
	if a.ExternalSync == nil {
		return nil
	}
	if err := a.ExternalSync.deletePlugin(ctx, pluginName); err != nil {
		log.Printf("WARN: external git delete %q: %v", pluginName, err)
	}
	return nil
}
