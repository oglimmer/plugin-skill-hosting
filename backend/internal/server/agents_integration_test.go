package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestAgentLifecycle_Integration drives an agent through create, read, update
// and delete against a real Postgres, and checks after each write that the
// published git repo holds the right agents/<name>.md.
func TestAgentLifecycle_Integration(t *testing.T) {
	pool := requireTestDB(t)
	app := newIntegrationApp(t, pool)
	owner := seedUser(t, pool, "agent-owner", false)
	other := seedUser(t, pool, "agent-other", false)

	const plugin = "agent-lifecycle-plugin"
	rec := httptest.NewRecorder()
	app.handleCreatePlugin(rec, authedReq(http.MethodPost, "/api/plugins",
		`{"name":"`+plugin+`","description":"holds agents"}`, owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("create plugin status = %d; body=%s", rec.Code, readBody(rec))
	}
	var created Plugin
	_ = json.Unmarshal(rec.Body.Bytes(), &created)

	// publishedAgent reads agents/<name>.md from the tip of the bare repo, i.e.
	// what a `git clone` (and so Claude Code) would receive.
	publishedAgent := func(name string) (string, bool) {
		cmd := exec.Command("git", "show", "main:agents/"+name+".md")
		cmd.Dir = app.repoPath(plugin)
		out, err := cmd.Output()
		return string(out), err == nil
	}
	pluginVersion := func() string {
		var v string
		if err := pool.QueryRowContext(context.Background(),
			`SELECT version FROM plugins WHERE name = $1`, plugin).Scan(&v); err != nil {
			t.Fatalf("read plugin version: %v", err)
		}
		return v
	}

	// --- validation failure is a 400 and writes nothing ---
	rec = httptest.NewRecorder()
	app.handleCreateAgent(rec, authedReq(http.MethodPost, "/api/plugins/"+plugin+"/agents",
		`{"name":"worker","description":"d","body":""}`, owner, "name", plugin))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create without body status = %d, want 400; body=%s", rec.Code, readBody(rec))
	}
	if v := pluginVersion(); v != created.Version {
		t.Errorf("version moved on a rejected create: %s -> %s", created.Version, v)
	}

	// --- create (by a non-owner: any approved user may contribute) ---
	rec = httptest.NewRecorder()
	app.handleCreateAgent(rec, authedReq(http.MethodPost, "/api/plugins/"+plugin+"/agents",
		`{"name":"worker","description":"Does the work: all of it","body":"You are a worker.","model":"opus","tools":"Read, Edit"}`,
		other, "name", plugin))
	if rec.Code != http.StatusOK {
		t.Fatalf("create agent status = %d, want 200; body=%s", rec.Code, readBody(rec))
	}
	var ag Agent
	if err := json.Unmarshal(rec.Body.Bytes(), &ag); err != nil {
		t.Fatalf("decode agent: %v", err)
	}
	if ag.Name != "worker" || ag.Model != "opus" || ag.Tools != "Read, Edit" {
		t.Errorf("created agent = %+v", ag)
	}
	if ag.CreatedByName == nil || *ag.CreatedByName != other.Username {
		t.Errorf("createdByName = %v, want %s", ag.CreatedByName, other.Username)
	}
	afterCreate := pluginVersion()
	if afterCreate == created.Version {
		t.Errorf("create did not bump the plugin version (still %s)", afterCreate)
	}

	md, ok := publishedAgent("worker")
	if !ok {
		t.Fatal("agents/worker.md is not in the published repo after create")
	}
	wantMD := "---\nname: worker\ndescription: \"Does the work: all of it\"\nmodel: opus\ntools: Read, Edit\n---\n\nYou are a worker.\n"
	if md != wantMD {
		t.Errorf("published worker.md\n got: %q\nwant: %q", md, wantMD)
	}

	// --- duplicate is a 409 ---
	rec = httptest.NewRecorder()
	app.handleCreateAgent(rec, authedReq(http.MethodPost, "/api/plugins/"+plugin+"/agents",
		`{"name":"worker","description":"d","body":"b"}`, owner, "name", plugin))
	if rec.Code != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want 409; body=%s", rec.Code, readBody(rec))
	}
	if v := pluginVersion(); v != afterCreate {
		t.Errorf("version moved on a conflicting create: %s -> %s", afterCreate, v)
	}

	// --- plugin detail lists the agent ---
	rec = httptest.NewRecorder()
	app.handleGetPlugin(rec, authedReq(http.MethodGet, "/api/plugins/"+plugin, "", owner, "name", plugin))
	var detail Plugin
	_ = json.Unmarshal(rec.Body.Bytes(), &detail)
	if len(detail.Agents) != 1 || detail.Agents[0].Name != "worker" {
		t.Errorf("plugin detail agents = %+v, want [worker]", detail.Agents)
	}

	// --- update: omitted model is kept, tools "" is cleared ---
	rec = httptest.NewRecorder()
	app.handleUpdateAgent(rec, authedReq(http.MethodPut, "/api/plugins/"+plugin+"/agents/worker",
		`{"description":"new description","body":"You are a careful worker.","tools":""}`,
		owner, "name", plugin, "agent", "worker"))
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body=%s", rec.Code, readBody(rec))
	}
	var updated Agent
	_ = json.Unmarshal(rec.Body.Bytes(), &updated)
	if updated.Model != "opus" {
		t.Errorf("model = %q, want it preserved as opus", updated.Model)
	}
	if updated.Tools != "" {
		t.Errorf("tools = %q, want cleared", updated.Tools)
	}
	md, _ = publishedAgent("worker")
	if !strings.Contains(md, "description: new description\n") || strings.Contains(md, "tools:") ||
		!strings.HasSuffix(md, "You are a careful worker.\n") {
		t.Errorf("published worker.md after update: %q", md)
	}

	// --- get / list ---
	rec = httptest.NewRecorder()
	app.handleGetAgent(rec, authedReq(http.MethodGet, "/api/plugins/"+plugin+"/agents/worker", "",
		owner, "name", plugin, "agent", "worker"))
	if rec.Code != http.StatusOK {
		t.Errorf("get status = %d, want 200", rec.Code)
	}
	rec = httptest.NewRecorder()
	app.handleGetAgent(rec, authedReq(http.MethodGet, "/api/plugins/"+plugin+"/agents/nope", "",
		owner, "name", plugin, "agent", "nope"))
	if rec.Code != http.StatusNotFound {
		t.Errorf("get missing status = %d, want 404", rec.Code)
	}
	rec = httptest.NewRecorder()
	app.handleListAgents(rec, authedReq(http.MethodGet, "/api/plugins/"+plugin+"/agents", "", owner, "name", plugin))
	var list []Agent
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if len(list) != 1 {
		t.Errorf("list length = %d, want 1", len(list))
	}

	// --- delete: a stranger is refused, the creator is allowed ---
	stranger := seedUser(t, pool, "agent-stranger", false)
	rec = httptest.NewRecorder()
	app.handleDeleteAgent(rec, authedReq(http.MethodDelete, "/api/plugins/"+plugin+"/agents/worker", "",
		stranger, "name", plugin, "agent", "worker"))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("stranger delete status = %d, want 403; body=%s", rec.Code, readBody(rec))
	}
	rec = httptest.NewRecorder()
	app.handleDeleteAgent(rec, authedReq(http.MethodDelete, "/api/plugins/"+plugin+"/agents/worker", "",
		other, "name", plugin, "agent", "worker"))
	if rec.Code != http.StatusNoContent {
		t.Fatalf("creator delete status = %d, want 204; body=%s", rec.Code, readBody(rec))
	}
	if _, ok := publishedAgent("worker"); ok {
		t.Error("agents/worker.md is still published after delete")
	}
	// The work tree must not keep a stale agents/ directory either.
	if _, err := os.Stat(filepath.Join(app.workPath(plugin), "agents")); !os.IsNotExist(err) {
		t.Errorf("agents/ dir still in the work tree after the last agent was deleted (err = %v)", err)
	}
}
