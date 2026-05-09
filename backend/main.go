package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

type Config struct {
	DatabaseURL   string
	JWTSecret     string
	ListenAddr    string
	DataDir       string
	PublicBaseURL string
}

func loadConfig() Config {
	c := Config{
		DatabaseURL:   getenv("DATABASE_URL", "postgres://marketplace:marketplace@localhost:5432/marketplace?sslmode=disable"),
		JWTSecret:     getenv("JWT_SECRET", "dev-secret-change-me-please-32-chars-min"),
		ListenAddr:    getenv("LISTEN_ADDR", ":8080"),
		DataDir:       getenv("DATA_DIR", "./data"),
		PublicBaseURL: getenv("PUBLIC_BASE_URL", "http://localhost:8080"),
	}
	return c
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	cfg := loadConfig()

	if err := os.MkdirAll(cfg.DataDir+"/repos", 0o755); err != nil {
		log.Fatalf("create data dir: %v", err)
	}
	if err := os.MkdirAll(cfg.DataDir+"/work", 0o755); err != nil {
		log.Fatalf("create work dir: %v", err)
	}

	db, err := openDB(cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer db.Close()

	if err := runMigrations(db); err != nil {
		log.Fatalf("migrate: %v", err)
	}

	app := &App{cfg: cfg, db: db}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   []string{"*"},
		AllowedMethods:   []string{"GET", "POST", "PUT", "DELETE", "OPTIONS"},
		AllowedHeaders:   []string{"Authorization", "Content-Type"},
		ExposedHeaders:   []string{"Link"},
		AllowCredentials: false,
		MaxAge:           300,
	}))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	})

	r.Get("/marketplace.json", app.handleMarketplaceJSON)
	r.Mount("/git", app.gitHandler())

	r.Route("/api", func(r chi.Router) {
		r.Post("/auth/register", app.handleRegister)
		r.Post("/auth/login", app.handleLogin)

		r.Get("/plugins", app.handleListPlugins)
		r.Get("/plugins/{name}", app.handleGetPlugin)

		r.Group(func(r chi.Router) {
			r.Use(app.authMiddleware)
			r.Get("/me", app.handleMe)
			r.Post("/plugins", app.handleCreatePlugin)
			r.Delete("/plugins/{name}", app.handleDeletePlugin)
			r.Post("/plugins/{name}/skills", app.handleCreateSkill)
			r.Put("/plugins/{name}/skills/{skill}", app.handleUpdateSkill)
			r.Delete("/plugins/{name}/skills/{skill}", app.handleDeleteSkill)
		})
	})

	srv := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           r,
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
