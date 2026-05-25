package server

import (
	"net/http"
)

// syncOutReport summarises a sync-out run for the admin caller.
type syncOutReport struct {
	SyncedPlugins []string          `json:"syncedPlugins"`
	Errors        map[string]string `json:"errors,omitempty"`
}

// syncInReport summarises a sync-in run for the admin caller.
type syncInReport struct {
	ReconciledPlugins []string `json:"reconciledPlugins"`
}

// handleAdminSyncOut iterates every active plugin in the DB and
// re-materializes it, which (when external sync is enabled) pushes the
// rendered tree into the external repo. Use when the DB is populated and
// the external repo is empty or partial — turns the existing marketplace
// into a mirror in one shot.
//
// Idempotent: re-running is safe, just slow. Conservative on conflicts —
// plugins that exist only in the external repo are NOT touched by this
// endpoint (each plugin's materialize only rewrites its own subtree).
func (a *App) handleAdminSyncOut(w http.ResponseWriter, r *http.Request) {
	if a.ExternalSync == nil {
		writeErr(w, http.StatusServiceUnavailable, "external git sync not configured")
		return
	}
	plugins, err := a.queryPlugins(r.Context(), `WHERE p.deleted_at IS NULL ORDER BY p.name ASC`)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "db error")
		return
	}
	report := syncOutReport{
		SyncedPlugins: []string{},
		Errors:        map[string]string{},
	}
	for i := range plugins {
		if err := a.materializePlugin(r.Context(), &plugins[i]); err != nil {
			report.Errors[plugins[i].Name] = err.Error()
			continue
		}
		report.SyncedPlugins = append(report.SyncedPlugins, plugins[i].Name)
	}
	if len(report.Errors) == 0 {
		report.Errors = nil
	}
	writeJSON(w, http.StatusOK, report)
}

// handleAdminSyncIn reconciles every plugin currently in the external repo
// into the DB, regardless of whether it has changed since the last sync.
// Use when external is populated and the DB is empty or partial — pulls
// the existing repo into the marketplace in one shot.
//
// Idempotent: re-running re-applies the same upserts. Conservative on
// conflicts — plugins that exist only in the DB are NOT touched (they
// simply aren't in the external tree, so the reconciler never sees them).
func (a *App) handleAdminSyncIn(w http.ResponseWriter, r *http.Request) {
	if a.ExternalSync == nil {
		writeErr(w, http.StatusServiceUnavailable, "external git sync not configured")
		return
	}
	names, err := a.RunExternalBootstrap(r.Context())
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "bootstrap: "+err.Error())
		return
	}
	if names == nil {
		names = []string{}
	}
	writeJSON(w, http.StatusOK, syncInReport{ReconciledPlugins: names})
}
