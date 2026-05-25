package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// importSystemUsername is the username used for the catch-all user that
// owns/edits records imported from external git when no DB user matches the
// commit author email. Auto-created lazily on first webhook fire.
const (
	importSystemUsername = "external-git-sync"
	importSystemEmail    = "external-git-sync@local"
)

// reconcileImportedPlugin synchronises one plugin's state from the external
// work tree (already reset to FETCH_HEAD by importFromRemote) into Postgres.
// On disk-missing the plugin is soft-deleted; otherwise the manifest is
// upserted and skills + files are reconciled. Re-materializes the internal
// bare repo at /data/repos/<name>.git so /git/<name>.git serves the same
// state the marketplace UI now sees.
func (a *App) reconcileImportedPlugin(ctx context.Context, pluginName string, author commitAuthor) error {
	if a.ExternalSync == nil {
		return errors.New("external sync not enabled")
	}
	pluginDir := filepath.Join(a.ExternalSync.workDir, "plugins", pluginName)

	info, statErr := os.Stat(pluginDir)
	pluginExists := statErr == nil && info.IsDir()
	if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("stat plugin dir: %w", statErr)
	}

	actorID, err := a.resolveImportOwner(ctx, author.Email)
	if err != nil {
		return fmt.Errorf("resolve owner: %w", err)
	}

	existing, err := a.loadPluginByNameAny(ctx, pluginName)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load plugin: %w", err)
	}
	dbHasPlugin := err == nil

	if !pluginExists {
		if !dbHasPlugin || existing.DeletedAt != nil {
			return nil // never existed in DB, or already soft-deleted.
		}
		return a.softDeletePluginFromImport(ctx, existing, actorID)
	}

	manifest, err := readPluginManifest(pluginDir)
	if err != nil {
		return fmt.Errorf("read manifest: %w", err)
	}

	var pluginID string
	switch {
	case !dbHasPlugin:
		pluginID, err = a.insertPluginFromImport(ctx, pluginName, manifest, actorID)
		if err != nil {
			return fmt.Errorf("insert plugin: %w", err)
		}
		log.Printf("external git import: inserted plugin %q (owner=%s)", pluginName, actorID)
	default:
		pluginID = existing.ID
		if err := a.updatePluginFromImport(ctx, existing, manifest); err != nil {
			return fmt.Errorf("update plugin: %w", err)
		}
	}

	if err := a.reconcileSkillsFromImport(ctx, pluginID, pluginDir, actorID); err != nil {
		return fmt.Errorf("reconcile skills: %w", err)
	}

	// Re-materialize the internal bare repo so /git/<plugin>.git matches the
	// imported state. The withSkipExternalPush flag prevents materializePlugin
	// from re-pushing back to the same external remote we just imported from.
	refreshed, err := a.loadPluginByName(ctx, pluginName)
	if err != nil {
		return fmt.Errorf("reload plugin: %w", err)
	}
	return a.materializePlugin(withSkipExternalPush(ctx), refreshed)
}

