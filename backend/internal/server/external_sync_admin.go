package server

import (
	"net/http"
	"sort"
)

// syncOutReport summarises a sync-out run for the admin caller.
type syncOutReport struct {
	SyncedPlugins []string          `json:"syncedPlugins"`
	Errors        map[string]string `json:"errors,omitempty"`
}

// handleAdminSyncOut iterates every active plugin in the DB and
// re-materializes it, which (when external sync is enabled) pushes the
// rendered tree into the external repo. Use when the DB is populated and
// the external repo is empty or partial — turns the existing marketplace
// into a mirror in one shot.
//
// Idempotent: re-running is safe, just slow.
func (a *App) handleAdminSyncOut(w http.ResponseWriter, r *http.Request) {
	if a.ExternalSync == nil {
		writeErr(w, http.StatusServiceUnavailable, "external git sync not configured")
		return
	}
	plugins, err := a.queryPlugins(r.Context(), `WHERE p.deleted_at IS NULL ORDER BY p.name ASC`)
	if err != nil {
		serverErr(w, r, err, "db error")
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

// emptyIfNil returns s, or a non-nil empty slice when s is nil, so JSON encodes
// it as [] rather than null for cleaner client handling.
func emptyIfNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// externalSyncStatus reports how the external mirror diverges from the DB.
// InSync is true exactly when all three drift buckets are empty. RemoteURL is
// credential-scrubbed so it is safe to surface in the admin UI.
type externalSyncStatus struct {
	RemoteURL string   `json:"remoteUrl"`
	Branch    string   `json:"branch"`
	InSync    bool     `json:"inSync"`
	Missing   []string `json:"missing"`
	OutOfDate []string `json:"outOfDate"`
	Extra     []string `json:"extra"`
}

// activePluginsByName loads every active plugin and returns both a name-keyed
// lookup and the sorted list of names — the shared setup for the status and
// reconcile handlers.
func (a *App) activePluginsByName(r *http.Request) (map[string]*Plugin, []string, error) {
	plugins, err := a.queryPlugins(r.Context(), `WHERE p.deleted_at IS NULL ORDER BY p.name ASC`)
	if err != nil {
		return nil, nil, err
	}
	byName := make(map[string]*Plugin, len(plugins))
	names := make([]string, 0, len(plugins))
	for i := range plugins {
		byName[plugins[i].Name] = &plugins[i]
		names = append(names, plugins[i].Name)
	}
	return byName, names, nil
}

// handleAdminSyncStatus reports whether the external git mirror matches the DB,
// without changing anything. Read-only counterpart to sync-out / reconcile.
func (a *App) handleAdminSyncStatus(w http.ResponseWriter, r *http.Request) {
	if a.ExternalSync == nil {
		writeErr(w, http.StatusServiceUnavailable, "external git sync not configured")
		return
	}
	byName, names, err := a.activePluginsByName(r)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	render := func(name, dir, prevDir string) error {
		return a.renderPluginInto(r.Context(), byName[name], dir, prevDir)
	}
	missing, outOfDate, extra, err := a.ExternalSync.checkPlugins(r.Context(), names, render)
	if err != nil {
		serverErr(w, r, err, "external git status")
		return
	}
	sort.Strings(missing)
	sort.Strings(outOfDate)
	sort.Strings(extra)
	writeJSON(w, http.StatusOK, externalSyncStatus{
		RemoteURL: scrubGitCredentials(a.Cfg.ExternalGitRemoteURL),
		Branch:    a.Cfg.ExternalGitBranch,
		InSync:    len(missing) == 0 && len(outOfDate) == 0 && len(extra) == 0,
		Missing:   emptyIfNil(missing),
		OutOfDate: emptyIfNil(outOfDate),
		Extra:     emptyIfNil(extra),
	})
}

// reconcileReport summarises a reconcile run: which plugins were (re)pushed to
// the mirror, which stale plugin dirs were removed, and any per-item failures.
type reconcileReport struct {
	Pushed  []string          `json:"pushed"`
	Removed []string          `json:"removed"`
	Errors  map[string]string `json:"errors,omitempty"`
}

// handleAdminSyncReconcile brings the external mirror back in line with the DB:
// it pushes the plugins that are missing or out of date and deletes the plugin
// dirs that no longer correspond to an active plugin. Unlike sync-out (which
// force-pushes everything), reconcile only touches what has drifted and also
// prunes extras. Per-item failures are collected; the run is best-effort and
// safe to repeat.
func (a *App) handleAdminSyncReconcile(w http.ResponseWriter, r *http.Request) {
	if a.ExternalSync == nil {
		writeErr(w, http.StatusServiceUnavailable, "external git sync not configured")
		return
	}
	byName, names, err := a.activePluginsByName(r)
	if err != nil {
		serverErr(w, r, err, "db error")
		return
	}
	render := func(name, dir, prevDir string) error {
		return a.renderPluginInto(r.Context(), byName[name], dir, prevDir)
	}
	missing, outOfDate, extra, err := a.ExternalSync.checkPlugins(r.Context(), names, render)
	if err != nil {
		serverErr(w, r, err, "external git status")
		return
	}

	report := reconcileReport{
		Pushed:  []string{},
		Removed: []string{},
		Errors:  map[string]string{},
	}

	needPush := append(append([]string{}, missing...), outOfDate...)
	sort.Strings(needPush)
	for _, name := range needPush {
		p := byName[name]
		if err := a.ExternalSync.pushPlugin(r.Context(), name, func(dir, prevDir string) error {
			return a.renderPluginInto(r.Context(), p, dir, prevDir)
		}); err != nil {
			report.Errors[name] = err.Error()
			continue
		}
		report.Pushed = append(report.Pushed, name)
	}

	sort.Strings(extra)
	for _, name := range extra {
		if err := a.ExternalSync.deletePlugin(r.Context(), name); err != nil {
			report.Errors[name] = err.Error()
			continue
		}
		report.Removed = append(report.Removed, name)
	}

	if len(report.Errors) == 0 {
		report.Errors = nil
	}
	writeJSON(w, http.StatusOK, report)
}
