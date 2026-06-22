// Package db owns the connection pool and the embedded migration scripts.
package db

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/0001_init.sql
var migration0001 string

//go:embed migrations/0002_oidc.sql
var migration0002 string

//go:embed migrations/0003_api_token.sql
var migration0003 string

//go:embed migrations/0004_skill_audit.sql
var migration0004 string

//go:embed migrations/0005_plugin_soft_delete.sql
var migration0005 string

//go:embed migrations/0006_skill_files.sql
var migration0006 string

//go:embed migrations/0007_skill_file_versions.sql
var migration0007 string

//go:embed migrations/0008_user_approval.sql
var migration0008 string

//go:embed migrations/0009_skill_extra_frontmatter.sql
var migration0009 string

//go:embed migrations/0010_user_admin.sql
var migration0010 string

//go:embed migrations/0011_oauth.sql
var migration0011 string

//go:embed migrations/0012_skill_audit_results.sql
var migration0012 string

//go:embed migrations/0013_user_soft_delete.sql
var migration0013 string

//go:embed migrations/0014_token_version.sql
var migration0014 string

//go:embed migrations/0015_api_token_encryption.sql
var migration0015 string

//go:embed migrations/0016_user_theme.sql
var migration0016 string

//go:embed migrations/0017_skill_version_move_action.sql
var migration0017 string

//go:embed migrations/0018_skill_lock.sql
var migration0018 string

// Open opens the application's *sql.DB through pgx's database/sql adapter and
// configures it for use behind a transaction-pool PgBouncer (the deployment in
// front of OVH Managed PG, and the common HA layout in general).
//
// Why pgx and not lib/pq: lib/pq splits Parse and Bind across separate
// extended-protocol round trips. PgBouncer in transaction-pool mode is free to
// rebind the underlying server connection between them, so the Bind lands on a
// backend that never saw the Parse and PG returns either
//
//	pq: unnamed prepared statement does not exist (26000)
//
// or
//
//	pq: bind message has N result formats but query has M columns (08P01)
//
// pgx with QueryExecModeExec pipelines Parse+Bind+Describe+Execute+Sync into a
// single message group so the whole exchange completes inside one PgBouncer-
// owned transaction; there is no window where the server can be swapped.
func Open(url string) (*sql.DB, error) {
	cfg, err := pgx.ParseConfig(url)
	if err != nil {
		return nil, fmt.Errorf("parse db url: %w", err)
	}
	// No statement caching and no separate Parse round-trip. Required for
	// PgBouncer transaction pooling; harmless on a direct PG connection.
	cfg.DefaultQueryExecMode = pgx.QueryExecModeExec
	cfg.StatementCacheCapacity = 0
	cfg.DescriptionCacheCapacity = 0

	db := stdlib.OpenDB(*cfg)
	// Cap idle lifetime so PgBouncer / managed-PG proxies don't hand us back a
	// connection whose server-side peer was already reaped, and bound the
	// pool size so a single backend can't exhaust the bouncer's client slots.
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxIdleTime(30 * time.Second)
	db.SetConnMaxLifetime(5 * time.Minute)
	for i := 0; i < 30; i++ {
		if err = db.Ping(); err == nil {
			return db, nil
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("ping db: %w", err)
}

func Migrate(db *sql.DB) error {
	if _, err := db.Exec(migration0001); err != nil {
		return fmt.Errorf("0001_init: %w", err)
	}
	if _, err := db.Exec(migration0002); err != nil {
		return fmt.Errorf("0002_oidc: %w", err)
	}
	if _, err := db.Exec(migration0003); err != nil {
		return fmt.Errorf("0003_api_token: %w", err)
	}
	if _, err := db.Exec(migration0004); err != nil {
		return fmt.Errorf("0004_skill_audit: %w", err)
	}
	if _, err := db.Exec(migration0005); err != nil {
		return fmt.Errorf("0005_plugin_soft_delete: %w", err)
	}
	if _, err := db.Exec(migration0006); err != nil {
		return fmt.Errorf("0006_skill_files: %w", err)
	}
	if _, err := db.Exec(migration0007); err != nil {
		return fmt.Errorf("0007_skill_file_versions: %w", err)
	}
	if _, err := db.Exec(migration0008); err != nil {
		return fmt.Errorf("0008_user_approval: %w", err)
	}
	if _, err := db.Exec(migration0009); err != nil {
		return fmt.Errorf("0009_skill_extra_frontmatter: %w", err)
	}
	if _, err := db.Exec(migration0010); err != nil {
		return fmt.Errorf("0010_user_admin: %w", err)
	}
	if _, err := db.Exec(migration0011); err != nil {
		return fmt.Errorf("0011_oauth: %w", err)
	}
	if _, err := db.Exec(migration0012); err != nil {
		return fmt.Errorf("0012_skill_audit_results: %w", err)
	}
	if _, err := db.Exec(migration0013); err != nil {
		return fmt.Errorf("0013_user_soft_delete: %w", err)
	}
	if _, err := db.Exec(migration0014); err != nil {
		return fmt.Errorf("0014_token_version: %w", err)
	}
	if _, err := db.Exec(migration0015); err != nil {
		return fmt.Errorf("0015_api_token_encryption: %w", err)
	}
	if _, err := db.Exec(migration0016); err != nil {
		return fmt.Errorf("0016_user_theme: %w", err)
	}
	if _, err := db.Exec(migration0017); err != nil {
		return fmt.Errorf("0017_skill_version_move_action: %w", err)
	}
	if _, err := db.Exec(migration0018); err != nil {
		return fmt.Errorf("0018_skill_lock: %w", err)
	}
	return nil
}

// Exec is the subset of *sql.DB / *sql.Tx that the application reaches for.
// Anything that takes Exec can run inside or outside a transaction.
type Exec interface {
	ExecContext(ctx context.Context, query string, args ...interface{}) (sql.Result, error)
	QueryRowContext(ctx context.Context, query string, args ...interface{}) *sql.Row
}
