package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"time"
)

type App struct {
	cfg  Config
	db   *sql.DB
	oidc *OIDCRuntime // populated only when cfg.AuthMode == "oidc"
}

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username"`
	APIToken  string    `json:"apiToken,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
}

type Plugin struct {
	ID          string    `json:"id"`
	OwnerID     string    `json:"ownerId"`
	OwnerName   string    `json:"ownerName,omitempty"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Version     string    `json:"version"`
	AuthorName  string    `json:"authorName"`
	AuthorEmail string    `json:"authorEmail"`
	Homepage    string    `json:"homepage"`
	License     string    `json:"license"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Skills      []Skill   `json:"skills,omitempty"`
}

type Skill struct {
	ID          string    `json:"id"`
	PluginID    string    `json:"pluginId"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Body        string    `json:"body"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
