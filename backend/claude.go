package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	claudeAPIURL     = "https://api.anthropic.com/v1/messages"
	claudeAPIVersion = "2023-06-01"
)

type claudeMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type claudeRequest struct {
	Model     string          `json:"model"`
	MaxTokens int             `json:"max_tokens"`
	System    string          `json:"system,omitempty"`
	Messages  []claudeMessage `json:"messages"`
}

type claudeResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// callClaude sends a single user turn to the Claude messages API and returns
// the model's text reply. Some newer models reject assistant-prefill, so we
// rely on prompt engineering + a tolerant JSON extractor on the caller side
// instead of pinning the response with a leading `{`.
func (a *App) callClaude(ctx context.Context, system, user string) (string, error) {
	if strings.TrimSpace(a.cfg.AnthropicAPIKey) == "" {
		return "", errors.New("Claude API not configured (set ANTHROPIC_API_KEY)")
	}
	payload, err := json.Marshal(claudeRequest{
		Model:     a.cfg.AnthropicModel,
		MaxTokens: 2048,
		System:    system,
		Messages:  []claudeMessage{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, "POST", claudeAPIURL, bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", claudeAPIVersion)
	req.Header.Set("x-api-key", a.cfg.AnthropicAPIKey)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	var cr claudeResponse
	if err := json.Unmarshal(body, &cr); err != nil {
		return "", fmt.Errorf("decode claude response: %w", err)
	}
	if cr.Error != nil {
		return "", fmt.Errorf("claude api: %s", cr.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("claude api status %d", resp.StatusCode)
	}
	var sb strings.Builder
	for _, c := range cr.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	return sb.String(), nil
}

const skillValidationSystemPrompt = `You are an expert reviewer of Claude Code agent skills. A skill is a directory containing SKILL.md (Markdown with YAML frontmatter: name, description, plus a body that tells Claude how to perform a task) and optionally supporting files under scripts/, references/, or assets/.

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
6. If a file listing is provided, cross-check it against the body: flag references to files that are not listed (problem) and flag listed files the body never mentions (warning).

Be direct. No filler, no praise. If everything is fine, return an empty findings array.`

// Finding is a single categorized item in a validation report.
type Finding struct {
	Severity string `json:"severity"` // "problem" | "warning" | "info"
	Title    string `json:"title"`
	Detail   string `json:"detail"`
}

// ValidationReport is the structured response returned to the UI.
type ValidationReport struct {
	Summary              string    `json:"summary"`
	Findings             []Finding `json:"findings"`
	SuggestedDescription string    `json:"suggestedDescription,omitempty"`
}

type validateSkillRequest struct {
	Name        string             `json:"name"`
	Description string             `json:"description"`
	Body        string             `json:"body"`
	Files       []SkillFileSummary `json:"files,omitempty"`
}

func (a *App) handleValidateSkill(w http.ResponseWriter, r *http.Request) {
	var req validateSkillRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.Description) == "" && strings.TrimSpace(req.Body) == "" {
		writeErr(w, http.StatusBadRequest, "description or body is required")
		return
	}

	userMsg := fmt.Sprintf(
		"Skill name: %s\n\n--- Description ---\n%s\n\n--- Body (Markdown after frontmatter) ---\n%s",
		strings.TrimSpace(req.Name),
		strings.TrimSpace(req.Description),
		req.Body,
	)
	if len(req.Files) > 0 {
		var sb strings.Builder
		sb.WriteString("\n\n--- Supporting files (paths only, not contents) ---\n")
		for _, f := range req.Files {
			kind := "text"
			if f.IsBinary {
				kind = "binary"
			}
			fmt.Fprintf(&sb, "- %s (%s, %d bytes)\n", f.Path, kind, f.SizeBytes)
		}
		userMsg += sb.String()
	}

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	start := time.Now()
	raw, err := a.callClaude(ctx, skillValidationSystemPrompt, userMsg)
	claudeValidationDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		claudeValidationTotal.WithLabelValues("error").Inc()
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	report, err := parseValidationReport(raw)
	if err != nil {
		claudeValidationTotal.WithLabelValues("error").Inc()
		writeErr(w, http.StatusBadGateway, "could not parse Claude response: "+err.Error())
		return
	}
	claudeValidationTotal.WithLabelValues("success").Inc()
	writeJSON(w, http.StatusOK, report)
}

// parseValidationReport extracts a ValidationReport from text that should be
// pure JSON. We tolerate stray whitespace or accidental code fences and trim
// to the outermost { ... } before unmarshalling.
func parseValidationReport(s string) (*ValidationReport, error) {
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
	s = s[start : end+1]

	var report ValidationReport
	if err := json.Unmarshal([]byte(s), &report); err != nil {
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
