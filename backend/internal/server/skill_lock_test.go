package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

// lockSkill calls handleLockSkill as the given (admin) user and returns the
// recorder. The router gates this on admin; the handler itself doesn't, so
// tests drive it directly with an admin user.
func lockSkill(t *testing.T, app *App, admin *User, plugin, skill, reason string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleLockSkill(rec, authedReq(http.MethodPost,
		"/api/plugins/"+plugin+"/skills/"+skill+"/lock",
		`{"reason":"`+reason+`"}`, admin, "name", plugin, "skill", skill))
	return rec
}

func unlockSkill(t *testing.T, app *App, admin *User, plugin, skill string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleUnlockSkill(rec, authedReq(http.MethodDelete,
		"/api/plugins/"+plugin+"/skills/"+skill+"/lock",
		"", admin, "name", plugin, "skill", skill))
	return rec
}

// renderedSkillExists reports whether renderPluginInto writes a SKILL.md for the
// named skill — i.e. whether the skill is part of the git tree. Locked skills
// must not appear.
func renderedSkillExists(t *testing.T, app *App, plugin, skill string) bool {
	t.Helper()
	ctx := context.Background()
	p, err := app.loadPluginByName(ctx, plugin)
	if err != nil {
		t.Fatalf("load plugin %s: %v", plugin, err)
	}
	dir := t.TempDir()
	if err := app.renderPluginInto(ctx, p, dir); err != nil {
		t.Fatalf("render plugin %s: %v", plugin, err)
	}
	_, err = os.Stat(filepath.Join(dir, "skills", skill, "SKILL.md"))
	return err == nil
}

// TestSkillLock_Integration covers the full admin lock/unlock lifecycle: a
// locked skill stays visible in the REST view but is withdrawn from git and
// MCP and rejects mutations; unlocking restores all three.
func TestSkillLock_Integration(t *testing.T) {
	pool := requireTestDB(t)
	app := newIntegrationApp(t, pool)
	admin := seedUser(t, pool, "lock-admin", true)

	createPluginForMove(t, app, admin, "lock-plug")
	skill := createSkillForMove(t, app, admin, "lock-plug", "guarded")

	// Baseline: present in git and resolvable over MCP.
	if !renderedSkillExists(t, app, "lock-plug", "guarded") {
		t.Fatalf("skill missing from git before lock")
	}
	if _, err := app.resolveSkill(context.Background(), skill.PluginID, "guarded"); err != nil {
		t.Fatalf("resolveSkill before lock: %v", err)
	}

	// --- lock ---
	rec := lockSkill(t, app, admin, "lock-plug", "guarded", "under review")
	if rec.Code != http.StatusOK {
		t.Fatalf("lock status = %d, want 200; body=%s", rec.Code, readBody(rec))
	}
	var locked Skill
	if err := json.Unmarshal(rec.Body.Bytes(), &locked); err != nil {
		t.Fatalf("decode locked skill: %v", err)
	}
	if !locked.Locked {
		t.Errorf("locked skill not flagged locked")
	}
	if locked.LockSource == nil || *locked.LockSource != "admin" {
		t.Errorf("lock source = %v, want admin", locked.LockSource)
	}
	if locked.LockReason != "under review" {
		t.Errorf("lock reason = %q, want %q", locked.LockReason, "under review")
	}

	// Still visible in the web UI (REST), flagged locked.
	if !pluginHasActiveSkill(t, app, "lock-plug", "guarded") {
		t.Errorf("locked skill vanished from REST plugin view")
	}
	// Withdrawn from git.
	if renderedSkillExists(t, app, "lock-plug", "guarded") {
		t.Errorf("locked skill still present in git tree")
	}
	// Withdrawn from MCP (looks like it doesn't exist).
	if _, err := app.resolveSkill(context.Background(), skill.PluginID, "guarded"); err == nil {
		t.Errorf("locked skill still resolvable over MCP")
	}

	// Mutation is rejected with 403 while locked.
	urec := httptest.NewRecorder()
	app.handleUpdateSkill(urec, authedReq(http.MethodPut,
		"/api/plugins/lock-plug/skills/guarded",
		`{"description":"new","body":"x"}`, admin, "name", "lock-plug", "skill", "guarded"))
	if urec.Code != http.StatusForbidden {
		t.Errorf("update of locked skill status = %d, want 403; body=%s", urec.Code, readBody(urec))
	}

	// --- unlock ---
	if rec := unlockSkill(t, app, admin, "lock-plug", "guarded"); rec.Code != http.StatusOK {
		t.Fatalf("unlock status = %d, want 200; body=%s", rec.Code, readBody(rec))
	}
	if renderedSkillExists(t, app, "lock-plug", "guarded") != true {
		t.Errorf("skill missing from git after unlock")
	}
	if _, err := app.resolveSkill(context.Background(), skill.PluginID, "guarded"); err != nil {
		t.Errorf("resolveSkill after unlock: %v", err)
	}

	// Unlocking a skill that isn't locked is a 409.
	if rec := unlockSkill(t, app, admin, "lock-plug", "guarded"); rec.Code != http.StatusConflict {
		t.Errorf("double-unlock status = %d, want 409; body=%s", rec.Code, readBody(rec))
	}
}

