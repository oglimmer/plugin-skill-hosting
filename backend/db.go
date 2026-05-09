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
	return nil
}
