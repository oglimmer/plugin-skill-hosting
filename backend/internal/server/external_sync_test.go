package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"marketplace/internal/config"
)

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

func TestNewExternalSync_DisabledWhenEmpty(t *testing.T) {
	if got := newExternalSync(config.Config{}, "/tmp/data"); got != nil {
		t.Errorf("newExternalSync with empty remote should return nil, got %+v", got)
	}
}

func TestNewExternalSync_EnabledWhenRemoteSet(t *testing.T) {
	got := newExternalSync(config.Config{
		ExternalGitRemoteURL: "https://github.com/acme/marketplace.git",
	}, "/tmp/data")
	if got == nil {
		t.Fatal("expected non-nil syncer")
	}
	want := "/tmp/data/external/marketplace"
	if got.workDir != want {
		t.Errorf("workDir = %q, want %q", got.workDir, want)
	}
}

func TestCredentialedURL_HTTPS(t *testing.T) {
	es := &externalSync{cfg: config.Config{
		ExternalGitRemoteURL: "https://github.com/acme/marketplace.git",
		ExternalGitUsername:  "oauth2",
		ExternalGitToken:     "secret-token",
	}}
	got := es.credentialedURL()
	want := "https://oauth2:secret-token@github.com/acme/marketplace.git"
	if got != want {
		t.Errorf("credentialedURL = %q, want %q", got, want)
	}
}

func TestCredentialedURL_DefaultsToXAccessToken(t *testing.T) {
	es := &externalSync{cfg: config.Config{
		ExternalGitRemoteURL: "https://github.com/acme/marketplace.git",
		ExternalGitToken:     "secret",
	}}
	if got := es.credentialedURL(); !strings.HasPrefix(got, "https://x-access-token:secret@") {
		t.Errorf("expected x-access-token default username; got %q", got)
	}
}

func TestCredentialedURL_NoTokenLeavesURLUnchanged(t *testing.T) {
	remote := "https://github.com/acme/marketplace.git"
	es := &externalSync{cfg: config.Config{ExternalGitRemoteURL: remote}}
	if got := es.credentialedURL(); got != remote {
		t.Errorf("credentialedURL with no token = %q, want %q", got, remote)
	}
}

func TestCredentialedURL_SSHRemoteUnchanged(t *testing.T) {
	remote := "git@github.com:acme/marketplace.git"
	es := &externalSync{cfg: config.Config{
		ExternalGitRemoteURL: remote,
		ExternalGitToken:     "secret-token",
	}}
	if got := es.credentialedURL(); got != remote {
		t.Errorf("ssh remote should not get token injection; got %q", got)
	}
}

func TestScrubGitCredentials_RedactsUserInfo(t *testing.T) {
	in := "fatal: could not push https://oauth2:secret-token@gitlab.com/acme/marketplace.git"
	got := scrubGitCredentials(in)
	if strings.Contains(got, "secret-token") || strings.Contains(got, "oauth2") {
		t.Errorf("credentials not redacted: %q", got)
	}
	if !strings.Contains(got, "https://REDACTED@gitlab.com/acme/marketplace.git") {
		t.Errorf("scrubbed output lost remote context: %q", got)
	}
}

func TestScrubGitCredentials_LeavesUncredentialedURLAlone(t *testing.T) {
	in := "fatal: not a git repository: https://gitlab.com/acme/marketplace.git"
	if got := scrubGitCredentials(in); got != in {
		t.Errorf("uncredentialed URL was modified: %q", got)
	}
}

