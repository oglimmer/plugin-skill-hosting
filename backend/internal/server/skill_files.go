package server

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-chi/chi/v5"

	"marketplace/internal/db"
	"marketplace/internal/metrics"
	"marketplace/internal/semver"
)

// Caps for supporting-file uploads. Per-file 10MB matches what users want for
// fonts and small docx templates; the per-skill total stops a single skill
// from monopolizing storage.
const (
	maxSkillFileBytes  = 10 * 1024 * 1024  // 10 MB per file
	maxSkillTotalBytes = 100 * 1024 * 1024 // 100 MB per skill
	maxSkillFileCount  = 50
)

// validSkillFileSegmentRe is applied to each "/"-separated segment of a skill
// file path; it rejects path-traversal characters and exotic unicode that
// would round-trip badly through git or filesystem layers. The top-level
// directory is matched by the same expression — scripts/references/assets are
// the conventional Anthropic-skill folders, but any name passing this regex
// (and the depth/length caps) is accepted.
var validSkillFileSegmentRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// SkillFile is what the API returns when listing or fetching a single file.
// For binary files content is base64-encoded; for text files content holds
// the raw UTF-8 string. The flag tells the frontend which mode applies.
type SkillFile struct {
	Path      string    `json:"path"`
	IsBinary  bool      `json:"isBinary"`
	SizeBytes int       `json:"sizeBytes"`
	Content   string    `json:"content,omitempty"` // text or base64, depending on IsBinary
	UpdatedAt time.Time `json:"updatedAt"`
}

