package server

import (
	"database/sql"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
)

// UserSummary is the public-safe projection of a user row used by the
// directory endpoint. It deliberately omits password_hash, api_token, and
// oidc_* so the listing can never leak secrets.
type UserSummary struct {
	ID             string     `json:"id"`
	Username       string     `json:"username"`
	Email          string     `json:"email"`
	Status         string     `json:"status"`
	IsAdmin        bool       `json:"isAdmin"`
	CreatedAt      time.Time  `json:"createdAt"`
	ApprovedBy     *string    `json:"approvedBy,omitempty"`
	ApprovedByName *string    `json:"approvedByName,omitempty"`
	ApprovedAt     *time.Time `json:"approvedAt,omitempty"`
}

// Ordering: pending first so reviewers see actionable rows immediately,
// then approved (oldest first — gives a stable directory), then rejected
// for audit purposes. Soft-deleted users are filtered out entirely — they
// only linger so their plugins keep a valid owner reference.
const userListSelect = `
	SELECT u.id, u.username, u.email, u.status, u.is_admin, u.created_at,
	       u.approved_by, ap.username, u.approved_at
	FROM users u
	LEFT JOIN users ap ON ap.id = u.approved_by
	WHERE u.status <> 'deleted'
	ORDER BY
	    CASE u.status WHEN 'pending' THEN 0 WHEN 'approved' THEN 1 ELSE 2 END,
	    u.created_at ASC`

func (a *App) handleListUsers(w http.ResponseWriter, r *http.Request) {
	rows, err := a.DB.QueryContext(r.Context(), userListSelect)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	defer rows.Close()

	users := []UserSummary{}
	for rows.Next() {
		var u UserSummary
		var approvedBy, approvedByName sql.NullString
		var approvedAt sql.NullTime
		if err := rows.Scan(&u.ID, &u.Username, &u.Email, &u.Status, &u.IsAdmin, &u.CreatedAt,
			&approvedBy, &approvedByName, &approvedAt); err != nil {
			serverErr(w, r, err, "db error")
			return
		}
		if approvedBy.Valid {
			v := approvedBy.String
			u.ApprovedBy = &v
		}
		if approvedByName.Valid {
			v := approvedByName.String
			u.ApprovedByName = &v
		}
		if approvedAt.Valid {
			v := approvedAt.Time
			u.ApprovedAt = &v
		}
		users = append(users, u)
	}
	if err := rows.Err(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, users)
}

// transitionUser flips a target user's status, recording the approver and
// timestamp when going to 'approved'. Returns ErrNoRows when the target
// doesn't exist, and a generic error when the row is already in a terminal
// state that disallows this transition. The trailing error is non-nil only
// for unexpected backend failures so callers can log them via serverErr.
func (a *App) transitionUser(r *http.Request, targetID, newStatus string) (int, string, error) {
	approver := currentUser(r)
	if approver == nil {
		return http.StatusUnauthorized, "missing bearer token", nil
	}
	if approver.ID == targetID {
		return http.StatusBadRequest, "cannot change your own status", nil
	}

	var (
		query string
		args  []interface{}
	)
	switch newStatus {
	case UserStatusApproved:
		// Approve is the universal "let them in" action — it works on a fresh
		// pending request and also reinstates a previously rejected user. The
		// approved_by/approved_at columns record the most recent approval.
		query = `UPDATE users
		         SET status = 'approved', approved_by = $1, approved_at = NOW()
		         WHERE id = $2 AND status IN ('pending', 'rejected')`
		args = []interface{}{approver.ID, targetID}
	case UserStatusRejected:
		// A rejected row keeps approved_by/approved_at empty so the UI can
		// distinguish "approved by X" from "rejected".
		query = `UPDATE users
		         SET status = 'rejected', approved_by = NULL, approved_at = NULL
		         WHERE id = $1 AND status IN ('pending', 'approved')`
		args = []interface{}{targetID}
	default:
		return http.StatusInternalServerError, "unsupported transition", nil
	}

	res, err := a.DB.ExecContext(r.Context(), query, args...)
	if err != nil {
		return http.StatusInternalServerError, "db error", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return http.StatusInternalServerError, "db error", err
	}
	if n == 0 {
		var exists bool
		if e := a.DB.QueryRowContext(r.Context(),
			`SELECT EXISTS(SELECT 1 FROM users WHERE id = $1)`, targetID,
		).Scan(&exists); e != nil {
			return http.StatusInternalServerError, "db error", e
		} else if !exists {
			return http.StatusNotFound, "user not found", nil
		}
		return http.StatusConflict, "user is not in a state that can be " + newStatus, nil
	}
	return 0, "", nil
}

