package main

import (
	"database/sql"
	_ "embed"
	"fmt"
	"time"

	_ "github.com/lib/pq"
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

func openDB(url string) (*sql.DB, error) {
	db, err := sql.Open("postgres", url)
	if err != nil {
		return nil, err
	}
	for i := 0; i < 30; i++ {
		if err = db.Ping(); err == nil {
			return db, nil
		}
		time.Sleep(time.Second)
	}
	return nil, fmt.Errorf("ping db: %w", err)
}

func runMigrations(db *sql.DB) error {
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
	return nil
}
