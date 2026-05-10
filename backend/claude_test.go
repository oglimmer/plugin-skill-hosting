package main

import (
	"context"
	"strings"
	"testing"
)

func TestParseValidationReport_Plain(t *testing.T) {
	in := `{"summary":"ok","findings":[{"severity":"problem","title":"T","detail":"D"}]}`
	rep, err := parseValidationReport(in)
	if err != nil {
		t.Fatalf("parseValidationReport: %v", err)
	}
	if rep.Summary != "ok" || len(rep.Findings) != 1 || rep.Findings[0].Severity != "problem" {
		t.Errorf("unexpected report: %+v", rep)
	}
}

func TestParseValidationReport_StripsCodeFence(t *testing.T) {
	in := "```json\n{\"summary\":\"hi\",\"findings\":[]}\n```"
	rep, err := parseValidationReport(in)
	if err != nil {
		t.Fatalf("parseValidationReport: %v", err)
	}
	if rep.Summary != "hi" {
		t.Errorf("summary = %q", rep.Summary)
	}
}

func TestParseValidationReport_NormalisesSeverity(t *testing.T) {
	in := `{"summary":"x","findings":[
		{"severity":"WARNING","title":"a","detail":"a"},
		{"severity":"  Info  ","title":"b","detail":"b"},
		{"severity":"weird","title":"c","detail":"c"}
	]}`
	rep, err := parseValidationReport(in)
	if err != nil {
		t.Fatalf("parseValidationReport: %v", err)
	}
	wants := []string{"warning", "info", "info"} // unknown defaults to "info"
	for i, w := range wants {
		if rep.Findings[i].Severity != w {
			t.Errorf("findings[%d].Severity = %q, want %q", i, rep.Findings[i].Severity, w)
		}
	}
}

func TestParseValidationReport_NoJSON(t *testing.T) {
	if _, err := parseValidationReport("totally not json"); err == nil {
		t.Error("expected error for non-JSON input")
	}
}

func TestParseValidationReport_GarbageBeforeAndAfter(t *testing.T) {
	in := "preamble blah {\"summary\":\"s\",\"findings\":[]} trailing junk"
	rep, err := parseValidationReport(in)
	if err != nil {
		t.Fatalf("parseValidationReport: %v", err)
	}
	if rep.Summary != "s" {
		t.Errorf("summary = %q", rep.Summary)
	}
}

func TestParseValidationReport_MalformedJSON(t *testing.T) {
	// Looks like JSON (has braces) but isn't a valid object.
	if _, err := parseValidationReport("{not valid"); err == nil {
		t.Error("expected unmarshal error for malformed JSON")
	}
}

func TestParseValidationReport_EmptyFindingsAndSuggestion(t *testing.T) {
	in := `{"summary":"all good","findings":[],"suggestedDescription":"better one"}`
	rep, err := parseValidationReport(in)
	if err != nil {
		t.Fatalf("parseValidationReport: %v", err)
	}
	if len(rep.Findings) != 0 {
		t.Errorf("findings should be empty, got %d", len(rep.Findings))
	}
	if rep.SuggestedDescription != "better one" {
		t.Errorf("suggestedDescription = %q", rep.SuggestedDescription)
	}
}

func TestCallClaude_NoAPIKeyConfigured(t *testing.T) {
	a := &App{cfg: Config{AnthropicAPIKey: ""}}
	if _, err := a.callClaude(context.Background(), "sys", "user"); err == nil {
		t.Error("expected error when API key is unset")
	} else if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should mention env var; got %v", err)
	}
}

func TestCallClaude_WhitespaceKeyTreatedAsUnset(t *testing.T) {
	a := &App{cfg: Config{AnthropicAPIKey: "   "}}
	if _, err := a.callClaude(context.Background(), "sys", "user"); err == nil {
		t.Error("expected error when API key is only whitespace")
	}
}