func (a *App) handleApproveUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	status, msg, err := a.transitionUser(r, id, UserStatusApproved)
	if err != nil {
		serverErr(w, r, err, msg)
		return
	}
	if status != 0 {
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleRejectUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	status, msg, err := a.transitionUser(r, id, UserStatusRejected)
	if err != nil {
		serverErr(w, r, err, msg)
		return
	}
	if status != 0 {
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteUser soft-deletes a rejected user by flipping their status to
// 'deleted'. We deliberately don't remove the row: plugins reference their
// owner via owner_id ON DELETE CASCADE, so a hard delete would wipe every
// plugin the user ever published. A 'deleted' user is hidden from the
// directory (see userListSelect) and can no longer log in (auth requires
// 'approved'), but the row survives so their plugins keep a valid owner.
func (a *App) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}

	res, err := a.DB.ExecContext(r.Context(),
		`UPDATE users SET status = 'deleted' WHERE id = $1 AND status = 'rejected'`, id)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	n, err := res.RowsAffected()
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if n == 0 {
		// Nothing updated: distinguish a missing user from one that isn't in
		// the rejected state required for deletion.
		var status string
		err := a.DB.QueryRowContext(r.Context(),
			`SELECT status FROM users WHERE id = $1`, id).Scan(&status)
		if err == sql.ErrNoRows {
			writeErr(w, http.StatusNotFound, "user not found")
			return
		}
		if err != nil {
			serverErr(w, r, err, "db error")
			return
		}
		writeErr(w, http.StatusConflict, "only rejected users can be deleted")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// setAdmin flips the is_admin flag on another user. Self-changes are blocked
// so an admin can't accidentally orphan their own access, and so the "last
// admin standing" cannot demote themselves into a lockout. Only acts on
// approved users — pending/rejected accounts must first be approved.
//
// Returns a trailing error for unexpected backend failures so callers can
// log via serverErr; expected outcomes (conflict, not-found) leave it nil.
func (a *App) setAdmin(r *http.Request, targetID string, makeAdmin bool) (int, string, error) {
	actor := currentUser(r)
	if actor == nil {
		return http.StatusUnauthorized, "missing bearer token", nil
	}
	if actor.ID == targetID {
		return http.StatusBadRequest, "cannot change your own admin status", nil
	}

	res, err := a.DB.ExecContext(r.Context(),
		`UPDATE users SET is_admin = $1 WHERE id = $2 AND status = 'approved'`,
		makeAdmin, targetID)
	if err != nil {
		return http.StatusInternalServerError, "db error", err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return http.StatusInternalServerError, "db error", err
	}
	if n == 0 {
		var status string
		err := a.DB.QueryRowContext(r.Context(),
			`SELECT status FROM users WHERE id = $1`, targetID).Scan(&status)
		if err == sql.ErrNoRows {
			return http.StatusNotFound, "user not found", nil
		}
		if err != nil {
			return http.StatusInternalServerError, "db error", err
		}
		return http.StatusConflict, "only approved users can be promoted or demoted", nil
	}
	return 0, "", nil
}

func (a *App) handlePromoteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	status, msg, err := a.setAdmin(r, id, true)
	if err != nil {
		serverErr(w, r, err, msg)
		return
	}
	if status != 0 {
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleDemoteUser(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !isUUID(id) {
		writeErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	status, msg, err := a.setAdmin(r, id, false)
	if err != nil {
		serverErr(w, r, err, msg)
		return
	}
	if status != 0 {
		writeErr(w, status, msg)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// isUUID is a light-touch shape check — we don't need to validate the
// version/variant nibble, just keep junk out of the SQL parameter so the DB
// returns a clean "not found" instead of a 22P02 type error.
func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if r != '-' {
				return false
			}
			continue
		}
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}
