// Package skillvalidation defines the structured report the AI-skill validator
// returns plus a tolerant parser for Claude's free-form output.
package skillvalidation

import (
	"encoding/json"
	"errors"
	"strings"
)

// SystemPrompt is the rubric we give Claude when asking it to review a skill.
const SystemPrompt = `You are an expert reviewer of Claude Code agent skills. A skill is a directory containing SKILL.md (Markdown with YAML frontmatter: name, description, plus a body that tells Claude how to perform a task) and optionally supporting files under scripts/, references/, or assets/.

You will receive a draft. Your entire response must be a single JSON object and nothing else — no leading text, no trailing text, no Markdown, no code fences. The very first character of your output must be "{" and the very last character must be "}". Match exactly this schema:

{
  "summary": "one short sentence verdict",
  "findings": [
    {
      "severity": "problem" | "warning" | "info",
      "title": "very short headline (max ~70 chars)",
      "detail": "one or two sentences explaining the issue and what to change"
    }
  ],
  "suggestedDescription": "rewritten description sentence, or empty string if the current one is already good"
}

Severity rules:
- "problem": will cause Claude to misuse or fail to invoke the skill (e.g. vague description, missing trigger phrases, contradictory body, broken structure). Must be fixed.
- "warning": should be fixed before publishing (e.g. weak phrasing, scope too broad/narrow, missing examples).
- "info": polish or stylistic suggestion (e.g. tighten wording, add a heading).

Evaluation focus:
1. The description is the only thing Claude sees when deciding whether to invoke a skill — it must clearly state WHAT the skill does AND WHEN to trigger it (explicit trigger verbs/nouns/phrases).
2. The skill name should be a clear lowercase slug.
3. The body should be structured, action-oriented Markdown with step-by-step guidance.
4. Body must not contradict the description.
5. Watch for ambiguity, overlap with general capabilities, or scope so broad it would always trigger (or so narrow it never would).
6. If a "Supporting files" section is provided, you see only paths, sizes, and a text/binary flag — never contents. Cross-check the listing against the body and the "References" graph (when present):
   - Flag references in the body to files that are not listed (problem).
   - A file counts as referenced if the body mentions it OR it appears as the target ("-> X") of any edge in the References section — those edges are computed server-side from file contents you cannot see. Only flag a listed file as "never referenced" (warning) when BOTH are absent.
   - If no References section is provided, treat the absence as "unknown" rather than "unreferenced" and skip that warning entirely.
   - Do not make claims about what is inside a supporting file (arguments it accepts, behavior, correctness) — you have not seen contents. Stick to whether it is named, listed, and reachable.

Be direct. No filler, no praise. If everything is fine, return an empty findings array.`

// Finding is a single categorized item in a validation report.
type Finding struct {
	Severity string `json:"severity"` // "problem" | "warning" | "info"
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// Report is the structured response returned to the UI.
type Report struct {
	Summary              string    `json:"summary"`
	Findings             []Finding `json:"findings"`
	SuggestedDescription string    `json:"suggestedDescription,omitempty"`
}

// FixSystemPrompt is the rubric for the per-finding fix call. The model gets
// the original draft plus one specific finding and must return a minimal patch
// targeting just that finding.
const FixSystemPrompt = `You are an expert reviewer of Claude Code agent skills. The user will show you a skill draft and ONE specific finding from a prior review. Your job is to produce a minimal patch that resolves that ONE finding without altering anything else.

Your entire response must be a single JSON object and nothing else — no leading text, no trailing text, no Markdown, no code fences. The very first character of your output must be "{" and the very last character must be "}". Match exactly this schema:

{
  "name": "rewritten slug (OMIT this key entirely if name should not change)",
  "description": "rewritten description (OMIT this key entirely if description should not change)",
  "body": "rewritten full body markdown (OMIT this key entirely if body should not change)",
  "extraFrontmatter": "rewritten YAML frontmatter lines (OMIT this key entirely if extra frontmatter should not change)",
  "note": "one short sentence explaining what you changed (always include)"
}

Rules:
- Only include keys for fields you are actually changing. Omit any field you are not modifying.
- When you DO include a field, return the COMPLETE rewritten value — the frontend replaces the field wholesale, not as a diff.
- Address ONLY the finding you are given. Do not refactor unrelated parts of the skill.
- Prefer the smallest edit that resolves the finding.
- The skill name is a lowercase slug (letters, digits, hyphens). Only change it if the finding explicitly concerns the name.
- Supporting files are listed by path only — you have not seen their contents. Do not invent or rewrite anything based on assumed file contents; refer to supporting files by path only.`

// Fix is the JSON patch returned by the per-finding fix endpoint. Each field
// is a pointer so we can distinguish "no change" (nil) from "set to empty
// string" (non-nil empty), e.g. clearing extraFrontmatter.
type Fix struct {
	Name             *string `json:"name,omitempty"`
	Description      *string `json:"description,omitempty"`
	Body             *string `json:"body,omitempty"`
	ExtraFrontmatter *string `json:"extraFrontmatter,omitempty"`
	Note             string  `json:"note,omitempty"`
}

// ParseFix extracts a Fix from a Claude response. Same tolerance rules as
// Parse — stray whitespace, code fences, and surrounding prose are stripped.
func ParseFix(s string) (*Fix, error) {
	raw, err := extractJSONObject(s)
	if err != nil {
		return nil, err
	}
	var fix Fix
	if err := json.Unmarshal(raw, &fix); err != nil {
		return nil, err
	}
	return &fix, nil
}

// Parse extracts a Report from text that should be pure JSON. We tolerate
// stray whitespace or accidental code fences and trim to the outermost
// { ... } before unmarshalling.
func Parse(s string) (*Report, error) {
	raw, err := extractJSONObject(s)
	if err != nil {
		return nil, err
	}
	var report Report
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, err
	}
	for i, f := range report.Findings {
		sev := strings.ToLower(strings.TrimSpace(f.Severity))
		switch sev {
		case "problem", "warning", "info":
			report.Findings[i].Severity = sev
		default:
			report.Findings[i].Severity = "info"
		}
	}
	return &report, nil
}

func extractJSONObject(s string) ([]byte, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	s = strings.TrimSpace(s)
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end < 0 || end < start {
		return nil, errors.New("no JSON object found")
	}
	return []byte(s[start : end+1]), nil
}
