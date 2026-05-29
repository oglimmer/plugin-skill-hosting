package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"marketplace/internal/config"
)

// Unmatched routes must return a consistent error: JSON for API clients and a
// styled HTML page for browsers (Accept: text/html), never chi's bare
// "404 page not found" plaintext.
func TestNotFound_ContentNegotiation(t *testing.T) {
	app := &App{Cfg: config.Config{AuthMode: "password", JWTSecret: "x"}}
	h := NewRouter(app)

	t.Run("json for api clients", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/api/does-not-exist", nil)
		req.Header.Set("Accept", "application/json")
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		if body := rec.Body.String(); !strings.Contains(body, `"error":"not found"`) {
			t.Errorf("body = %q, want JSON error", body)
		}
	})

	t.Run("html for browsers", func(t *testing.T) {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/totally-bogus", nil)
		req.Header.Set("Accept", "text/html,application/xhtml+xml")
		h.ServeHTTP(rec, req)

		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404", rec.Code)
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Errorf("Content-Type = %q, want text/html", ct)
		}
		if body := rec.Body.String(); !strings.Contains(body, "<!DOCTYPE html>") || !strings.Contains(body, "Page not found") {
			t.Errorf("body did not contain the HTML error page: %q", body)
		}
	})
}

// A matched path hit with an unsupported method returns 405, not 404.
func TestMethodNotAllowed(t *testing.T) {
	app := &App{Cfg: config.Config{AuthMode: "password", JWTSecret: "x"}}
	h := NewRouter(app)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("DELETE", "/api/version", nil) // /api/version is GET-only
	req.Header.Set("Accept", "application/json")
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405", rec.Code)
	}
	if body := rec.Body.String(); !strings.Contains(body, `"error":"method not allowed"`) {
		t.Errorf("body = %q, want JSON error", body)
	}
}
