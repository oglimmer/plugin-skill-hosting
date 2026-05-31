package server

import (
	"context"
	"os"
	"testing"

	"marketplace/internal/config"
	"marketplace/internal/db"
)

// TestFindOrCreateOIDCUser_EmailVerificationGating is the S8 regression guard:
// an incoming OIDC identity is auto-linked to an existing local account by
// email ONLY when the IdP verified that email. It needs a real Postgres, so it
// runs only when TEST_DATABASE_URL is set (pointed at a disposable database):
//
//	TEST_DATABASE_URL=postgres://marketplace:marketplace@localhost:5432/marketplace?sslmode=disable \
//	  go test ./internal/server/ -run EmailVerificationGating
func TestFindOrCreateOIDCUser_EmailVerificationGating(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to run the OIDC DB integration test")
	}
	pool, err := db.Open(dsn)
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer pool.Close()
	if err := db.Migrate(pool); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	ctx := context.Background()
	const issuer = "https://s8-test-idp.example.com"
	const email = "s8-victim@example.com"

	// Clean leftovers so the test is repeatable.
	if _, err := pool.ExecContext(ctx,
		`DELETE FROM users WHERE email = $1 OR oidc_issuer = $2 OR username = 's8victim'`,
		email, issuer); err != nil {
		t.Fatalf("cleanup: %v", err)
	}

	key := make([]byte, 32) // any 32-byte key; only display depends on it
	app := &App{Cfg: config.Config{APITokenKey: key, AuthMode: "oidc"}, DB: pool}

	// Seed a pre-existing account bound to the victim's identity.
	if _, err := pool.ExecContext(ctx,
		`INSERT INTO users (email, username, oidc_issuer, oidc_subject, api_token_hash, status, is_admin)
		 VALUES ($1, 's8victim', $2, 'victim-sub', $3, 'approved', false)`,
		email, issuer, sha256hex("s8-seed-token")); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	// Attacker: a DIFFERENT subject presenting the victim's email, UNVERIFIED.
	// Must NOT link — it should be rejected and leave the victim's binding intact.
	no := false
	if _, err := app.findOrCreateOIDCUser(ctx, issuer, &oidcClaims{
		Sub: "attacker-sub", Email: email, EmailVerified: &no,
	}); err == nil {
		t.Fatal("unverified email collided with an existing account but was NOT rejected — account-takeover risk")
	}
	var sub string
	if err := pool.QueryRowContext(ctx,
		`SELECT oidc_subject FROM users WHERE email = $1`, email).Scan(&sub); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if sub != "victim-sub" {
		t.Errorf("oidc_subject = %q, want victim-sub (must not relink to the attacker)", sub)
	}

	// A VERIFIED email with a new subject is allowed to link (e.g. the same
	// person on a new IdP registration).
	yes := true
	u, err := app.findOrCreateOIDCUser(ctx, issuer, &oidcClaims{
		Sub: "newdevice-sub", Email: email, EmailVerified: &yes,
	})
	if err != nil {
		t.Fatalf("verified email should link, got error: %v", err)
	}
	if u.Email != email {
		t.Errorf("linked user email = %q, want %q", u.Email, email)
	}
	if err := pool.QueryRowContext(ctx,
		`SELECT oidc_subject FROM users WHERE email = $1`, email).Scan(&sub); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if sub != "newdevice-sub" {
		t.Errorf("verified link did not update oidc_subject: got %q", sub)
	}
}
