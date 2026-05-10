package main

import "testing"

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
