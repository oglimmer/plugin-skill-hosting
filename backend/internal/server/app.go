// Package server hosts the App-coupled HTTP layer: handlers, middleware, and
// the data structures that flow between them. Everything in this package shares
// the *App receiver (cfg + db + optional oidc runtime).
package server

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"regexp"
	"time"

	"marketplace/internal/config"
)

type App struct {
	Cfg  config.Config
	DB   *sql.DB
	OIDC *oidcRuntime // populated only when Cfg.AuthMode == "oidc"
}

// User account status values. The status column has a CHECK constraint on
// these exact strings — keep them in sync with migration 0008.
const (
	UserStatusApproved = "approved"
	UserStatusPending  = "pending"
	UserStatusRejected = "rejected"
)

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username"`
	APIToken  string    `json:"apiToken,omitempty"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"createdAt"`
}

type Plugin struct {
	ID            string     `json:"id"`
	OwnerID       string     `json:"ownerId"`
	OwnerName     string     `json:"ownerName,omitempty"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Version       string     `json:"version"`
	AuthorName    string     `json:"authorName"`
	AuthorEmail   string     `json:"authorEmail"`
	Homepage      string     `json:"homepage"`
	License       string     `json:"license"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	DeletedAt     *time.Time `json:"deletedAt,omitempty"`
	DeletedBy     *string    `json:"deletedBy,omitempty"`
	DeletedByName *string    `json:"deletedByName,omitempty"`
	Skills        []Skill    `json:"skills,omitempty"`
}

type Skill struct {
	ID            string     `json:"id"`
	PluginID      string     `json:"pluginId"`
	Name          string     `json:"name"`
	Description   string     `json:"description"`
	Body          string     `json:"body"`
	CreatedAt     time.Time  `json:"createdAt"`
	UpdatedAt     time.Time  `json:"updatedAt"`
	CreatedBy     *string    `json:"createdBy,omitempty"`
	CreatedByName *string    `json:"createdByName,omitempty"`
	UpdatedBy     *string    `json:"updatedBy,omitempty"`
	UpdatedByName *string    `json:"updatedByName,omitempty"`
	DeletedAt     *time.Time `json:"deletedAt,omitempty"`
	DeletedBy     *string    `json:"deletedBy,omitempty"`
	DeletedByName *string    `json:"deletedByName,omitempty"`
}

type SkillVersion struct {
	ID           string    `json:"id"`
	SkillID      string    `json:"skillId"`
	Version      int       `json:"version"`
	Action       string    `json:"action"`
	Name         string    `json:"name"`
	Description  string    `json:"description"`
	Body         string    `json:"body"`
	EditedBy     *string   `json:"editedBy,omitempty"`
	EditedByName *string   `json:"editedByName,omitempty"`
	EditedAt     time.Time `json:"editedAt"`
}

type ctxKey string

const ctxUserKey ctxKey = "user"

var (
	slugRe     = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}[a-z0-9]$`)
	usernameRe = regexp.MustCompile(`^[a-zA-Z0-9_-]{3,32}$`)
)

func currentUser(r *http.Request) *User {
	v, _ := r.Context().Value(ctxUserKey).(*User)
	return v
}

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
