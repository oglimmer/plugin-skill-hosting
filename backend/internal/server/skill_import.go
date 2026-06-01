package server

import (
	"archive/zip"
	"bytes"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"unicode/utf8"

	"marketplace/internal/metrics"
	"marketplace/internal/semver"
)

// maxSkillImportZipBytes caps the uploaded archive itself. It is generous
// enough to cover the per-skill total (100 MB) plus zip overhead, but small
// enough to keep a buffered read sane.
const maxSkillImportZipBytes = 110 * 1024 * 1024

type parsedSkillFile struct {
	Path     string
	Data     []byte
	IsBinary bool
}

type parsedSkillImport struct {
	Name             string
	Description      string
	Body             string
	ExtraFrontmatter string
	Files            []parsedSkillFile
}

// parseSkillFrontmatter extracts name+description from the YAML-style
// frontmatter buildSkillMarkdown writes, and returns the rest of the document
// as body. Any frontmatter keys other than name and description are returned
// verbatim in extra so they round-trip on re-materialization. The first
// non-empty line must be "---" and the block terminates at the next "---". In
// addition to plain "key: value" pairs it understands the two block-scalar
// styles that show up in real SKILL.md files: folded (">") collapses indented
// continuation lines into a single space-joined string (preserving paragraph
// breaks), and literal ("|") keeps the newlines.
func parseSkillFrontmatter(content []byte) (name, description, extra, body string, err error) {
	lines := strings.Split(string(content), "\n")
	for i := range lines {
		lines[i] = strings.TrimRight(lines[i], "\r")
	}

	i := 0
	for i < len(lines) && strings.TrimSpace(lines[i]) == "" {
		i++
	}
	if i >= len(lines) || lines[i] != "---" {
		return "", "", "", "", errors.New("SKILL.md must start with '---' frontmatter delimiter")
	}
	i++
	start := i
	end := -1
	for ; i < len(lines); i++ {
		if lines[i] == "---" {
			end = i
			break
		}
	}
	if end == -1 {
		return "", "", "", "", errors.New("SKILL.md frontmatter must be terminated with '---'")
	}

	block := lines[start:end]
	fields := parseFrontmatterFields(block)
	name = fields["name"]
	description = fields["description"]
	if name == "" {
		return "", "", "", "", errors.New("SKILL.md frontmatter missing 'name'")
	}
	if description == "" {
		return "", "", "", "", errors.New("SKILL.md frontmatter missing 'description'")
	}
	extra = extractExtraFrontmatter(block)

	bodyStr := strings.Join(lines[end+1:], "\n")
	body = strings.TrimLeft(bodyStr, "\n")
	return name, description, extra, body, nil
}

// extractExtraFrontmatter walks the same line range parseFrontmatterFields
// reads but returns the verbatim source for every top-level key other than
// name and description. Continuation lines (indented or blank) follow the
// preceding top-level key. The result preserves comments, YAML lists, block
// scalars, and exact indentation so the round-trip is lossless.
func extractExtraFrontmatter(lines []string) string {
	var out []string
	// Start "include = true" so any comments or unknown content before the
	// first recognised key are preserved.
	include := true
	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)
		if indent == 0 && trimmed != "" {
			if idx := strings.Index(trimmed, ":"); idx > 0 {
				key := strings.TrimSpace(trimmed[:idx])
				switch key {
				case "name", "description":
					include = false
				default:
					include = true
				}
			}
			// A non-colon column-0 line (e.g. a comment) inherits the
			// current include state, so comments grouped with the current
			// key follow that key's fate.
		}
		if include {
			out = append(out, line)
		}
	}
	// Drop leading/trailing blank lines but keep internal structure.
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// parseFrontmatterFields walks the lines between the two "---" markers and
// returns a map of recognised keys. Each key may use a plain inline value, a
// folded ">" block, or a literal "|" block. Continuation lines belong to the
// most recent key as long as they are indented past column 0.
func parseFrontmatterFields(lines []string) map[string]string {
	fields := map[string]string{}
	var curKey string
	var curStyle byte // 0=plain, '>'=folded, '|'=literal
	var curBuf []string

	flush := func() {
		if curKey == "" || curStyle == 0 {
			curKey = ""
			curStyle = 0
			curBuf = nil
			return
		}
		if curStyle == '|' {
			fields[curKey] = strings.TrimRight(joinLiteral(curBuf), "\n")
		} else {
			fields[curKey] = joinFolded(curBuf)
		}
		curKey = ""
		curStyle = 0
		curBuf = nil
	}

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		indent := len(line) - len(trimmed)

		if indent == 0 {
			if trimmed == "" {
				// Blank line at column 0 — for an active block scalar this is
				// a paragraph break; otherwise just ignored.
				if curStyle == '>' || curStyle == '|' {
					curBuf = append(curBuf, "")
				}
				continue
			}
			idx := strings.Index(trimmed, ":")
			if idx <= 0 {
				continue
			}
			flush()
			key := strings.TrimSpace(trimmed[:idx])
			val := strings.TrimSpace(trimmed[idx+1:])
			curKey = key
			switch {
			case strings.HasPrefix(val, ">"):
				curStyle = '>'
			case strings.HasPrefix(val, "|"):
				curStyle = '|'
			default:
				curStyle = 0
				fields[key] = strings.Trim(val, `"'`)
			}
			continue
		}

		// Indented continuation.
		if curStyle == '>' || curStyle == '|' {
			curBuf = append(curBuf, trimmed)
		}
	}
	flush()
	return fields
}