// readPluginManifest parses .claude-plugin/plugin.json under pluginDir. A
// missing manifest is treated as an empty one — that matches what the
// marketplace would generate when the only edit was to skills/.
func readPluginManifest(pluginDir string) (pluginManifest, error) {
	var m pluginManifest
	raw, err := os.ReadFile(filepath.Join(pluginDir, ".claude-plugin", "plugin.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return m, err
	}
	return m, nil
}

func (a *App) insertPluginFromImport(ctx context.Context, name string, m pluginManifest, actorID string) (string, error) {
	if !slugRe.MatchString(name) {
		return "", fmt.Errorf("plugin name %q is not a valid slug", name)
	}
	version := strings.TrimSpace(m.Version)
	if version == "" {
		version = "0.1.0"
	}
	var authorName, authorEmail string
	if m.Author != nil {
		authorName = m.Author.Name
		authorEmail = m.Author.Email
	}
	var id string
	err := a.DB.QueryRowContext(ctx, `
		INSERT INTO plugins (owner_id, name, description, version, author_name, author_email, homepage, license)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING id
	`, actorID, name, m.Description, version, authorName, authorEmail, m.Homepage, m.License).Scan(&id)
	return id, err
}

func (a *App) updatePluginFromImport(ctx context.Context, existing *Plugin, m pluginManifest) error {
	version := strings.TrimSpace(m.Version)
	if version == "" {
		version = existing.Version
	}
	var authorName, authorEmail string
	if m.Author != nil {
		authorName = m.Author.Name
		authorEmail = m.Author.Email
	}
	_, err := a.DB.ExecContext(ctx, `
		UPDATE plugins
		   SET description = $1, version = $2,
		       author_name = $3, author_email = $4,
		       homepage = $5, license = $6,
		       updated_at = now(),
		       deleted_at = NULL, deleted_by = NULL
		 WHERE id = $7
	`, m.Description, version, authorName, authorEmail, m.Homepage, m.License, existing.ID)
	return err
}

func (a *App) softDeletePluginFromImport(ctx context.Context, existing *Plugin, actorID string) error {
	_, err := a.DB.ExecContext(ctx, `
		UPDATE plugins SET deleted_at = now(), deleted_by = $1, updated_at = now()
		WHERE id = $2
	`, actorID, existing.ID)
	if err != nil {
		return err
	}
	log.Printf("external git import: soft-deleted plugin %q", existing.Name)
	return nil
}

// reconcileSkillsFromImport walks plugin/skills/* on disk against the
// active skills in DB, and inserts / updates / soft-deletes / reconciles
// supporting files to match. Each change records a skill_versions row so
// "Edit history" in the UI shows the external sync as the origin of the
// change (edited_by = actorID, action = create/update/delete).
func (a *App) reconcileSkillsFromImport(ctx context.Context, pluginID, pluginDir, actorID string) error {
	skillsRoot := filepath.Join(pluginDir, "skills")
	onDiskSkills := map[string]string{} // name -> abs dir
	entries, err := os.ReadDir(skillsRoot)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillDir := filepath.Join(skillsRoot, e.Name())
		// Only count it as a skill if SKILL.md is present — empty dirs that
		// might appear during a partial push are ignored.
		if _, err := os.Stat(filepath.Join(skillDir, "SKILL.md")); err == nil {
			onDiskSkills[e.Name()] = skillDir
		}
	}

	existing, err := a.loadSkillsForPlugin(ctx, pluginID)
	if err != nil {
		return err
	}
	existingByName := map[string]Skill{}
	for _, s := range existing {
		existingByName[s.Name] = s
	}

	for name, skillDir := range onDiskSkills {
		if !slugRe.MatchString(name) {
			log.Printf("external git import: skipping skill with invalid slug %q", name)
			continue
		}
		raw, err := os.ReadFile(filepath.Join(skillDir, "SKILL.md"))
		if err != nil {
			return err
		}
		_, desc, extra, body, err := parseSkillFrontmatter(raw)
		if err != nil {
			log.Printf("external git import: skill %q has unparseable SKILL.md: %v", name, err)
			continue
		}

		var skillID string
		if cur, ok := existingByName[name]; ok {
			skillID = cur.ID
			contentChanged := cur.Description != desc || cur.Body != body || cur.ExtraFrontmatter != extra
			if contentChanged {
				if _, err := a.DB.ExecContext(ctx, `
					UPDATE skills SET description=$1, body=$2, extra_frontmatter=$3,
					                  updated_at=now(), updated_by=$4,
					                  deleted_at=NULL, deleted_by=NULL
					WHERE id=$5
				`, desc, body, extra, actorID, skillID); err != nil {
					return err
				}
				if err := a.recordSkillVersion(ctx, a.DB, skillID, "update", name, desc, body, extra, actorID); err != nil {
					return err
				}
			}
		} else {
			if err := a.DB.QueryRowContext(ctx, `
				INSERT INTO skills (plugin_id, name, description, body, extra_frontmatter, created_by, updated_by)
				VALUES ($1, $2, $3, $4, $5, $6, $6) RETURNING id
			`, pluginID, name, desc, body, extra, actorID).Scan(&skillID); err != nil {
				return err
			}
			if err := a.recordSkillVersion(ctx, a.DB, skillID, "create", name, desc, body, extra, actorID); err != nil {
				return err
			}
		}

		if err := a.reconcileSkillFilesFromImport(ctx, skillID, skillDir); err != nil {
			return err
		}
	}

	for name, s := range existingByName {
		if _, stillOnDisk := onDiskSkills[name]; stillOnDisk {
			continue
		}
		if _, err := a.DB.ExecContext(ctx, `
			UPDATE skills SET deleted_at=now(), deleted_by=$1, updated_at=now(), updated_by=$1
			WHERE id=$2 AND deleted_at IS NULL
		`, actorID, s.ID); err != nil {
			return err
		}
		if err := a.recordSkillVersion(ctx, a.DB, s.ID, "delete", name, s.Description, s.Body, s.ExtraFrontmatter, actorID); err != nil {
			return err
		}
	}
	return nil
}