// SkillFileSummary is the lightweight shape returned by the list endpoint and
// passed into the validator: no content, just metadata.
type SkillFileSummary struct {
	Path      string    `json:"path"`
	IsBinary  bool      `json:"isBinary"`
	SizeBytes int       `json:"sizeBytes"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type skillFileUpsertReq struct {
	Content  string `json:"content"`  // base64 if IsBinary, otherwise raw UTF-8
	IsBinary *bool  `json:"isBinary"` // optional; if absent we sniff
}

// validateSkillFilePath canonicalises a user-supplied path and enforces the
// structural rules. Returns the cleaned path on success. A path may live at the
// skill root (a bare filename, e.g. config.json) or under any folder whose name
// passes validSkillFileSegmentRe; the UI surfaces the conventional
// scripts/references/assets folders by default, but the API tool is free to put
// files at the root or under arbitrary folder names. SKILL.md is reserved — it
// is generated from the skill body at materialization time, so a root file by
// that name would clobber the manifest.
func validateSkillFilePath(p string) (string, error) {
	if p == "" {
		return "", errors.New("path is required")
	}
	if len(p) > 256 {
		return "", errors.New("path is too long (max 256 chars)")
	}
	cleaned := path.Clean(p)
	if cleaned != p {
		return "", errors.New("path must be canonical (no .., ./, double slashes)")
	}
	if strings.HasPrefix(cleaned, "/") {
		return "", errors.New("path must be relative")
	}
	parts := strings.Split(cleaned, "/")
	if len(parts) > 6 {
		return "", errors.New("path is nested too deep (max 6 segments)")
	}
	for _, seg := range parts {
		if seg == "" || seg == "." || seg == ".." {
			return "", errors.New("invalid path segment")
		}
		if !validSkillFileSegmentRe.MatchString(seg) {
			return "", fmt.Errorf("invalid characters in %q (allowed: A-Z a-z 0-9 _ . -)", seg)
		}
	}
	if strings.EqualFold(cleaned, "SKILL.md") {
		return "", errors.New("SKILL.md is reserved; edit the skill body instead")
	}
	return cleaned, nil
}

// decodeFileContent normalises the upsert payload into raw bytes plus the
// is_binary flag. IsBinary=true treats Content as base64; otherwise Content is
// raw UTF-8 and we reject anything that fails the validity check (the client
// must set IsBinary=true and re-encode as base64).
func decodeFileContent(req *skillFileUpsertReq) (data []byte, binary bool, err error) {
	if req.IsBinary != nil && *req.IsBinary {
		data, err = base64.StdEncoding.DecodeString(req.Content)
		if err != nil {
			return nil, false, errors.New("invalid base64 content")
		}
		return data, true, nil
	}
	data = []byte(req.Content)
	if !utf8.Valid(data) {
		return nil, false, errors.New("content is not valid UTF-8; set isBinary=true and base64-encode")
	}
	return data, false, nil
}

// loadSkillFiles returns all files for a skill ordered by path.
func (a *App) loadSkillFiles(ctx context.Context, skillID string) ([]SkillFile, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT path, content_text, content_blob, is_binary, size_bytes, updated_at
		FROM skill_files WHERE skill_id = $1 ORDER BY path ASC
	`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkillFile{}
	for rows.Next() {
		var f SkillFile
		var text sql.NullString
		var blob []byte
		if err := rows.Scan(&f.Path, &text, &blob, &f.IsBinary, &f.SizeBytes, &f.UpdatedAt); err != nil {
			return nil, err
		}
		if f.IsBinary {
			f.Content = base64.StdEncoding.EncodeToString(blob)
		} else if text.Valid {
			f.Content = text.String
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// loadSkillFileSummaries returns paths + metadata for a skill, used by the
// validator (which doesn't need contents) and the list endpoint.
func (a *App) loadSkillFileSummaries(ctx context.Context, skillID string) ([]SkillFileSummary, error) {
	rows, err := a.DB.QueryContext(ctx, `
		SELECT path, is_binary, size_bytes, updated_at
		FROM skill_files WHERE skill_id = $1 ORDER BY path ASC
	`, skillID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SkillFileSummary{}
	for rows.Next() {
		var f SkillFileSummary
		if err := rows.Scan(&f.Path, &f.IsBinary, &f.SizeBytes, &f.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// loadSingleSkillFile fetches one file by path; sql.ErrNoRows if absent.
func (a *App) loadSingleSkillFile(ctx context.Context, skillID, p string) (*SkillFile, error) {
	row := a.DB.QueryRowContext(ctx, `
		SELECT path, content_text, content_blob, is_binary, size_bytes, updated_at
		FROM skill_files WHERE skill_id = $1 AND path = $2
	`, skillID, p)
	var f SkillFile
	var text sql.NullString
	var blob []byte
	if err := row.Scan(&f.Path, &text, &blob, &f.IsBinary, &f.SizeBytes, &f.UpdatedAt); err != nil {
		return nil, err
	}
	if f.IsBinary {
		f.Content = base64.StdEncoding.EncodeToString(blob)
	} else if text.Valid {
		f.Content = text.String
	}
	return &f, nil
}

// skillTotalBytes returns the sum of size_bytes for the skill, optionally
// excluding one path (used so an upsert of an existing file doesn't double-
// count its previous size).
func (a *App) skillTotalBytes(ctx context.Context, skillID, excludePath string) (total int, count int, err error) {
	args := []interface{}{skillID}
	q := `SELECT COALESCE(SUM(size_bytes),0)::bigint, COUNT(*)::bigint FROM skill_files WHERE skill_id = $1`
	if excludePath != "" {
		q += ` AND path <> $2`
		args = append(args, excludePath)
	}
	var totalBig, countBig int64
	if err = a.DB.QueryRowContext(ctx, q, args...).Scan(&totalBig, &countBig); err != nil {
		return 0, 0, err
	}
	return int(totalBig), int(countBig), nil
}

// filePathParam pulls the wildcard tail from a URL like /files/scripts/foo.py.
// chi exposes it as URLParam("*").
func filePathParam(r *http.Request) string {
	return strings.TrimPrefix(chi.URLParam(r, "*"), "/")
}

func (a *App) handleListSkillFiles(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	s := a.loadActiveSkillOrRespond(w, r, p.ID)
	if s == nil {
		return
	}
	files, err := a.loadSkillFileSummaries(r.Context(), s.ID)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, files)
}

func (a *App) handleGetSkillFile(w http.ResponseWriter, r *http.Request) {
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	s := a.loadActiveSkillOrRespond(w, r, p.ID)
	if s == nil {
		return
	}
	pth, err := validateSkillFilePath(filePathParam(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := a.loadSingleSkillFile(r.Context(), s.ID, pth)
	if err == sql.ErrNoRows {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	writeJSON(w, http.StatusOK, f)
}

func (a *App) handleUpsertSkillFile(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	s := a.loadActiveSkillOrRespond(w, r, p.ID)
	if s == nil {
		return
	}
	if rejectIfLocked(w, s) {
		return
	}
	pth, err := validateSkillFilePath(filePathParam(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	var req skillFileUpsertReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	data, isBinary, err := decodeFileContent(&req)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if len(data) > maxSkillFileBytes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("file exceeds %d byte limit", maxSkillFileBytes))
		return
	}

	priorTotal, priorCount, err := a.skillTotalBytes(r.Context(), s.ID, pth)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if priorTotal+len(data) > maxSkillTotalBytes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("skill total would exceed %d bytes", maxSkillTotalBytes))
		return
	}
	// priorCount excludes the file being upserted; check whether adding it
	// would push the count over the cap (only matters for new files).
	exists := true
	if _, err := a.loadSingleSkillFile(r.Context(), s.ID, pth); err == sql.ErrNoRows {
		exists = false
	} else if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if !exists && priorCount+1 > maxSkillFileCount {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("skill exceeds file count limit (%d)", maxSkillFileCount))
		return
	}

	var contentText sql.NullString
	var contentBlob []byte
	if isBinary {
		contentBlob = data
	} else {
		contentText = sql.NullString{String: string(data), Valid: true}
	}

	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if _, err := tx.ExecContext(r.Context(), `
		INSERT INTO skill_files (skill_id, path, content_text, content_blob, is_binary, size_bytes)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (skill_id, path) DO UPDATE SET
			content_text = EXCLUDED.content_text,
			content_blob = EXCLUDED.content_blob,
			is_binary = EXCLUDED.is_binary,
			size_bytes = EXCLUDED.size_bytes,
			updated_at = now()
	`, s.ID, pth, contentText, contentBlob, isBinary, len(data)); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.recordSkillVersion(r.Context(), tx, s.ID, "update", s.Name, s.Description, s.Body, s.ExtraFrontmatter, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.bumpAndPersistPluginVersion(r.Context(), tx, p, semver.BumpPatch); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	f, err := a.loadSingleSkillFile(r.Context(), s.ID, pth)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	metrics.SkillFileMutationsTotal.WithLabelValues("upsert", "success").Inc()
	writeJSON(w, http.StatusOK, f)
}

func (a *App) handleDeleteSkillFile(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}
	s := a.loadActiveSkillOrRespond(w, r, p.ID)
	if s == nil {
		return
	}
	if rejectIfLocked(w, s) {
		return
	}
	pth, err := validateSkillFilePath(filePathParam(r))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}

	tx, err := a.DB.BeginTx(r.Context(), nil)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	res, err := tx.ExecContext(r.Context(),
		`DELETE FROM skill_files WHERE skill_id = $1 AND path = $2`, s.ID, pth)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if n, _ := res.RowsAffected(); n == 0 {
		writeErr(w, http.StatusNotFound, "file not found")
		return
	}
	if err := a.recordSkillVersion(r.Context(), tx, s.ID, "update", s.Name, s.Description, s.Body, s.ExtraFrontmatter, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := a.bumpAndPersistPluginVersion(r.Context(), tx, p, semver.BumpPatch); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	if err := a.materializePluginDetached(p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}
	metrics.SkillFileMutationsTotal.WithLabelValues("delete", "success").Inc()
	w.WriteHeader(http.StatusNoContent)
}

// snapshotSkillFiles copies the current skill_files rows for skillID into
// skill_file_versions tagged with versionID. Called from recordSkillVersion so
// every version row carries a frozen view of the supporting files.
func snapshotSkillFiles(ctx context.Context, tx db.Exec, skillVersionID, skillID string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO skill_file_versions (skill_version_id, path, content_text, content_blob, is_binary, size_bytes)
		SELECT $1, path, content_text, content_blob, is_binary, size_bytes
		FROM skill_files WHERE skill_id = $2
	`, skillVersionID, skillID)
	return err
}

// restoreSkillFilesFromVersion replaces the skill_files tree with the snapshot
// recorded under the given skill_versions row. Used by revert.
func restoreSkillFilesFromVersion(ctx context.Context, tx db.Exec, skillID, skillVersionID string) error {
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM skill_files WHERE skill_id = $1`, skillID); err != nil {
		return err
	}
	_, err := tx.ExecContext(ctx, `
		INSERT INTO skill_files (skill_id, path, content_text, content_blob, is_binary, size_bytes)
		SELECT $1, path, content_text, content_blob, is_binary, size_bytes
		FROM skill_file_versions WHERE skill_version_id = $2
	`, skillID, skillVersionID)
	return err
}
