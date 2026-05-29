// Package server hosts the App-coupled HTTP layer: handlers, middleware, and
// the data structures that flow between them. Everything in this package shares
// the *App receiver (cfg + db + optional oidc runtime).
package server

import (
	"database/sql"
	"encoding/json"
	"log"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5/middleware"

	"marketplace/internal/config"
	"marketplace/internal/email"
)

type App struct {
	Cfg  config.Config
	DB   *sql.DB
	OIDC *oidcRuntime // populated only when Cfg.AuthMode == "oidc"

	// ExternalSync mirrors marketplace state to a configured external git
	// repo (GitHub/GitLab/…) on every materialize/delete. nil when the
	// feature is disabled (Cfg.ExternalGitRemoteURL empty).
	ExternalSync *externalSync

	// Email sends outbound notifications (currently only skill-audit alerts).
	// Its zero value is "not configured" — Send is a no-op guarded by callers.
	Email email.Sender

	// auditRunning guards against overlapping audit sweeps: a manual trigger
	// while the scheduled sweep is in flight (or vice versa) is rejected.
	auditRunning atomic.Bool

	// ready gates the readiness probe. False while REMATERIALIZE_ON_STARTUP is
	// running; true otherwise. Use MarkReady/IsReady to access it.
	ready atomic.Bool
}

func (a *App) MarkReady()    { a.ready.Store(true) }
func (a *App) IsReady() bool { return a.ready.Load() }

// User account status values. The status column has a CHECK constraint on
// these exact strings — keep them in sync with migrations 0008 and 0013.
// 'deleted' is a soft-delete terminal state: the row is retained so plugins
// keep a valid owner, but the account is hidden from the directory and can't
// log in.
const (
	UserStatusApproved = "approved"
	UserStatusPending  = "pending"
	UserStatusRejected = "rejected"
	UserStatusDeleted  = "deleted"
)

type User struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Username  string    `json:"username"`
	APIToken  string    `json:"apiToken,omitempty"`
	Status    string    `json:"status"`
	IsAdmin   bool      `json:"isAdmin"`
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
	ID               string     `json:"id"`
	PluginID         string     `json:"pluginId"`
	Name             string     `json:"name"`
	Description      string     `json:"description"`
	Body             string     `json:"body"`
	ExtraFrontmatter string     `json:"extraFrontmatter"`
	CreatedAt        time.Time  `json:"createdAt"`
	UpdatedAt        time.Time  `json:"updatedAt"`
	CreatedBy        *string    `json:"createdBy,omitempty"`
	CreatedByName    *string    `json:"createdByName,omitempty"`
	UpdatedBy        *string    `json:"updatedBy,omitempty"`
	UpdatedByName    *string    `json:"updatedByName,omitempty"`
	DeletedAt        *time.Time `json:"deletedAt,omitempty"`
	DeletedBy        *string    `json:"deletedBy,omitempty"`
	DeletedByName    *string    `json:"deletedByName,omitempty"`
}

type SkillVersion struct {
	ID               string    `json:"id"`
	SkillID          string    `json:"skillId"`
	Version          int       `json:"version"`
	Action           string    `json:"action"`
	Name             string    `json:"name"`
	Description      string    `json:"description"`
	Body             string    `json:"body"`
	ExtraFrontmatter string    `json:"extraFrontmatter"`
	EditedBy         *string   `json:"editedBy,omitempty"`
	EditedByName     *string   `json:"editedByName,omitempty"`
	EditedAt         time.Time `json:"editedAt"`
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

// serverErr logs the underlying error tagged with the chi request ID, method,
// and route pattern, then responds with a generic 500 JSON body so internal
// detail doesn't leak to clients. Use this anywhere a 500 is returned because
// of an unexpected failure (DB, IO, encoding, …) — silent "db error" replies
// were the main reason intermittent PgBouncer / network errors couldn't be
// triaged from kubectl logs.
func serverErr(w http.ResponseWriter, r *http.Request, err error, publicMsg string) {
	log.Printf("ERROR reqID=%s %s %s: %s: %v",
		middleware.GetReqID(r.Context()), r.Method, r.URL.Path, publicMsg, err)
	writeErr(w, http.StatusInternalServerError, publicMsg)
}
