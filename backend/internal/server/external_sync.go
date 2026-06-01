package server

import (
	"context"
	"errors"
	"fmt"
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
func (es *externalSync) pushPlugin(ctx context.Context, pluginName string, render func(targetDir string) error) error {
	es.mu.Lock()
	defer es.mu.Unlock()
	return es.withRetry(ctx, func() error {
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
		return es.commitAndPush(ctx, fmt.Sprintf("Update plugin %s", pluginName))
	})
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
	render := func(targetDir string) error {
		return a.renderPluginInto(ctx, p, targetDir)
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
