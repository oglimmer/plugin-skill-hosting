package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"time"

	"marketplace/internal/metrics"
	"marketplace/internal/skillvalidation"
)

// claudeAPIURL is a var (not a const) only so tests can point callClaude at a
// mock server; production code never reassigns it.
var claudeAPIURL = "https://api.anthropic.com/v1/messages"

const (
	claudeAPIVersion = "2023-06-01"

	// claudeMaxAttempts bounds how many times callClaude will (re)issue a
	// request when the API returns a transient signal. The total wall time is
	// further bounded by the request context (90s in the handlers), so a real
	// long generation that eats the deadline simply stops retrying.
	claudeMaxAttempts = 4
	// claudeBackoffBase is the first retry delay; subsequent delays double it
	// (500ms, 1s, 2s, …) plus jitter, unless the API sends a Retry-After.
	claudeBackoffBase = 500 * time.Millisecond
	claudeBackoffMax  = 8 * time.Second
)

// transientClaudeStatus reports whether an HTTP status from the Claude API is
// worth retrying. 429 (rate limit) and 529 (overloaded) are the ones Anthropic
// explicitly recommends retrying; 500/502/503 are transient upstream blips.
// These previously surfaced straight to the user as a 502 with no retry, which
// was the dominant cause of clustered "claude api: Overloaded" failures under
// load.
func transientClaudeStatus(code int) bool {
	switch code {
	case http.StatusTooManyRequests, // 429
		http.StatusInternalServerError, // 500
		http.StatusBadGateway,          // 502
		http.StatusServiceUnavailable,  // 503
		529:                            // overloaded_error
		return true
	}
	return false
}

// parseRetryAfter reads a Retry-After header (delta-seconds form only, which is
// what Anthropic sends) and returns it as a duration, or 0 if absent/invalid.
func parseRetryAfter(h http.Header) time.Duration {
	v := strings.TrimSpace(h.Get("Retry-After"))
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(v); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second
	}
	return 0
}

// claudeBackoff computes the wait before retry attempt n (1-based). When the
// server supplied a Retry-After we honor it; otherwise we use exponential
// backoff with full jitter, capped at claudeBackoffMax.
func claudeBackoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > claudeBackoffMax {
			return claudeBackoffMax
		}
		return retryAfter
	}
	d := claudeBackoffBase << (attempt - 1)
	if d > claudeBackoffMax {
		d = claudeBackoffMax
	}
	// Full jitter: pick uniformly in [d/2, d] to avoid synchronized retries
	// across concurrent requests all hitting the API at the same instant.
	half := d / 2
	return half + time.Duration(rand.Int63n(int64(half)+1))
}

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
	StopReason string `json:"stop_reason,omitempty"`
	Error      *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

// callClaude sends a single user turn to the Claude messages API and returns
// the model's text reply. Some newer models reject assistant-prefill, so we
// rely on prompt engineering + a tolerant JSON extractor on the caller side
// instead of pinning the response with a leading `{`. Returns a clear error
// when the model hit max_tokens, since truncated JSON is the #1 cause of
// downstream "no JSON object found" parse failures.
func (a *App) callClaude(ctx context.Context, system, user string, maxTokens int) (string, error) {
	if strings.TrimSpace(a.Cfg.AnthropicAPIKey) == "" {
		return "", errors.New("Claude API not configured (set ANTHROPIC_API_KEY)")
	}
	payload, err := json.Marshal(claudeRequest{
		Model:     a.Cfg.AnthropicModel,
		MaxTokens: maxTokens,
		System:    system,
		Messages:  []claudeMessage{{Role: "user", Content: user}},
	})
	if err != nil {
		return "", err
	}

	var lastErr error
	attempt := 1
	for ; attempt <= claudeMaxAttempts; attempt++ {
		text, retryAfter, retryable, err := a.doClaudeRequest(ctx, payload, maxTokens)
		if err == nil {
			return text, nil
		}
		lastErr = err
		// Non-transient (auth, bad request, truncation, parse) or the final
		// attempt: surface immediately. The caller turns this into the 502.
		if !retryable || attempt == claudeMaxAttempts {
			break
		}
		delay := claudeBackoff(attempt, retryAfter)
		log.Printf("claude: transient failure (attempt %d/%d): %v — retrying in %s",
			attempt, claudeMaxAttempts, err, delay.Round(time.Millisecond))
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(delay):
		}
	}
	log.Printf("claude: giving up after %d attempt(s): %v", min(attempt, claudeMaxAttempts), lastErr)
	return "", lastErr
}