// deleteSkill calls handleDeleteSkill as the given user and returns the
// recorder. The route isn't admin-gated, so the handler decides whether a
// locked skill may be removed based on the caller's admin flag.
func deleteSkill(t *testing.T, app *App, user *User, plugin, skill string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	app.handleDeleteSkill(rec, authedReq(http.MethodDelete,
		"/api/plugins/"+plugin+"/skills/"+skill,
		"", user, "name", plugin, "skill", skill))
	return rec
}

// TestDeleteLockedSkill_AdminOnly verifies that a locked skill can be deleted by
// an admin but not by a non-admin: deletion is removal (it can't republish the
// withdrawn content), so it's the one mutation a lock doesn't block for admins.
func TestDeleteLockedSkill_AdminOnly(t *testing.T) {
	pool := requireTestDB(t)
	app := newIntegrationApp(t, pool)
	admin := seedUser(t, pool, "lockdel-admin", true)
	member := seedUser(t, pool, "lockdel-member", false)

	createPluginForMove(t, app, admin, "lockdel-plug")
	createSkillForMove(t, app, admin, "lockdel-plug", "lockdel-doomed")
	if rec := lockSkill(t, app, admin, "lockdel-plug", "lockdel-doomed", "under review"); rec.Code != http.StatusOK {
		t.Fatalf("lock status = %d, want 200; body=%s", rec.Code, readBody(rec))
	}

	// A non-admin is refused with 403 and the skill survives.
	if rec := deleteSkill(t, app, member, "lockdel-plug", "lockdel-doomed"); rec.Code != http.StatusForbidden {
		t.Fatalf("non-admin delete of locked skill = %d, want 403; body=%s", rec.Code, readBody(rec))
	}
	if !pluginHasActiveSkill(t, app, "lockdel-plug", "lockdel-doomed") {
		t.Fatalf("locked skill was deleted by a non-admin")
	}

	// An admin deletes it outright.
	if rec := deleteSkill(t, app, admin, "lockdel-plug", "lockdel-doomed"); rec.Code != http.StatusNoContent {
		t.Fatalf("admin delete of locked skill = %d, want 204; body=%s", rec.Code, readBody(rec))
	}
	if pluginHasActiveSkill(t, app, "lockdel-plug", "lockdel-doomed") {
		t.Errorf("locked skill still active after admin delete")
	}
}

// TestAutoLockSuppression_Integration verifies the audit auto-lock and the
// "admin unlock suppresses re-lock until the skill is edited" rule.
func TestAutoLockSuppression_Integration(t *testing.T) {
	pool := requireTestDB(t)
	app := newIntegrationApp(t, pool)
	admin := seedUser(t, pool, "autolock-admin", true)

	createPluginForMove(t, app, admin, "autolock-plug")
	skill := createSkillForMove(t, app, admin, "autolock-plug", "risky")
	ctx := context.Background()

	// Audit auto-locks an over-threshold skill.
	locked, err := app.autoLockSkill(ctx, skill.ID, "exfiltrates data")
	if err != nil {
		t.Fatalf("autoLockSkill: %v", err)
	}
	if !locked {
		t.Fatalf("autoLockSkill did not lock a fresh skill")
	}
	got, err := app.loadSkillByID(ctx, skill.ID)
	if err != nil {
		t.Fatalf("load skill: %v", err)
	}
	if !got.Locked || got.LockSource == nil || *got.LockSource != "audit" {
		t.Fatalf("after auto-lock: locked=%v source=%v, want locked audit", got.Locked, got.LockSource)
	}

	// Admin unlocks the audit lock — this acknowledges it.
	if rec := unlockSkill(t, app, admin, "autolock-plug", "risky"); rec.Code != http.StatusOK {
		t.Fatalf("unlock status = %d, want 200; body=%s", rec.Code, readBody(rec))
	}

	// A later audit run must NOT re-lock it (suppressed).
	relocked, err := app.autoLockSkill(ctx, skill.ID, "still risky")
	if err != nil {
		t.Fatalf("autoLockSkill (suppressed): %v", err)
	}
	if relocked {
		t.Errorf("auto-lock re-locked an admin-acknowledged skill")
	}

	// Editing the skill clears the suppression, so the audit owns it again.
	urec := httptest.NewRecorder()
	app.handleUpdateSkill(urec, authedReq(http.MethodPut,
		"/api/plugins/autolock-plug/skills/risky",
		`{"description":"d","body":"reworked"}`, admin, "name", "autolock-plug", "skill", "risky"))
	if urec.Code != http.StatusNoContent {
		t.Fatalf("update status = %d, want 204; body=%s", urec.Code, readBody(urec))
	}
	relocked, err = app.autoLockSkill(ctx, skill.ID, "risky again")
	if err != nil {
		t.Fatalf("autoLockSkill (after edit): %v", err)
	}
	if !relocked {
		t.Errorf("auto-lock did not re-lock after the skill was edited")
	}
}
