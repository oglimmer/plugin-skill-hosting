package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDescriptionLen_CountsUTF16CodeUnits(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want int
	}{
		{"ascii", "abc", 3},
		{"empty", "", 0},
		// Em dash is BMP: one rune, one UTF-16 code unit. Our descriptions are
		// full of these, so a byte-based count would reject valid text.
		{"em dash", "a—b", 3},
		// Astral plane: one rune, but a surrogate pair — the SDK counts 2.
		{"emoji", "a🎯b", 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := descriptionLen(tc.in); got != tc.want {
				t.Errorf("descriptionLen(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestValidateSkillDescription(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr string
	}{
		{"empty", "", "description is required"},
		{"whitespace only", "   \n\t ", "description is required"},
		{"ok", "does a thing, use when asked", ""},
		{"exactly at limit", strings.Repeat("a", maxSkillDescriptionChars), ""},
		{"one over", strings.Repeat("a", maxSkillDescriptionChars+1), "at most 1024 characters (got 1025)"},
		// 1024 runes of em dash is 1024 UTF-16 units but 3072 bytes — must pass.
		{"em dashes at limit", strings.Repeat("—", maxSkillDescriptionChars), ""},
		// 513 emoji = 1026 UTF-16 units, so it must fail even though it is
		// only 513 runes.
		{"emoji over limit", strings.Repeat("🎯", 513), "at most 1024 characters (got 1026)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSkillDescription(tc.in)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("validateSkillDescription() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("validateSkillDescription() = nil, want error containing %q", tc.wantErr)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tc.wantErr)
			}
		})
	}
}

// TestSkillWritePaths_RejectLongDescription covers the REST create and update
// handlers. Update is the one that previously had no description check at all,
// so an over-limit description could be introduced by editing an existing
// skill even once create was tightened.
func TestSkillWritePaths_RejectLongDescription_Integration(t *testing.T) {
	pool := requireTestDB(t)
	app := newIntegrationApp(t, pool)
	owner := seedUser(t, pool, "desc-limit-owner", false)

	const pluginName = "desc-limit-plugin"
	const skillName = "desc-limit-skill"

	rec := httptest.NewRecorder()
	app.handleCreatePlugin(rec, authedReq(http.MethodPost, "/api/plugins",
		`{"name":"`+pluginName+`","description":"holder","authorName":"Ann","license":"MIT"}`, owner))
	if rec.Code != http.StatusOK {
		t.Fatalf("create plugin status = %d, want 200; body=%s", rec.Code, readBody(rec))
	}

	tooLong := strings.Repeat("a", maxSkillDescriptionChars+1)

	// --- create rejects it ---
	rec = httptest.NewRecorder()
	app.handleCreateSkill(rec, authedReq(http.MethodPost, "/api/plugins/"+pluginName+"/skills",
		`{"name":"`+skillName+`","description":"`+tooLong+`","body":"# hi"}`, owner, "name", pluginName))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("create skill status = %d, want 400; body=%s", rec.Code, readBody(rec))
	}

	// --- create with a valid description succeeds ---
	rec = httptest.NewRecorder()
	app.handleCreateSkill(rec, authedReq(http.MethodPost, "/api/plugins/"+pluginName+"/skills",
		`{"name":"`+skillName+`","description":"short and valid","body":"# hi"}`, owner, "name", pluginName))
	if rec.Code != http.StatusOK {
		t.Fatalf("create valid skill status = %d, want 200; body=%s", rec.Code, readBody(rec))
	}

	// --- update rejects it ---
	rec = httptest.NewRecorder()
	app.handleUpdateSkill(rec, authedReq(http.MethodPut, "/api/plugins/"+pluginName+"/skills/"+skillName,
		`{"description":"`+tooLong+`","body":"# hi"}`, owner, "name", pluginName, "skill", skillName))
	if rec.Code != http.StatusBadRequest {
		t.Errorf("update skill status = %d, want 400; body=%s", rec.Code, readBody(rec))
	}
}
