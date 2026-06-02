package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"marketplace/internal/config"
)

func TestCallClaude_NoAPIKeyConfigured(t *testing.T) {
	a := &App{Cfg: config.Config{AnthropicAPIKey: ""}}
	if _, err := a.callClaude(context.Background(), "sys", "user", 1024); err == nil {
		t.Error("expected error when API key is unset")
	} else if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should mention env var; got %v", err)
	}
}

func TestCallClaude_WhitespaceKeyTreatedAsUnset(t *testing.T) {
	a := &App{Cfg: config.Config{AnthropicAPIKey: "   "}}
	if _, err := a.callClaude(context.Background(), "sys", "user", 1024); err == nil {
		t.Error("expected error when API key is only whitespace")
	}
}

// withMockClaude points callClaude at h for the duration of the test and
// restores the real URL afterwards.
func withMockClaude(t *testing.T, h http.HandlerFunc) {
	t.Helper()
	srv := httptest.NewServer(h)
	prev := claudeAPIURL
	claudeAPIURL = srv.URL
	t.Cleanup(func() {
		claudeAPIURL = prev
		srv.Close()
	})
}

// TestCallClaude_RetriesOverloaded reproduces the production 502: the API
// returns overloaded_error (529) before eventually succeeding. callClaude must
// retry and surface the final success rather than the transient failure.
func TestCallClaude_RetriesOverloaded(t *testing.T) {
	var calls int32
	withMockClaude(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		if n < 3 {
			w.WriteHeader(529)
			w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`))
	})

	a := &App{Cfg: config.Config{AnthropicAPIKey: "test-key", AnthropicModel: "m"}}
	out, err := a.callClaude(context.Background(), "sys", "user", 1024)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if out != "ok" {
		t.Errorf("text = %q, want %q", out, "ok")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 attempts (2 retries), got %d", got)
	}
}

// TestCallClaude_NoRetryOnBadRequest ensures a deterministic 400 is surfaced
// immediately without burning retries.
func TestCallClaude_NoRetryOnBadRequest(t *testing.T) {
	var calls int32
	withMockClaude(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"type":"error","error":{"type":"invalid_request_error","message":"bad"}}`))
	})

	a := &App{Cfg: config.Config{AnthropicAPIKey: "test-key", AnthropicModel: "m"}}
	if _, err := a.callClaude(context.Background(), "sys", "user", 1024); err == nil {
		t.Fatal("expected error on 400")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 attempt for non-transient error, got %d", got)
	}
}

// TestCallClaude_RespectsContextCancellation verifies retries stop when the
// caller's deadline expires instead of spinning the full attempt budget.
func TestCallClaude_RespectsContextCancellation(t *testing.T) {
	withMockClaude(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(529)
		w.Write([]byte(`{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`))
	})

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	a := &App{Cfg: config.Config{AnthropicAPIKey: "test-key", AnthropicModel: "m"}}
	if _, err := a.callClaude(ctx, "sys", "user", 1024); err == nil {
		t.Fatal("expected error when context expires mid-retry")
	}
}

func TestTransientClaudeStatus(t *testing.T) {
	transient := []int{429, 500, 502, 503, 529}
	for _, c := range transient {
		if !transientClaudeStatus(c) {
			t.Errorf("status %d should be transient", c)
		}
	}
	for _, c := range []int{200, 400, 401, 403, 404} {
		if transientClaudeStatus(c) {
			t.Errorf("status %d should not be transient", c)
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	h := http.Header{}
	if d := parseRetryAfter(h); d != 0 {
		t.Errorf("absent header should be 0, got %s", d)
	}
	h.Set("Retry-After", "3")
	if d := parseRetryAfter(h); d != 3*time.Second {
		t.Errorf("Retry-After: 3 -> %s, want 3s", d)
	}
	h.Set("Retry-After", "garbage")
	if d := parseRetryAfter(h); d != 0 {
		t.Errorf("invalid header should be 0, got %s", d)
	}
}
