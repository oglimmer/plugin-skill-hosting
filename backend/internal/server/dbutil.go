package server

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation. The reliable signal is SQLSTATE 23505 on the typed *pgconn.PgError
// that pgx surfaces; the string-match fallback only covers wrapped/!=pgx errors
// that never reach the typed form.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	msg := err.Error()
	return strings.Contains(msg, "duplicate") || strings.Contains(msg, "unique")
}

// respondDBOrConflict maps a write error to either 409 (unique violation,
// using the supplied conflict message) or a logged 500. The non-conflict
// branch routes through serverErr so the underlying error lands in logs
// instead of being swallowed behind "db error".
func respondDBOrConflict(w http.ResponseWriter, r *http.Request, err error, conflictMsg string) {
	if isUniqueViolation(err) {
		writeErr(w, http.StatusConflict, conflictMsg)
		return
	}
	serverErr(w, r, err, "db error")
}
