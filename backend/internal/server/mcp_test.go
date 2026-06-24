package server

import (
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resultText concatenates the text of every TextContent block in a tool result.
// okResult only ever emits a single text block, but joining keeps the helper
// robust if that changes.
func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	var b strings.Builder
	for _, c := range res.Content {
		tc, ok := c.(*mcp.TextContent)
		if !ok {
			t.Fatalf("unexpected content type %T, want *mcp.TextContent", c)
		}
		b.WriteString(tc.Text)
	}
	return b.String()
}

// TestOKResultEmbedsPayloadInText pins the fix for the bug where read tools
// returned only a one-line summary header (e.g. "8 plugin(s)") with none of the
// data. The full payload was handed to the SDK as StructuredContent, but
// clients that surface only the text Content blocks (claude.ai's connector,
// among others) saw nothing useful. okResult must therefore render the data
// into the text Content itself — the summary line followed by the JSON body.
func TestOKResultEmbedsPayloadInText(t *testing.T) {
	detail := mcpPluginDetail{
		Name:        "marketing-team",
		Description: "Marketing team plugin",
		Version:     "6.0.3",
		OwnerName:   "acme",
		License:     "MIT",
		Homepage:    "https://example.com",
		Skills: []mcpSkillSummary{
			{Name: "deslop", Description: "remove slop from drafts", UpdatedAt: time.Unix(0, 0)},
			{Name: "script-writer", Description: "write video scripts", UpdatedAt: time.Unix(0, 0)},
		},
	}

	res, out, err := okResult("plugin \"marketing-team\" v6.0.3, 2 skill(s)", detail)
	if err != nil {
		t.Fatalf("okResult returned error: %v", err)
	}
	// The structured value must pass through untouched for clients that do read
	// StructuredContent.
	if out.Name != detail.Name || len(out.Skills) != len(detail.Skills) {
		t.Fatalf("okResult mutated its out value: %+v", out)
	}

	text := resultText(t, res)

	// The human-readable summary header is still present.
	if !strings.Contains(text, "2 skill(s)") {
		t.Errorf("text content missing summary header; got:\n%s", text)
	}
	// And — the crux of the regression — every skill name and description is now
	// visible in the text content, not just hidden in StructuredContent.
	for _, want := range []string{
		"deslop", "remove slop from drafts",
		"script-writer", "write video scripts",
		"marketing-team", "6.0.3",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text content missing %q; got:\n%s", want, text)
		}
	}
}

// TestOKResultListPluginsPayload guards the same property for the list endpoint,
// whose summary ("N plugin(s)") discards the most data when only the header is
// surfaced.
func TestOKResultListPluginsPayload(t *testing.T) {
	out := mcpListPluginsOut{Plugins: []mcpPluginSummary{
		{Name: "alpha", Description: "first plugin", Version: "1.0.0", OwnerName: "a", UpdatedAt: time.Unix(0, 0)},
		{Name: "beta", Description: "second plugin", Version: "2.0.0", OwnerName: "b", UpdatedAt: time.Unix(0, 0)},
	}}

	res, _, err := okResult("2 plugin(s)", out)
	if err != nil {
		t.Fatalf("okResult returned error: %v", err)
	}
	text := resultText(t, res)

	for _, want := range []string{"alpha", "first plugin", "beta", "second plugin"} {
		if !strings.Contains(text, want) {
			t.Errorf("text content missing %q; got:\n%s", want, text)
		}
	}
}
