// Command marketplace is the plugin-skill-hosting backend HTTP server.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"marketplace/internal/config"
	"marketplace/internal/db"
	"marketplace/internal/email"
	"marketplace/internal/metrics"
	"marketplace/internal/server"
)

func main() {
	cfg := config.Load()

	if err := os.MkdirAll(cfg.DataDir+"/repos", 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir+"/work", 0o755); err != nil {
		log.Fatalf("create work dir: %v", err)
	}

	pool, err := db.Open(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	if err := db.Migrate(pool); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	metrics.RegisterDBStats(pool)

	app := &server.App{
		Cfg: cfg,
		DB:  pool,
		Email: email.Sender{
			Host:     cfg.SMTPHost,
			Port:     cfg.SMTPPort,
			Username: cfg.SMTPUsername,
			Password: cfg.SMTPPassword,
			From:     cfg.SMTPFrom,
			UseTLS:   cfg.SMTPUseTLS,
		},
	}

	// rootCtx is cancelled on SIGINT/SIGTERM. Every background worker derives
	// from it, so a shutdown signal cancels in-flight DB queries and git
	// subprocesses instead of letting them outlive the process.
	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// bg tracks the long-lived background goroutines so shutdown can wait for
	// them to unwind after rootCtx is cancelled.
	var bg sync.WaitGroup

	// One-time (idempotent) encryption of any API tokens still stored in
	// plaintext from before migration 0015, clearing the plaintext as it goes.
	app.BackfillAPITokenCiphertext(rootCtx)

	if err := app.InitExternalSync(rootCtx); err != nil {
		log.Fatalf("external git sync init: %v", err)
	}

	if cfg.RematerializeOnStartup {
		bg.Add(1)
		go func() {
			defer bg.Done()
			app.RematerializeAll(rootCtx)
		}()
	} else {
		app.MarkReady()
	}

	if cfg.AuthMode == "oidc" {
		if err := app.InitOIDC(rootCtx); err != nil {
			log.Fatalf("oidc init: %v", err)
		}
	}

	app.StartOAuthGC(rootCtx, &bg)
	app.StartSkillAudit(rootCtx, &bg)

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           server.NewRouter(app),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go func() {
		log.Printf("listening on %s", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("listen: %v", err)
		}
	}()

	<-rootCtx.Done()
	log.Println("shutting down")
	stop() // restore default signal handling so a second signal kills hard

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown: %v", err)
	}

	// Wait for background workers to drain, bounded by the same deadline so a
	// wedged worker can't block exit indefinitely.
	done := make(chan struct{})
	go func() { bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-shutdownCtx.Done():
		log.Println("shutdown: background workers did not exit in time")
	}
}