// joinFolded implements YAML's ">" semantics narrowly: blank lines between
// non-blank lines become a single newline (paragraph break); otherwise lines
// are joined with a space. Leading and trailing blank runs are dropped.
func joinFolded(lines []string) string {
	// Strip leading and trailing blank lines.
	for len(lines) > 0 && strings.TrimSpace(lines[0]) == "" {
		lines = lines[1:]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	var b strings.Builder
	wroteAny := false
	prevBlank := false
	for _, l := range lines {
		if strings.TrimSpace(l) == "" {
			b.WriteByte('\n')
			prevBlank = true
			continue
		}
		if wroteAny && !prevBlank {
			b.WriteByte(' ')
		}
		b.WriteString(strings.TrimSpace(l))
		wroteAny = true
		prevBlank = false
	}
	return b.String()
}

// joinLiteral implements YAML's "|" semantics: newlines are preserved as-is.
// Indented continuation lines are already stored without their common indent.
func joinLiteral(lines []string) string {
	return strings.Join(lines, "\n")
}

// unsupportedSkillRoots lists top-level directories that are valid in the
// upstream skill format but the marketplace doesn't store yet. We silently
// drop them on import rather than reject the whole zip.
var unsupportedSkillRoots = map[string]bool{
	"evals": true,
}

// shouldSkipZipEntry filters out macOS/Finder artefacts that frequently sneak
// into zips, plus content the marketplace doesn't yet support (evals/). They
// would otherwise fail strict path validation and frustrate users who didn't
// intentionally include them.
func shouldSkipZipEntry(p string) bool {
	if strings.HasPrefix(p, "__MACOSX/") || p == "__MACOSX" {
		return true
	}
	base := path.Base(p)
	if base == ".DS_Store" || strings.HasPrefix(base, "._") {
		return true
	}
	if i := strings.Index(p, "/"); i > 0 {
		if unsupportedSkillRoots[p[:i]] {
			return true
		}
	} else if unsupportedSkillRoots[p] {
		return true
	}
	return false
}

// extractSkillZip parses an uploaded zip archive into a skill payload.
// SKILL.md must be present, either at the root or under a single top-level
// directory (which is then stripped). Every other file must validate against
// validateSkillFilePath, i.e. either a bare filename at the skill root or a
// path under a folder (scripts/, references/, assets/, or any other name).
func extractSkillZip(buf []byte) (*parsedSkillImport, error) {
	zr, err := zip.NewReader(bytes.NewReader(buf), int64(len(buf)))
	if err != nil {
		return nil, fmt.Errorf("invalid zip: %w", err)
	}
	// Pass 1: find SKILL.md and determine whether all entries live under a
	// single top-level directory we should strip.
	var skillEntry *zip.File
	prefix := ""
	for _, f := range zr.File {
		if strings.Contains(f.Name, `\`) {
			return nil, fmt.Errorf("invalid path %q (backslash)", f.Name)
		}
		if shouldSkipZipEntry(f.Name) {
			continue
		}
		clean := path.Clean(f.Name)
		if strings.HasPrefix(clean, "/") || clean == ".." || strings.HasPrefix(clean, "../") {
			return nil, fmt.Errorf("invalid path %q", f.Name)
		}
		if f.FileInfo().IsDir() {
			continue
		}
		if path.Base(clean) == "SKILL.md" {
			if skillEntry != nil {
				return nil, errors.New("multiple SKILL.md entries found")
			}
			skillEntry = f
			if dir := path.Dir(clean); dir != "." {
				if strings.Contains(dir, "/") {
					return nil, fmt.Errorf("SKILL.md must be at root or in a single top-level directory; found %q", f.Name)
				}
				prefix = dir + "/"
			}
		}
	}
	if skillEntry == nil {
		return nil, errors.New("zip does not contain SKILL.md")
	}

	skillBytes, err := readZipEntry(skillEntry, maxSkillFileBytes)
	if err != nil {
		return nil, fmt.Errorf("SKILL.md: %w", err)
	}
	if !utf8.Valid(skillBytes) {
		return nil, errors.New("SKILL.md is not valid UTF-8")
	}
	name, description, extra, body, err := parseSkillFrontmatter(skillBytes)
	if err != nil {
		return nil, err
	}

	out := &parsedSkillImport{
		Name:             strings.ToLower(strings.TrimSpace(name)),
		Description:      description,
		Body:             body,
		ExtraFrontmatter: extra,
	}
	totalBytes := 0
	for _, f := range zr.File {
		if f == skillEntry || f.FileInfo().IsDir() || shouldSkipZipEntry(f.Name) {
			continue
		}
		if !f.FileInfo().Mode().IsRegular() {
			return nil, fmt.Errorf("entry %q is not a regular file", f.Name)
		}
		clean := path.Clean(f.Name)
		if prefix != "" {
			if !strings.HasPrefix(clean, prefix) {
				return nil, fmt.Errorf("entry %q is outside the skill directory %q", f.Name, prefix)
			}
			clean = strings.TrimPrefix(clean, prefix)
		}
		// Re-check after prefix-stripping so e.g. "my-skill/evals/x.md"
		// is dropped the same way as a top-level "evals/x.md".
		if shouldSkipZipEntry(clean) {
			continue
		}
		validated, err := validateSkillFilePath(clean)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		data, err := readZipEntry(f, maxSkillFileBytes)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		totalBytes += len(data)
		if totalBytes > maxSkillTotalBytes {
			return nil, fmt.Errorf("zip exceeds %d byte total skill limit", maxSkillTotalBytes)
		}
		if len(out.Files)+1 > maxSkillFileCount {
			return nil, fmt.Errorf("zip exceeds %d file limit", maxSkillFileCount)
		}
		out.Files = append(out.Files, parsedSkillFile{
			Path:     validated,
			Data:     data,
			IsBinary: !utf8.Valid(data),
		})
	}
	return out, nil
}

func readZipEntry(f *zip.File, maxBytes int) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, int64(maxBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxBytes {
		return nil, fmt.Errorf("exceeds %d byte limit", maxBytes)
	}
	return data, nil
}

func (a *App) handleImportSkill(w http.ResponseWriter, r *http.Request) {
	user := currentUser(r)
	p := a.loadActivePluginOrRespond(w, r)
	if p == nil {
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxSkillImportZipBytes)
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid upload: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing 'file' upload")
		return
	}
	defer file.Close()

	buf, err := io.ReadAll(io.LimitReader(file, maxSkillImportZipBytes+1))
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read upload: "+err.Error())
		return
	}
	if int64(len(buf)) > maxSkillImportZipBytes {
		writeErr(w, http.StatusBadRequest, fmt.Sprintf("zip exceeds %d byte limit", maxSkillImportZipBytes))
		return
	}

	parsed, err := extractSkillZip(buf)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !slugRe.MatchString(parsed.Name) {
		writeErr(w, http.StatusBadRequest, "SKILL.md name must be 3-64 chars, lowercase, [a-z0-9-]")
		return
	}

	priorSkillCount, err := a.pluginSkillCount(r.Context(), p.ID)
	if err != nil {
		serverErr(w, r, err, "db error")
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

	var id string
	if err := tx.QueryRowContext(r.Context(), `
		INSERT INTO skills (plugin_id, name, description, body, extra_frontmatter, created_by, updated_by)
		VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id
	`, p.ID, parsed.Name, parsed.Description, parsed.Body, parsed.ExtraFrontmatter, user.ID).Scan(&id); err != nil {
		respondDBOrConflict(w, r, err, "skill with that name already exists")
		return
	}
	for _, f := range parsed.Files {
		var contentText sql.NullString
		var contentBlob []byte
		if f.IsBinary {
			contentBlob = f.Data
		} else {
			contentText = sql.NullString{String: string(f.Data), Valid: true}
		}
		if _, err := tx.ExecContext(r.Context(), `
			INSERT INTO skill_files (skill_id, path, content_text, content_blob, is_binary, size_bytes)
			VALUES ($1, $2, $3, $4, $5, $6)
		`, id, f.Path, contentText, contentBlob, f.IsBinary, len(f.Data)); err != nil {
			serverErr(w, r, err, "db error")
			return
		}
	}
	if err := a.recordSkillVersion(r.Context(), tx, id, "create", parsed.Name, parsed.Description, parsed.Body, parsed.ExtraFrontmatter, user.ID); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	if err := tx.Commit(); err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	committed = true

	if priorSkillCount == 0 {
		if err := a.touchPluginUpdatedAt(r.Context(), p.ID); err != nil {
			serverErr(w, r, err, "db error")
			return
		}
	} else {
		if err := a.bumpAndPersistPluginVersion(r.Context(), p, semver.BumpMajor); err != nil {
			serverErr(w, r, err, "db error")
			return
		}
	}

	if err := a.materializePlugin(r.Context(), p); err != nil {
		writeErr(w, http.StatusInternalServerError, "git materialize: "+err.Error())
		return
	}

	metrics.SkillMutationsTotal.WithLabelValues("create", "success").Inc()
	if s, err := a.loadSkillByID(r.Context(), id); err == nil {
		writeJSON(w, http.StatusOK, s)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id})
}
