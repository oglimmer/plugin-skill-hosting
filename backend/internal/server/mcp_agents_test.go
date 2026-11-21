package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// connectMCP registers every tool on a fresh server and returns a client
// session wired to it in memory. serverCtx becomes the base context of the
// tool handlers, so a user stashed in it is what userFromCtx returns — the
// same contract the HTTP token gate provides in production.
func connectMCP(t *testing.T, app *App, serverCtx context.Context) *mcp.ClientSession {
	t.Helper()
	server := mcp.NewServer(&mcp.Implementation{Name: "test", Version: "0"}, nil)
	app.registerMCPTools(server)
	clientT, serverT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(serverCtx, serverT, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0"}, nil)
	cs, err := client.Connect(context.Background(), clientT, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

// TestMCPAgentToolsRegistered checks the agent tools are exposed and that
// update_agent only requires the fields that are not "keep when omitted".
func TestMCPAgentToolsRegistered(t *testing.T) {
	cs := connectMCP(t, &App{}, context.Background())
	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	byName := map[string]*mcp.Tool{}
	for _, tool := range res.Tools {
		byName[tool.Name] = tool
	}
	for _, name := range []string{"get_agent", "create_agent", "update_agent"} {
		if byName[name] == nil {
			t.Errorf("tool %q is not registered", name)
		}
	}
	if t.Failed() {
		return
	}

	raw, err := json.Marshal(byName["update_agent"].InputSchema)
	if err != nil {
		t.Fatalf("marshal schema: %v", err)
	}
	var schema struct {
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatalf("decode schema: %v", err)
	}
	required := map[string]bool{}
	for _, r := range schema.Required {
		required[r] = true
	}
	for _, f := range []string{"plugin", "agent", "description", "body"} {
		if !required[f] {
			t.Errorf("update_agent: %q should be required; schema=%s", f, raw)
		}
	}
	for _, f := range []string{"model", "tools", "extraFrontmatter"} {
		if required[f] {
			t.Errorf("update_agent: %q must be optional; schema=%s", f, raw)
		}
		if _, ok := schema.Properties[f]; !ok {
			t.Errorf("update_agent: %q missing from schema; schema=%s", f, raw)
		}
	}
}

// TestMCPAgentTools_Integration creates, reads and updates an agent purely
// through MCP tool calls and checks the result in the published git repo.
func TestMCPAgentTools_Integration(t *testing.T) {
	pool := requireTestDB(t)
	app := newIntegrationApp(t, pool)
	owner := seedUser(t, pool, "mcp-agent-owner", false)

	const plugin = "mcp-agent-plugin"
	rec := httptest.NewRecorder()
	app.handleCreatePlugin(rec, authedReq(http.MethodPost, "/api/plugins",
		`{"name":"`+plugin+`","description":"holds agents"}`, owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("create plugin status = %d; body=%s", rec.Code, readBody(rec))
	}

	cs := connectMCP(t, app, context.WithValue(context.Background(), ctxUserKey, owner))
	call := func(tool string, args map[string]any) (string, bool) {
		t.Helper()
		res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
		if err != nil {
			t.Fatalf("%s: protocol error: %v", tool, err)
		}
		return resultText(t, res), res.IsError
	}

	// A tool error (not a crash) for invalid input.
	if text, isErr := call("create_agent", map[string]any{
		"plugin": plugin, "name": "worker", "description": "d", "body": "",
	}); !isErr || !strings.Contains(text, "body is required") {
		t.Errorf("create with empty body: isError=%v text=%q", isErr, text)
	}

	text, isErr := call("create_agent", map[string]any{
		"plugin": plugin, "name": "worker", "description": "Does the work",
		"body": "You are a worker.", "model": "claude-opus-5-5",
	})
	if isErr {
		t.Fatalf("create_agent failed: %s", text)
	}
	if !strings.Contains(text, plugin+":worker") {
		t.Errorf("create_agent result should name the agent type; got %q", text)
	}

	if text, isErr := call("create_agent", map[string]any{
		"plugin": plugin, "name": "worker", "description": "d", "body": "b",
	}); !isErr || !strings.Contains(text, "already exists") {
		t.Errorf("duplicate create: isError=%v text=%q", isErr, text)
	}

	// get_plugin lists it; get_agent returns the full definition.
	if text, _ := call("get_plugin", map[string]any{"plugin": plugin}); !strings.Contains(text, "1 agent(s)") ||
		!strings.Contains(text, `"worker"`) {
		t.Errorf("get_plugin does not list the agent: %q", text)
	}
	if text, isErr := call("get_agent", map[string]any{"plugin": plugin, "agent": "worker"}); isErr ||
		!strings.Contains(text, "You are a worker.") || !strings.Contains(text, "claude-opus-5-5") {
		t.Errorf("get_agent: isError=%v text=%q", isErr, text)
	}
	if text, isErr := call("get_agent", map[string]any{"plugin": plugin, "agent": "nope"}); !isErr ||
		!strings.Contains(text, "not found") {
		t.Errorf("get_agent on a missing agent: isError=%v text=%q", isErr, text)
	}

	// update_agent without model keeps the stored model.
	if text, isErr := call("update_agent", map[string]any{
		"plugin": plugin, "agent": "worker", "description": "Does all the work",
		"body": "You are a careful worker.", "tools": "Read, Edit",
	}); isErr {
		t.Fatalf("update_agent failed: %s", text)
	}

	cmd := exec.Command("git", "show", "main:agents/worker.md")
	cmd.Dir = app.repoPath(plugin)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("agents/worker.md not published: %v", err)
	}
	want := "---\nname: worker\ndescription: Does all the work\nmodel: claude-opus-5-5\ntools: Read, Edit\n---\n\nYou are a careful worker.\n"
	if string(out) != want {
		t.Errorf("published worker.md\n got: %q\nwant: %q", out, want)
	}
}