// doClaudeRequest performs one round-trip to the Claude messages API. It
// returns the assembled text on success, or an error together with a
// retryable flag and any server-suggested Retry-After delay. Transient
// conditions (network error, 429/5xx/529, overloaded_error) are reported as
// retryable so callClaude can back off and try again.
func (a *App) doClaudeRequest(ctx context.Context, payload []byte, maxTokens int) (text string, retryAfter time.Duration, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, "POST", claudeAPIURL, bytes.NewReader(payload))
	if err != nil {
		return "", 0, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", claudeAPIVersion)
	req.Header.Set("x-api-key", a.Cfg.AnthropicAPIKey)

	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		// Don't retry if the failure is the caller's context being cancelled
		// or its deadline expiring — backing off would be pointless.
		if ctx.Err() != nil {
			return "", 0, false, err
		}
		// Network-level errors (connection reset, DNS, TLS) are transient.
		return "", 0, true, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", 0, ctx.Err() == nil, err
	}

	var cr claudeResponse
	if jerr := json.Unmarshal(body, &cr); jerr != nil {
		// A non-JSON body on a transient status (e.g. a gateway's plain-text
		// 503) should still be retried rather than mis-reported as a decode bug.
		if transientClaudeStatus(resp.StatusCode) {
			return "", parseRetryAfter(resp.Header), true, fmt.Errorf("claude api status %d", resp.StatusCode)
		}
		return "", 0, false, fmt.Errorf("decode claude response: %w", jerr)
	}
	if cr.Error != nil {
		retry := transientClaudeStatus(resp.StatusCode) || cr.Error.Type == "overloaded_error" || cr.Error.Type == "rate_limit_error"
		return "", parseRetryAfter(resp.Header), retry, fmt.Errorf("claude api: %s", cr.Error.Message)
	}
	if resp.StatusCode >= 300 {
		return "", parseRetryAfter(resp.Header), transientClaudeStatus(resp.StatusCode), fmt.Errorf("claude api status %d", resp.StatusCode)
	}

	var sb strings.Builder
	for _, c := range cr.Content {
		if c.Type == "text" {
			sb.WriteString(c.Text)
		}
	}
	if cr.StopReason == "max_tokens" {
		// Truncation is deterministic, not transient — retrying won't help.
		return sb.String(), 0, false, fmt.Errorf("response truncated at max_tokens=%d — increase the limit", maxTokens)
	}
	return sb.String(), 0, false, nil
}

// logClaudeParseFailure records the raw model output (truncated) so prompt or
// max_tokens regressions can be diagnosed without re-running the call.
func logClaudeParseFailure(endpoint string, raw string, err error) {
	const cap = 600
	snippet := raw
	if len(snippet) > cap {
		snippet = snippet[:cap] + "…(truncated)"
	}
	log.Printf("claude parse failure: endpoint=%s err=%v raw=%q", endpoint, err, snippet)
}

type validateSkillRequest struct {
	// PluginName + SkillName let the server load file CONTENTS from the DB
	// so it can compute a file→file reference graph. Contents themselves
	// are never forwarded to Claude — only the resulting edge list. Both
	// fields are optional; when absent the validator still runs but loses
	// the cross-file reference signal (and may flag orphans more eagerly).
	PluginName  string             `json:"pluginName,omitempty"`
	SkillName   string             `json:"skillName,omitempty"`
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

	edges := a.loadFileRefEdgesForSkill(r.Context(), req.PluginName, req.SkillName, req.Files)
	userMsg := buildSkillPromptMessage(req.Name, req.Description, req.Body, "", req.Files, edges)

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	start := time.Now()
	raw, err := a.callClaude(ctx, skillvalidation.SystemPrompt, userMsg, 2048)
	metrics.ClaudeValidationDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ClaudeValidationTotal.WithLabelValues("error").Inc()
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	report, err := skillvalidation.Parse(raw)
	if err != nil {
		metrics.ClaudeValidationTotal.WithLabelValues("error").Inc()
		logClaudeParseFailure("validate", raw, err)
		writeErr(w, http.StatusBadGateway, "could not parse Claude response: "+err.Error())
		return
	}
	metrics.ClaudeValidationTotal.WithLabelValues("success").Inc()
	writeJSON(w, http.StatusOK, report)
}

type fixFindingRequest struct {
	PluginName       string                  `json:"pluginName,omitempty"`
	SkillName        string                  `json:"skillName,omitempty"`
	Name             string                  `json:"name"`
	Description      string                  `json:"description"`
	Body             string                  `json:"body"`
	ExtraFrontmatter string                  `json:"extraFrontmatter,omitempty"`
	Files            []SkillFileSummary      `json:"files,omitempty"`
	Finding          skillvalidation.Finding `json:"finding"`
}

