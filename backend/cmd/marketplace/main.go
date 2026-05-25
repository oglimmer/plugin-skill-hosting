// Command marketplace is the plugin-skill-hosting backend HTTP server.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"marketplace/internal/config"
	"marketplace/internal/db"
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

	app := &server.App{Cfg: cfg, DB: pool}

	if err := app.InitExternalSync(context.Background()); err != nil {
		log.Fatalf("external git sync init: %v", err)
	}

	if cfg.RematerializeOnStartup {
		go app.RematerializeAll(context.Background())
	} else {
		app.MarkReady()
	}

	if cfg.AuthMode == "oidc" {
		if err := app.InitOIDC(context.Background()); err != nil {
			log.Fatalf("oidc init: %v", err)
		}
	}

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

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	log.Println("shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(ctx)
}