// reconcileSkillFilesFromImport walks every file under skillDir except
// SKILL.md, runs each path through the same validator the upload endpoint
// uses, and upserts/deletes rows in skill_files so the table matches what's
// on disk. Binary-ness is inferred from UTF-8 validity (same as the
// skill_import.go importer).
func (a *App) reconcileSkillFilesFromImport(ctx context.Context, skillID, skillDir string) error {
	onDisk := map[string][]byte{}
	err := filepath.Walk(skillDir, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, relErr := filepath.Rel(skillDir, p)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "SKILL.md" {
			return nil
		}
		validated, vErr := validateSkillFilePath(rel)
		if vErr != nil {
			log.Printf("external git import: skipping invalid skill file path %q: %v", rel, vErr)
			return nil
		}
		data, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		if len(data) > maxSkillFileBytes {
			log.Printf("external git import: skipping oversized skill file %q (%d bytes)", rel, len(data))
			return nil
		}
		onDisk[validated] = data
		return nil
	})
	if err != nil {
		return err
	}

	existing, err := a.loadSkillFileSummaries(ctx, skillID)
	if err != nil {
		return err
	}
	existingByPath := map[string]struct{}{}
	for _, f := range existing {
		existingByPath[f.Path] = struct{}{}
	}

	for p, data := range onDisk {
		isBinary := !utf8.Valid(data)
		var contentText sql.NullString
		var contentBlob []byte
		if isBinary {
			contentBlob = data
		} else {
			contentText = sql.NullString{String: string(data), Valid: true}
		}
		if _, err := a.DB.ExecContext(ctx, `
			INSERT INTO skill_files (skill_id, path, content_text, content_blob, is_binary, size_bytes)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (skill_id, path) DO UPDATE SET
			    content_text = EXCLUDED.content_text,
			    content_blob = EXCLUDED.content_blob,
			    is_binary    = EXCLUDED.is_binary,
			    size_bytes   = EXCLUDED.size_bytes,
			    updated_at   = now()
		`, skillID, p, contentText, contentBlob, isBinary, len(data)); err != nil {
			return err
		}
	}
	for p := range existingByPath {
		if _, stillThere := onDisk[p]; stillThere {
			continue
		}
		if _, err := a.DB.ExecContext(ctx,
			`DELETE FROM skill_files WHERE skill_id=$1 AND path=$2`, skillID, p); err != nil {
			return err
		}
	}
	return nil
}

// resolveImportOwner picks the DB user that should be credited for an
// imported change. Tries to match the commit author email (case-insensitive,
// only approved users); falls back to the auto-provisioned system user.
func (a *App) resolveImportOwner(ctx context.Context, email string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	if email != "" {
		var id string
		err := a.DB.QueryRowContext(ctx,
			`SELECT id FROM users WHERE lower(email) = $1 AND status = 'approved' LIMIT 1`, email).Scan(&id)
		if err == nil {
			return id, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return "", err
		}
	}
	return a.getOrCreateImportSystemUser(ctx)
}

// getOrCreateImportSystemUser returns the id of the dedicated system user
// used as the fallback owner / actor for external-git imports. Created on
// first call; idempotent thereafter.
func (a *App) getOrCreateImportSystemUser(ctx context.Context) (string, error) {
	var id string
	err := a.DB.QueryRowContext(ctx,
		`SELECT id FROM users WHERE username = $1`, importSystemUsername).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	apiTok, err := generateAPIToken()
	if err != nil {
		return "", err
	}
	if err := a.DB.QueryRowContext(ctx, `
		INSERT INTO users (email, username, api_token, status, is_admin)
		VALUES ($1, $2, $3, 'approved', false)
		RETURNING id
	`, importSystemEmail, importSystemUsername, apiTok).Scan(&id); err != nil {
		return "", err
	}
	log.Printf("external git import: created system user %q (id=%s)", importSystemUsername, id)
	return id, nil
}