func TestIsPushRejection_Markers(t *testing.T) {
	cases := []struct {
		msg  string
		want bool
	}{
		{"! [rejected] main -> main (non-fast-forward)", true},
		{"hint: Updates were rejected because the tip of your current branch is behind", true},
		{"Please pull and retry: fetch first", true},
		{"fatal: unable to access", false},
		{"", false},
	}
	for _, c := range cases {
		got := isPushRejection(&simpleErr{c.msg})
		if got != c.want {
			t.Errorf("isPushRejection(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}

type simpleErr struct{ s string }

func (e *simpleErr) Error() string { return e.s }

// TestExternalSync_PushAndDelete_EndToEnd exercises the full push/delete
// cycle against a local bare repo standing in for GitHub/GitLab. Requires
// the git binary in PATH, which is already the case for the rest of the
// server package (materialize uses it too).
func TestExternalSync_PushAndDelete_EndToEnd(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	if _, err := runGit(context.Background(), "", "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	dataDir := filepath.Join(root, "data")
	cfg := config.Config{
		ExternalGitRemoteURL: bare,
		ExternalGitBranch:    "main",
		ExternalGitUsername:  "x-access-token",
		ExternalGitToken:     "", // local file:// remote needs no auth
	}
	es := newExternalSync(cfg, dataDir)
	if es == nil {
		t.Fatal("syncer should be enabled")
	}

	ctx := context.Background()
	if err := es.initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// Push two plugins.
	for _, name := range []string{"alpha", "beta"} {
		render := func(targetDir string) error {
			if err := os.MkdirAll(targetDir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(targetDir, "README.md"),
				[]byte("# "+name+"\n"), 0o644)
		}
		if err := es.pushPlugin(ctx, name, render); err != nil {
			t.Fatalf("pushPlugin %s: %v", name, err)
		}
	}

	// Clone the bare repo into a separate verification dir and verify both
	// plugin subdirs are present at HEAD.
	verifyDir := filepath.Join(root, "verify")
	if _, err := runGit(ctx, "", "clone", bare, verifyDir); err != nil {
		t.Fatalf("verify clone: %v", err)
	}
	for _, name := range []string{"alpha", "beta"} {
		path := filepath.Join(verifyDir, "plugins", name, "README.md")
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected %s, got err %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(verifyDir, "README.md")); err != nil {
		t.Errorf("root README missing: %v", err)
	}

	// Delete alpha.
	if err := es.deletePlugin(ctx, "alpha"); err != nil {
		t.Fatalf("deletePlugin: %v", err)
	}
	if _, err := runGit(ctx, verifyDir, "pull", "--rebase"); err != nil {
		t.Fatalf("verify pull: %v", err)
	}
	if _, err := os.Stat(filepath.Join(verifyDir, "plugins", "alpha")); !os.IsNotExist(err) {
		t.Errorf("expected alpha to be deleted, got err %v", err)
	}
	if _, err := os.Stat(filepath.Join(verifyDir, "plugins", "beta", "README.md")); err != nil {
		t.Errorf("expected beta to survive delete of alpha: %v", err)
	}

	// Delete a plugin that doesn't exist — should be a no-op, not an error.
	if err := es.deletePlugin(ctx, "never-existed"); err != nil {
		t.Errorf("delete of missing plugin should be no-op, got: %v", err)
	}
}

// TestExternalSync_InitializeOnExistingClone confirms initialize fast-paths
// to fetch+reset when .git already exists (e.g. pod restart with PVC).
func TestExternalSync_InitializeOnExistingClone(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	ctx := context.Background()
	bare := filepath.Join(root, "remote.git")
	if _, err := runGit(ctx, "", "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	// Seed remote with an initial commit so cloning succeeds.
	seed := filepath.Join(root, "seed")
	if _, err := runGit(ctx, "", "init", "-b", "main", seed); err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "hello"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := runGit(ctx, seed, "add", "-A"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	if _, err := runGit(ctx, seed, "commit", "-m", "seed"); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if _, err := runGit(ctx, seed, "remote", "add", "origin", bare); err != nil {
		t.Fatalf("seed remote: %v", err)
	}
	if _, err := runGit(ctx, seed, "push", "origin", "main"); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	dataDir := filepath.Join(root, "data")
	cfg := config.Config{
		ExternalGitRemoteURL: bare,
		ExternalGitBranch:    "main",
	}
	es := newExternalSync(cfg, dataDir)
	if err := es.initialize(context.Background()); err != nil {
		t.Fatalf("first init: %v", err)
	}

	// Second initialize on the same workDir — should not re-clone and not error.
	if err := es.initialize(context.Background()); err != nil {
		t.Fatalf("second init: %v", err)
	}
	if _, err := os.Stat(filepath.Join(es.workDir, "hello")); err != nil {
		t.Errorf("expected seeded file to survive re-initialize: %v", err)
	}
}
