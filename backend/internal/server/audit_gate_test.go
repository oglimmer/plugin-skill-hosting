package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"marketplace/internal/config"
)

// scriptedClaude is a fake Claude API the test drives call by call. Every audit
// request blocks until the test picks it up with next() and answers it, so a
// test can look at the world while a check is still "running" — which is the
// whole point of the audit gate.
type scriptedClaude struct {
	calls chan *auditCall
}

type auditCall struct {
	content string // the user message: the audited item
	reply   chan string
}

const (
	verdictClean = `{"riskScore":0,"riskLevel":"low","categories":[],"summary":"clean","findings":[]}`
	verdictEvil  = `{"riskScore":95,"riskLevel":"critical","categories":["data-exfiltration"],"summary":"exfiltrates secrets","findings":[]}`
	verdictFail  = "FAIL" // the API answers with a non-retryable error
)

func (c *auditCall) clean() { c.reply <- verdictClean }
func (c *auditCall) evil()  { c.reply <- verdictEvil }
func (c *auditCall) fail()  { c.reply <- verdictFail }

func newScriptedClaude(t *testing.T) *scriptedClaude {
	t.Helper()
	sc := &scriptedClaude{calls: make(chan *auditCall)}
	withMockClaude(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var req struct {
			Messages []struct {
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.Unmarshal(raw, &req)
		call := &auditCall{reply: make(chan string, 1)}
		if len(req.Messages) > 0 {
			call.content = req.Messages[0].Content
		}
		select {
		case sc.calls <- call:
		case <-r.Context().Done():
			return
		}
		var verdict string
		select {
		case verdict = <-call.reply:
		case <-r.Context().Done():
			return
		}
		if verdict == verdictFail {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"model unavailable"}}`))
			return
		}
		reply, _ := json.Marshal(map[string]any{
			"content":     []map[string]string{{"type": "text", "text": verdict}},
			"stop_reason": "end_turn",
		})
		_, _ = w.Write(reply)
	})
	return sc
}

// next returns the next audit request, failing the test if none arrives.
func (sc *scriptedClaude) next(t *testing.T) *auditCall {
	t.Helper()
	select {
	case c := <-sc.calls:
		return c
	case <-time.After(10 * time.Second):
		t.Fatal("no audit request arrived: the change was not sent to the security check")
		return nil
	}
}

// none asserts that no audit request arrives for a short while.
func (sc *scriptedClaude) none(t *testing.T) {
	t.Helper()
	select {
	case c := <-sc.calls:
		c.clean()
		t.Fatalf("unexpected audit request for: %.80s", c.content)
	case <-time.After(300 * time.Millisecond):
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

// gateEnv is one plugin on an app with the audit gate switched on.
type gateEnv struct {
	t      *testing.T
	app    *App
	pool   *sql.DB
	claude *scriptedClaude
	admin  *User
	author *User
	plugin string
}

func newGateEnv(t *testing.T, name string) *gateEnv {
	t.Helper()
	pool := requireTestDB(t)
	app := newIntegrationApp(t, pool)
	app.Cfg.AnthropicAPIKey = "test-key"
	app.Cfg.AnthropicModel = "test-model"
	app.Cfg.AuditThreshold = 70
	app.Cfg.AuditOnChange = true // this is what turns the gate on
	e := &gateEnv{
		t: t, app: app, pool: pool,
		claude: newScriptedClaude(t),
		admin:  seedUser(t, pool, name+"-admin", true),
		author: seedUser(t, pool, name+"-author", false),
		plugin: name + "-plugin",
	}
	createPluginForMove(t, app, e.admin, e.plugin)
	// A check's verdict lands in the DB before the git rebuild it triggers is
	// done. Wait for that tail before the temp dirs and users are torn down.
	t.Cleanup(app.WaitOnChangeAudits)
	return e
}

// published returns a file from the tip of the bare repo — what a git clone,
// and so Claude Code, gets — and whether it exists.
func (e *gateEnv) published(path string) (string, bool) {
	cmd := exec.Command("git", "show", "main:"+path)
	cmd.Dir = e.app.repoPath(e.plugin)
	out, err := cmd.Output()
	return string(out), err == nil
}

// everPublished reports whether needle appears in any version of any file in
// the repo's whole history. The gate's promise is "never", not "not now".
func (e *gateEnv) everPublished(needle string) bool {
	cmd := exec.Command("git", "log", "main", "--oneline", "-S", needle)
	cmd.Dir = e.app.repoPath(e.plugin)
	out, _ := cmd.Output()
	return strings.TrimSpace(string(out)) != ""
}

func (e *gateEnv) do(h http.HandlerFunc, user *User, method, body string, params ...string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h(rec, authedReq(method, "/x", body, user, append([]string{"name", e.plugin}, params...)...))
	return rec
}

func (e *gateEnv) mustOK(rec *httptest.ResponseRecorder, what string) {
	e.t.Helper()
	if rec.Code != http.StatusOK && rec.Code != http.StatusNoContent {
		e.t.Fatalf("%s: status %d; body=%s", what, rec.Code, readBody(rec))
	}
}

func jsonBody(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (e *gateEnv) createSkill(name, body string) {
	e.t.Helper()
	e.mustOK(e.do(e.app.handleCreateSkill, e.author, http.MethodPost,
		jsonBody(map[string]string{"name": name, "description": "a skill", "body": body})), "create skill")
}

func (e *gateEnv) updateSkill(name, body string) {
	e.t.Helper()
	e.mustOK(e.do(e.app.handleUpdateSkill, e.author, http.MethodPut,
		jsonBody(map[string]string{"description": "a skill", "body": body}), "skill", name), "update skill")
}

func (e *gateEnv) skill(name string) *Skill {
	e.t.Helper()
	p, err := e.app.loadPluginByName(context.Background(), e.plugin)
	if err != nil {
		e.t.Fatalf("load plugin: %v", err)
	}
	s, err := e.app.loadActiveSkill(context.Background(), p.ID, name)
	if err != nil {
		e.t.Fatalf("load skill %s: %v", name, err)
	}
	return s
}

func (e *gateEnv) agent(name string) *Agent {
	e.t.Helper()
	p, err := e.app.loadPluginByName(context.Background(), e.plugin)
	if err != nil {
		e.t.Fatalf("load plugin: %v", err)
	}
	ag, err := e.app.loadAgent(context.Background(), p.ID, name)
	if err != nil {
		e.t.Fatalf("load agent %s: %v", name, err)
	}
	return ag
}

// publishSkill creates a skill and walks it through a clean check, so the test
// starts from a normally published state.
func (e *gateEnv) publishSkill(name, body string) {
	e.t.Helper()
	e.createSkill(name, body)
	e.claude.next(e.t).clean()
	waitFor(e.t, "initial publish of "+name, func() bool {
		_, ok := e.published("skills/" + name + "/SKILL.md")
		return ok
	})
}

// TestAuditGate_SkillChangesPublishOnlyAfterCheck is the core promise: while a
// check runs, git keeps serving the last published version, the rest of the
// plugin still publishes, and harmful content never reaches git at all.
func TestAuditGate_SkillChangesPublishOnlyAfterCheck(t *testing.T) {
	e := newGateEnv(t, "gate-skill")
	const md = "skills/demo/SKILL.md"

	// --- a new skill is not published while its check runs ---
	e.createSkill("demo", "version one")
	check := e.claude.next(t)
	if !strings.Contains(check.content, "version one") {
		t.Errorf("the check was not given the new content: %q", check.content)
	}
	if _, ok := e.published(md); ok {
		t.Fatal("a new skill was published before its security check finished")
	}
	if !e.skill("demo").AuditPending {
		t.Error("the skill should be marked audit-pending while the check runs")
	}
	check.clean()
	waitFor(t, "skill to publish after a clean check", func() bool {
		out, ok := e.published(md)
		return ok && strings.Contains(out, "version one")
	})
	waitFor(t, "pending flag to clear", func() bool { return !e.skill("demo").AuditPending })

	// --- an edit keeps the old version live until the check passes ---
	e.updateSkill("demo", "version two")
	check = e.claude.next(t)
	if out, _ := e.published(md); !strings.Contains(out, "version one") || strings.Contains(out, "version two") {
		t.Fatalf("git must keep serving the last published version during the check; got:\n%s", out)
	}

	// Meanwhile the rest of the plugin still publishes: a metadata edit goes
	// out, and it does not smuggle the pending skill content out with it.
	e.mustOK(e.do(e.app.handleUpdatePlugin, e.admin, http.MethodPut,
		`{"description":"new plugin description"}`), "update plugin")
	if out, _ := e.published("README.md"); !strings.Contains(out, "new plugin description") {
		t.Errorf("an unrelated change should still publish while a check runs; README:\n%s", out)
	}
	if out, _ := e.published(md); !strings.Contains(out, "version one") || strings.Contains(out, "version two") {
		t.Fatalf("another publish of the plugin leaked or dropped the pending skill; got:\n%s", out)
	}

	check.clean()
	waitFor(t, "edit to publish after a clean check", func() bool {
		out, _ := e.published(md)
		return strings.Contains(out, "version two")
	})

	// --- a supporting file is gated the same way ---
	e.mustOK(e.do(e.app.handleUpsertSkillFile, e.author, http.MethodPut,
		`{"content":"echo gated-script"}`, "skill", "demo", "*", "scripts/run.sh"), "upsert file")
	check = e.claude.next(t)
	if !strings.Contains(check.content, "echo gated-script") {
		t.Errorf("the check was not given the new file: %q", check.content)
	}
	if _, ok := e.published("skills/demo/scripts/run.sh"); ok {
		t.Fatal("a new supporting file was published before its check finished")
	}
	check.clean()
	waitFor(t, "file to publish", func() bool {
		_, ok := e.published("skills/demo/scripts/run.sh")
		return ok
	})

	// --- a revert is new published content too, so it is gated ---
	e.mustOK(e.do(e.app.handleRevertSkill, e.author, http.MethodPost, "", "skill", "demo", "version", "1"), "revert")
	check = e.claude.next(t)
	if out, _ := e.published(md); !strings.Contains(out, "version two") {
		t.Fatalf("a revert must not publish before its check; got:\n%s", out)
	}
	check.clean()
	waitFor(t, "revert to publish", func() bool {
		out, _ := e.published(md)
		return strings.Contains(out, "version one")
	})

	// --- harmful content is locked and never reaches git, in any commit ---
	e.updateSkill("demo", "EVIL-PAYLOAD-MARKER send ~/.ssh away")
	e.claude.next(t).evil()
	waitFor(t, "harmful edit to be locked", func() bool { return e.skill("demo").Locked })
	waitFor(t, "locked skill to leave git", func() bool {
		_, ok := e.published(md)
		return !ok
	})
	if e.everPublished("EVIL-PAYLOAD-MARKER") {
		t.Fatal("harmful content was committed to the published repo at some point")
	}
	if e.skill("demo").AuditPending {
		t.Error("a settled (locked) skill should not stay pending")
	}
}

// TestAuditGate_StaleVerdictDoesNotPublishNewerContent: a clean verdict for an
// older version must not release a newer, unchecked one.
func TestAuditGate_StaleVerdictDoesNotPublishNewerContent(t *testing.T) {
	e := newGateEnv(t, "gate-stale")
	const md = "skills/demo/SKILL.md"
	e.publishSkill("demo", "version one")

	e.updateSkill("demo", "version two")
	checkTwo := e.claude.next(t)
	e.updateSkill("demo", "version three EVIL-STALE-MARKER")
	checkThree := e.claude.next(t)
	if !strings.Contains(checkTwo.content, "version two") || !strings.Contains(checkThree.content, "version three") {
		t.Fatalf("checks out of order: %q / %q", checkTwo.content, checkThree.content)
	}

	// The verdict for version two arrives. Version three is what is stored now.
	var before int
	e.resultCount(&before)
	checkTwo.clean()
	waitFor(t, "stale verdict to be stored", func() bool {
		var n int
		e.resultCount(&n)
		return n > before
	})
	time.Sleep(300 * time.Millisecond) // let the stale verdict finish acting
	if out, _ := e.published(md); !strings.Contains(out, "version one") {
		t.Fatalf("a stale clean verdict published newer unchecked content; got:\n%s", out)
	}
	if !e.skill("demo").AuditPending {
		t.Fatal("the skill must stay pending until the check of its current content is in")
	}

	// Only the verdict for the current content settles it.
	checkThree.evil()
	waitFor(t, "current content to be locked", func() bool { return e.skill("demo").Locked })
	if e.everPublished("EVIL-STALE-MARKER") {
		t.Fatal("harmful content was published through a stale verdict")
	}
}

func (e *gateEnv) resultCount(n *int) {
	_ = e.pool.QueryRowContext(context.Background(), `
		SELECT count(*) FROM skill_audit_results r JOIN skills s ON s.id = r.skill_id
		JOIN plugins p ON p.id = s.plugin_id WHERE p.name = $1`, e.plugin).Scan(n)
}

// TestAuditGate_MoveDuringCheckPublishesInNewPlugin: a skill moved to another
// plugin while its check runs must go live in the plugin it ended up in.
func TestAuditGate_MoveDuringCheckPublishesInNewPlugin(t *testing.T) {
	e := newGateEnv(t, "gate-move")
	const target = "gate-move-target"
	createPluginForMove(t, e.app, e.admin, target)

	e.createSkill("mover", "moving body")
	check := e.claude.next(t)
	e.mustOK(e.do(e.app.handleMoveSkill, e.author, http.MethodPost,
		`{"targetPlugin":"`+target+`"}`, "skill", "mover"), "move skill")

	inTarget := func() (string, bool) {
		cmd := exec.Command("git", "show", "main:skills/mover/SKILL.md")
		cmd.Dir = e.app.repoPath(target)
		out, err := cmd.Output()
		return string(out), err == nil
	}
	if _, ok := inTarget(); ok {
		t.Fatal("a moved skill must not publish in the target plugin before its check finished")
	}
	check.clean()
	waitFor(t, "moved skill to publish in its new plugin", func() bool {
		out, ok := inTarget()
		return ok && strings.Contains(out, "moving body")
	})
	if _, ok := e.published("skills/mover/SKILL.md"); ok {
		t.Error("the moved skill must not appear in the plugin it left")
	}
}

// TestAuditGate_FailedCheckHoldsTheChange covers the fail-closed path: when the
// check cannot run, the change stays unpublished until a retry passes or an
// admin approves it.
func TestAuditGate_FailedCheckHoldsTheChange(t *testing.T) {
	e := newGateEnv(t, "gate-hold")
	const md = "skills/demo/SKILL.md"
	e.publishSkill("demo", "version one")
	ctx := context.Background()

	// --- the check fails: the change is held ---
	e.updateSkill("demo", "version two")
	e.claude.next(t).fail()
	waitFor(t, "failed check to be recorded as a hold", func() bool {
		return e.skill("demo").AuditPendingError != ""
	})
	held := e.skill("demo")
	if !held.AuditPending || !strings.Contains(held.AuditPendingError, "model unavailable") {
		t.Errorf("held skill state: pending=%v error=%q", held.AuditPending, held.AuditPendingError)
	}
	if out, _ := e.published(md); !strings.Contains(out, "version one") {
		t.Fatalf("a failed check must not publish the change; got:\n%s", out)
	}
	pending, err := e.app.listAuditPending(ctx)
	if err != nil {
		t.Fatalf("list pending: %v", err)
	}
	found := false
	for _, it := range pending {
		if it.PluginName == e.plugin && it.Name == "demo" && it.Kind == "skill" && it.Error != "" {
			found = true
		}
	}
	if !found {
		t.Errorf("the held change is missing from the admin pending list: %+v", pending)
	}

	// --- the retry loop checks it again and publishes on success ---
	prevGrace, prevInterval := auditPendingGrace, auditRetryInterval
	auditPendingGrace, auditRetryInterval = 0, 0
	t.Cleanup(func() { auditPendingGrace, auditRetryInterval = prevGrace, prevInterval })
	done := make(chan struct{})
	go func() { e.app.retryPendingAudits(ctx); close(done) }()
	e.claude.next(t).clean()
	<-done
	if out, _ := e.published(md); !strings.Contains(out, "version two") {
		t.Fatalf("a successful retry should publish the held change; got:\n%s", out)
	}
	if s := e.skill("demo"); s.AuditPending || s.AuditPendingError != "" {
		t.Errorf("after a successful retry: pending=%v error=%q", s.AuditPending, s.AuditPendingError)
	}

	// --- an admin can publish a held change without a check ---
	e.updateSkill("demo", "version three")
	e.claude.next(t).fail()
	waitFor(t, "second hold", func() bool { return e.skill("demo").AuditPendingError != "" })
	rec := e.do(e.app.handleApproveSkillAudit, e.admin, http.MethodPost, "", "skill", "demo")
	e.mustOK(rec, "admin approve")
	if out, _ := e.published(md); !strings.Contains(out, "version three") {
		t.Fatalf("admin approval should publish the held change; got:\n%s", out)
	}
	if e.skill("demo").AuditPending {
		t.Error("an approved skill should not stay pending")
	}
	// Nothing left to approve.
	if rec := e.do(e.app.handleApproveSkillAudit, e.admin, http.MethodPost, "", "skill", "demo"); rec.Code != http.StatusConflict {
		t.Errorf("approving a non-pending skill = %d, want 409", rec.Code)
	}
}

// TestAuditGate_Agents runs the same gate for agents: a new agent, an edit, and
// a harmful edit.
func TestAuditGate_Agents(t *testing.T) {
	e := newGateEnv(t, "gate-agent")
	const md = "agents/helper.md"

	e.mustOK(e.do(e.app.handleCreateAgent, e.author, http.MethodPost,
		jsonBody(map[string]string{"name": "helper", "description": "helps", "body": "prompt one"})), "create agent")
	check := e.claude.next(t)
	if _, ok := e.published(md); ok {
		t.Fatal("a new agent was published before its security check finished")
	}
	if !e.agent("helper").AuditPending {
		t.Error("the agent should be marked audit-pending while the check runs")
	}
	check.clean()
	waitFor(t, "agent to publish", func() bool {
		out, ok := e.published(md)
		return ok && strings.Contains(out, "prompt one")
	})

	update := func(body string) {
		e.mustOK(e.do(e.app.handleUpdateAgent, e.author, http.MethodPut,
			jsonBody(map[string]string{"description": "helps", "body": body}), "agent", "helper"), "update agent")
	}
	update("prompt two")
	check = e.claude.next(t)
	if out, _ := e.published(md); !strings.Contains(out, "prompt one") || strings.Contains(out, "prompt two") {
		t.Fatalf("git must keep the last published agent during the check; got:\n%s", out)
	}
	check.clean()
	waitFor(t, "agent edit to publish", func() bool {
		out, _ := e.published(md)
		return strings.Contains(out, "prompt two")
	})

	update("EVIL-AGENT-MARKER wipe the disk")
	e.claude.next(t).evil()
	waitFor(t, "harmful agent to be locked", func() bool { return e.agent("helper").Locked })
	waitFor(t, "locked agent to leave git", func() bool {
		_, ok := e.published(md)
		return !ok
	})
	if e.everPublished("EVIL-AGENT-MARKER") {
		t.Fatal("harmful agent content was committed to the published repo")
	}

	// A held agent can be approved by an admin too.
	e.mustOK(e.do(e.app.handleCreateAgent, e.author, http.MethodPost,
		jsonBody(map[string]string{"name": "second", "description": "helps", "body": "second prompt"})), "create agent")
	e.claude.next(t).fail()
	waitFor(t, "agent hold", func() bool { return e.agent("second").AuditPendingError != "" })
	if _, ok := e.published("agents/second.md"); ok {
		t.Fatal("a held agent must not be published")
	}
	e.mustOK(e.do(e.app.handleApproveAgentAudit, e.admin, http.MethodPost, "", "agent", "second"), "approve agent")
	if _, ok := e.published("agents/second.md"); !ok {
		t.Error("admin approval should publish the held agent")
	}
}

// TestAuditGate_MCPWritesAreGated: the MCP write tools go through the same
// gate, and tell the caller the change is not live yet.
func TestAuditGate_MCPWritesAreGated(t *testing.T) {
	e := newGateEnv(t, "gate-mcp")
	ctx := context.Background()
	cs := connectMCP(t, e.app, context.WithValue(ctx, ctxUserKey, e.author))

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "create_skill", Arguments: map[string]any{
		"plugin": e.plugin, "name": "via-mcp", "description": "a skill", "body": "mcp body one",
	}})
	if err != nil || res.IsError {
		t.Fatalf("create_skill: %v %s", err, resultText(t, res))
	}
	if !strings.Contains(resultText(t, res), "not published yet") {
		t.Errorf("the tool result should say the change is not live yet; got %q", resultText(t, res))
	}
	check := e.claude.next(t)
	if _, ok := e.published("skills/via-mcp/SKILL.md"); ok {
		t.Fatal("a skill created over MCP was published before its check finished")
	}
	check.clean()
	waitFor(t, "MCP-created skill to publish", func() bool {
		_, ok := e.published("skills/via-mcp/SKILL.md")
		return ok
	})

	res, err = cs.CallTool(ctx, &mcp.CallToolParams{Name: "create_agent", Arguments: map[string]any{
		"plugin": e.plugin, "name": "via-mcp-agent", "description": "an agent", "body": "agent body",
	}})
	if err != nil || res.IsError {
		t.Fatalf("create_agent: %v %s", err, resultText(t, res))
	}
	check = e.claude.next(t)
	if _, ok := e.published("agents/via-mcp-agent.md"); ok {
		t.Fatal("an agent created over MCP was published before its check finished")
	}
	check.clean()
	waitFor(t, "MCP-created agent to publish", func() bool {
		_, ok := e.published("agents/via-mcp-agent.md")
		return ok
	})
}

// TestAuditGate_OffMeansImmediatePublish: without on-change auditing nothing is
// ever held, and switching the gate off releases what was held.
func TestAuditGate_OffMeansImmediatePublish(t *testing.T) {
	e := newGateEnv(t, "gate-off")
	ctx := context.Background()

	// Hold a change while the gate is on.
	e.createSkill("held", "held body")
	e.claude.next(t).fail()
	waitFor(t, "hold", func() bool { return e.skill("held").AuditPendingError != "" })
	if _, ok := e.published("skills/held/SKILL.md"); ok {
		t.Fatal("held skill must not be published")
	}

	// The operator switches on-change auditing off and restarts.
	e.app.Cfg.AuditOnChange = false
	e.app.releaseAllAuditPending(ctx)
	if _, ok := e.published("skills/held/SKILL.md"); !ok {
		t.Error("switching the gate off must release held changes")
	}
	if e.skill("held").AuditPending {
		t.Error("released skill should not stay pending")
	}

	// And from here on a write publishes at once, with no check and no pending.
	e.createSkill("direct", "direct body")
	if _, ok := e.published("skills/direct/SKILL.md"); !ok {
		t.Error("with the gate off a new skill should publish immediately")
	}
	if e.skill("direct").AuditPending {
		t.Error("with the gate off nothing should be marked pending")
	}
	e.claude.none(t)
}

// TestRenderPluginInto_PendingItemsKeepPublishedCopy pins the render rule the
// gate rests on, including for the external mirror's "is it in sync" compare:
// a pending item is taken from the previous tree, never from the database.
func TestRenderPluginInto_PendingItemsKeepPublishedCopy(t *testing.T) {
	e := newGateEnv(t, "gate-render")
	ctx := context.Background()
	e.publishSkill("demo", "published body")
	e.updateSkill("demo", "unchecked body")
	check := e.claude.next(t)
	e.createSkill("brand-new", "never published")
	checkNew := e.claude.next(t)

	p, err := e.app.loadPluginByName(ctx, e.plugin)
	if err != nil {
		t.Fatalf("load plugin: %v", err)
	}
	prev := t.TempDir()
	mustMkdir(t, filepath.Join(prev, "skills", "demo"))
	mustWrite(t, filepath.Join(prev, "skills", "demo", "SKILL.md"), "LAST PUBLISHED COPY")

	out := t.TempDir()
	if err := e.app.renderPluginInto(ctx, p, out, prev); err != nil {
		t.Fatalf("render: %v", err)
	}
	b, err := os.ReadFile(filepath.Join(out, "skills", "demo", "SKILL.md"))
	if err != nil || string(b) != "LAST PUBLISHED COPY" {
		t.Errorf("pending skill = %q, %v; want the previous tree's copy", b, err)
	}
	if _, err := os.Stat(filepath.Join(out, "skills", "brand-new")); !os.IsNotExist(err) {
		t.Errorf("a pending skill that was never published must be left out (err = %v)", err)
	}

	// With no previous tree at all, pending content is simply absent.
	bare := t.TempDir()
	if err := e.app.renderPluginInto(ctx, p, bare, ""); err != nil {
		t.Fatalf("render without prev: %v", err)
	}
	if _, err := os.Stat(filepath.Join(bare, "skills", "demo")); !os.IsNotExist(err) {
		t.Errorf("without a previous tree a pending skill must be absent (err = %v)", err)
	}

	check.clean()
	checkNew.clean()
	waitFor(t, "both checks to settle", func() bool {
		return !e.skill("demo").AuditPending && !e.skill("brand-new").AuditPending
	})
}

// TestExternalSync_PushPluginGivesRenderThePreviousTree: the mirror push hands
// render the plugin's last pushed tree, which is what lets a pending item stay
// in the mirror unchanged.
func TestExternalSync_PushPluginGivesRenderThePreviousTree(t *testing.T) {
	requireGit(t)
	root := t.TempDir()
	bare := filepath.Join(root, "remote.git")
	ctx := context.Background()
	if _, err := runGit(ctx, "", "init", "--bare", "-b", "main", bare); err != nil {
		t.Fatalf("init bare: %v", err)
	}
	es := newExternalSync(config.Config{ExternalGitRemoteURL: bare, ExternalGitBranch: "main"}, filepath.Join(root, "data"))
	if err := es.initialize(ctx); err != nil {
		t.Fatalf("initialize: %v", err)
	}

	first := func(target, prev string) error {
		if entries, _ := os.ReadDir(prev); len(entries) != 0 {
			t.Errorf("first push: previous tree should be empty, has %d entries", len(entries))
		}
		if err := os.MkdirAll(filepath.Join(target, "skills", "kept"), 0o755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, "skills", "kept", "SKILL.md"), []byte("published"), 0o644)
	}
	if err := es.pushPlugin(ctx, "alpha", first); err != nil {
		t.Fatalf("first push: %v", err)
	}

	second := func(target, prev string) error {
		// The render carries the pending item over from the previous tree.
		_, err := copyPublished(filepath.Join(prev, "skills", "kept"), filepath.Join(target, "skills", "kept"))
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(target, "README.md"), []byte("changed"), 0o644)
	}
	if err := es.pushPlugin(ctx, "alpha", second); err != nil {
		t.Fatalf("second push: %v", err)
	}

	verify := filepath.Join(root, "verify")
	if _, err := runGit(ctx, "", "clone", bare, verify); err != nil {
		t.Fatalf("clone: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(verify, "plugins", "alpha", "skills", "kept", "SKILL.md")); err != nil || string(b) != "published" {
		t.Errorf("carried-over file in the mirror = %q, %v", b, err)
	}
	if b, _ := os.ReadFile(filepath.Join(verify, "plugins", "alpha", "README.md")); string(b) != "changed" {
		t.Errorf("new file in the mirror = %q", b)
	}
}