func (a *App) handleFixFinding(w http.ResponseWriter, r *http.Request) {
	var req fixFindingRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	if strings.TrimSpace(req.Finding.Title) == "" && strings.TrimSpace(req.Finding.Detail) == "" {
		writeErr(w, http.StatusBadRequest, "finding is required")
		return
	}

	edges := a.loadFileRefEdgesForSkill(r.Context(), req.PluginName, req.SkillName, req.Files)
	var sb strings.Builder
	sb.WriteString(buildSkillPromptMessage(req.Name, req.Description, req.Body, req.ExtraFrontmatter, req.Files, edges))
	fmt.Fprintf(&sb, "\n\n--- Finding to fix ---\nSeverity: %s\nTitle: %s\nDetail: %s",
		req.Finding.Severity, req.Finding.Title, req.Finding.Detail,
	)

	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()

	start := time.Now()
	// Fix responses can contain the FULL rewritten body, which easily exceeds
	// the validate-call budget. Be generous so we don't truncate the JSON.
	raw, err := a.callClaude(ctx, skillvalidation.FixSystemPrompt, sb.String(), 16384)
	metrics.ClaudeFindingFixDuration.Observe(time.Since(start).Seconds())
	if err != nil {
		metrics.ClaudeFindingFixTotal.WithLabelValues("error").Inc()
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	fix, err := skillvalidation.ParseFix(raw)
	if err != nil {
		metrics.ClaudeFindingFixTotal.WithLabelValues("error").Inc()
		logClaudeParseFailure("finding-fix", raw, err)
		writeErr(w, http.StatusBadGateway, "could not parse Claude response: "+err.Error())
		return
	}
	metrics.ClaudeFindingFixTotal.WithLabelValues("success").Inc()
	writeJSON(w, http.StatusOK, fix)
}

// buildSkillPromptMessage formats the user-side message both Claude calls
// send to the validator. It includes the editable fields the user is
// reviewing (name/description/body/optional extraFrontmatter), the list of
// supporting files (paths only — contents are intentionally withheld), and
// — when available — the cross-file reference graph computed server-side.
// The edge list is the validator's only signal for inter-file links and
// suppresses the "supporting file is never referenced" false positive when
// a file is reached only from another supporting file.
func buildSkillPromptMessage(name, description, body, extraFrontmatter string, files []SkillFileSummary, edges []FileRefEdge) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "Skill name: %s\n\n--- Description ---\n%s\n\n--- Body (Markdown after frontmatter) ---\n%s",
		strings.TrimSpace(name),
		strings.TrimSpace(description),
		body,
	)
	if strings.TrimSpace(extraFrontmatter) != "" {
		fmt.Fprintf(&sb, "\n\n--- Extra frontmatter (YAML lines) ---\n%s", extraFrontmatter)
	}
	if len(files) > 0 {
		sb.WriteString("\n\n--- Supporting files (paths only, not contents) ---\n")
		for _, f := range files {
			kind := "text"
			if f.IsBinary {
				kind = "binary"
			}
			fmt.Fprintf(&sb, "- %s (%s, %d bytes)\n", f.Path, kind, f.SizeBytes)
		}
	}
	if len(edges) > 0 {
		sb.WriteString("\n--- References (file → file, computed server-side from contents) ---\n")
		sb.WriteString("Each line means the source file's content mentions the target file's path (or unique basename). Treat any file appearing as a target as referenced.\n")
		for _, e := range edges {
			fmt.Fprintf(&sb, "- %s -> %s\n", e.From, e.To)
		}
	}
	return sb.String()
}

// loadFileRefEdgesForSkill resolves the plugin/skill, loads the skill's
// stored text files, and returns the reference graph. Best-effort: any
// lookup failure (missing plugin/skill, DB error, or fewer than 2 files)
// silently returns nil so the validator still runs without the edge signal.
// Contents stay server-side; only the returned edge list leaves this layer.
func (a *App) loadFileRefEdgesForSkill(ctx context.Context, pluginName, skillName string, summaries []SkillFileSummary) []FileRefEdge {
	if len(summaries) < 2 {
		return nil
	}
	pluginName = strings.TrimSpace(pluginName)
	skillName = strings.TrimSpace(skillName)
	if pluginName == "" || skillName == "" {
		return nil
	}
	p, err := a.loadPluginByName(ctx, pluginName)
	if err != nil {
		return nil
	}
	s, err := a.loadActiveSkill(ctx, p.ID, skillName)
	if err != nil {
		return nil
	}
	files, err := a.loadSkillFiles(ctx, s.ID)
	if err != nil {
		return nil
	}
	return computeFileRefEdges(files)
}
