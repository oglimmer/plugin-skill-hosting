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
	if _, err := runGit("", "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	dataDir := filepath.Join(root, "data")
	cfg := config.Config{
		ExternalGitRemoteURL:   bare,
		ExternalGitBranch:      "main",
		ExternalGitUsername:    "x-access-token",
		ExternalGitToken:       "", // local file:// remote needs no auth
		ExternalGitAuthorName:  "test",
		ExternalGitAuthorEmail: "test@local",
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
	if _, err := runGit("", "clone", bare, verifyDir); err != nil {
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
	if _, err := runGit(verifyDir, "pull", "--rebase"); err != nil {
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
	bare := filepath.Join(root, "remote.git")
	if _, err := runGit("", "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	// Seed remote with an initial commit so cloning succeeds.
	seed := filepath.Join(root, "seed")
	if _, err := runGit("", "init", "-b", "main", seed); err != nil {
		t.Fatalf("init seed: %v", err)
	}
	if err := os.WriteFile(filepath.Join(seed, "hello"), []byte("hi"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	if _, err := runGit(seed, "add", "-A"); err != nil {
		t.Fatalf("seed add: %v", err)
	}
	if _, err := runGit(seed, "commit", "-m", "seed"); err != nil {
		t.Fatalf("seed commit: %v", err)
	}
	if _, err := runGit(seed, "remote", "add", "origin", bare); err != nil {
		t.Fatalf("seed remote: %v", err)
	}
	if _, err := runGit(seed, "push", "origin", "main"); err != nil {
		t.Fatalf("seed push: %v", err)
	}

	dataDir := filepath.Join(root, "data")
	cfg := config.Config{
		ExternalGitRemoteURL:   bare,
		ExternalGitBranch:      "main",
		ExternalGitAuthorName:  "t",
		ExternalGitAuthorEmail: "t@l",
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

// TestExternalSync_BootstrapFromRemote_ReturnsAllPlugins exercises the
// admin sync-in path: it should reconcile every plugin currently in the
// remote tree, not just deltas, even on a fresh clone where localHEAD
// already equals FETCH_HEAD.
func TestExternalSync_BootstrapFromRemote_ReturnsAllPlugins(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	if _, err := runGit("", "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	// Seed the remote with three plugins via the normal push path.
	seed := newExternalSync(config.Config{
		ExternalGitRemoteURL:   bare,
		ExternalGitBranch:      "main",
		ExternalGitAuthorName:  "marketplace",
		ExternalGitAuthorEmail: "marketplace@local",
	}, filepath.Join(root, "seed"))
	if err := seed.initialize(context.Background()); err != nil {
		t.Fatalf("seed init: %v", err)
	}
	for _, name := range []string{"alpha", "beta", "gamma"} {
		n := name
		if err := seed.pushPlugin(context.Background(), n, func(dir string) error {
			os.MkdirAll(dir, 0o755)
			return os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+n+"\n"), 0o644)
		}); err != nil {
			t.Fatalf("seed push %s: %v", n, err)
		}
	}

	// Fresh syncer pointed at the now-populated remote.
	es := newExternalSync(config.Config{
		ExternalGitRemoteURL:   bare,
		ExternalGitBranch:      "main",
		ExternalGitAuthorName:  "marketplace",
		ExternalGitAuthorEmail: "marketplace@local",
	}, filepath.Join(root, "fresh"))
	if err := es.initialize(context.Background()); err != nil {
		t.Fatalf("fresh init: %v", err)
	}

	// importFromRemote would be a no-op (localHEAD == FETCH_HEAD just after
	// clone). bootstrap should still hand us every plugin.
	noopCalls := 0
	if err := es.importFromRemote(context.Background(), func(context.Context, string, commitAuthor) error {
		noopCalls++
		return nil
	}); err != nil {
		t.Fatalf("importFromRemote: %v", err)
	}
	if noopCalls != 0 {
		t.Errorf("expected importFromRemote to be a no-op on fresh clone; got %d calls", noopCalls)
	}

	var got []string
	names, err := es.bootstrapFromRemote(context.Background(), func(_ context.Context, n string, _ commitAuthor) error {
		got = append(got, n)
		return nil
	})
	if err != nil {
		t.Fatalf("bootstrapFromRemote: %v", err)
	}
	want := []string{"alpha", "beta", "gamma"}
	if !equalStringSlices(got, want) {
		t.Errorf("reconcile callbacks: got %v, want %v", got, want)
	}
	if !equalStringSlices(names, want) {
		t.Errorf("returned names: got %v, want %v", names, want)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func requireGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git binary not available")
	}
}

// TestExternalSync_ImportFromRemote_PicksUpChangedPlugin sets up a remote
// with two plugins, pushes a change to one of them, and verifies the
// reconcile callback receives exactly that plugin (not the unchanged one)
// along with the commit author info.
func TestExternalSync_ImportFromRemote_PicksUpChangedPlugin(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	if _, err := runGit("", "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	dataDir := filepath.Join(root, "data")
	cfg := config.Config{
		ExternalGitRemoteURL:   bare,
		ExternalGitBranch:      "main",
		ExternalGitAuthorName:  "marketplace",
		ExternalGitAuthorEmail: "marketplace@local",
	}
	es := newExternalSync(cfg, dataDir)
	ctx := context.Background()
	if err := es.initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	// Seed two plugins via the normal push path.
	for _, name := range []string{"alpha", "beta"} {
		n := name
		if err := es.pushPlugin(ctx, n, func(dir string) error {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dir, "README.md"), []byte("# "+n+"\n"), 0o644)
		}); err != nil {
			t.Fatalf("push %s: %v", n, err)
		}
	}

	// Independently clone the bare repo, edit alpha, push with a distinct
	// author identity so the import can attribute the change.
	editor := filepath.Join(root, "editor")
	if _, err := runGit("", "clone", bare, editor); err != nil {
		t.Fatalf("editor clone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(editor, "plugins", "alpha", "README.md"),
		[]byte("# alpha (edited externally)\n"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := runGitAs(editor, "Carol Editor", "carol@editor.test", nil,
		"commit", "-am", "External edit of alpha"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := runGit(editor, "push", "origin", "main"); err != nil {
		t.Fatalf("push: %v", err)
	}

	// Now fire importFromRemote on the syncer; capture which plugins are
	// reconciled and what author info was passed.
	type call struct {
		name   string
		author commitAuthor
	}
	var calls []call
	reconcile := func(_ context.Context, pluginName string, author commitAuthor) error {
		calls = append(calls, call{pluginName, author})
		return nil
	}
	if err := es.importFromRemote(ctx, reconcile); err != nil {
		t.Fatalf("importFromRemote: %v", err)
	}

	if len(calls) != 1 {
		t.Fatalf("expected exactly one reconcile call, got %d: %+v", len(calls), calls)
	}
	if calls[0].name != "alpha" {
		t.Errorf("expected alpha, got %q", calls[0].name)
	}
	if calls[0].author.Email != "carol@editor.test" {
		t.Errorf("expected author carol@editor.test, got %q", calls[0].author.Email)
	}
	if calls[0].author.Name != "Carol Editor" {
		t.Errorf("expected author name Carol Editor, got %q", calls[0].author.Name)
	}

	// Calling again with no remote changes is a fast no-op (no reconcile
	// calls beyond the first invocation).
	calls = nil
	if err := es.importFromRemote(ctx, reconcile); err != nil {
		t.Fatalf("second importFromRemote: %v", err)
	}
	if len(calls) != 0 {
		t.Errorf("up-to-date import should be no-op, got %d call(s)", len(calls))
	}
}

// TestExternalSync_ImportFromRemote_HandlesDeletion confirms a plugin
// deletion (subdir removed externally) propagates to the reconcile callback,
// which is then responsible for soft-deleting in DB.
func TestExternalSync_ImportFromRemote_HandlesDeletion(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	if _, err := runGit("", "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}

	dataDir := filepath.Join(root, "data")
	cfg := config.Config{
		ExternalGitRemoteURL:   bare,
		ExternalGitBranch:      "main",
		ExternalGitAuthorName:  "marketplace",
		ExternalGitAuthorEmail: "marketplace@local",
	}
	es := newExternalSync(cfg, dataDir)
	ctx := context.Background()
	if err := es.initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}
	if err := es.pushPlugin(ctx, "gone", func(dir string) error {
		os.MkdirAll(dir, 0o755)
		return os.WriteFile(filepath.Join(dir, "README.md"), []byte("bye\n"), 0o644)
	}); err != nil {
		t.Fatalf("push: %v", err)
	}

	editor := filepath.Join(root, "editor")
	if _, err := runGit("", "clone", bare, editor); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if err := os.RemoveAll(filepath.Join(editor, "plugins", "gone")); err != nil {
		t.Fatalf("rm: %v", err)
	}
	if _, err := runGit(editor, "add", "-A"); err != nil {
		t.Fatalf("add: %v", err)
	}
	if _, err := runGitAs(editor, "Dave", "dave@test", nil, "commit", "-m", "Remove gone"); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := runGit(editor, "push", "origin", "main"); err != nil {
		t.Fatalf("push: %v", err)
	}

	var reconciled []string
	if err := es.importFromRemote(ctx, func(_ context.Context, pluginName string, _ commitAuthor) error {
		reconciled = append(reconciled, pluginName)
		return nil
	}); err != nil {
		t.Fatalf("importFromRemote: %v", err)
	}
	if len(reconciled) != 1 || reconciled[0] != "gone" {
		t.Errorf("expected reconcile call for deleted plugin, got %v", reconciled)
	}
	// Local work tree should reflect the deletion now.
	if _, err := os.Stat(filepath.Join(es.workDir, "plugins", "gone")); !os.IsNotExist(err) {
		t.Errorf("local work tree still has plugins/gone after import: %v", err)
	}
}
