package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestWriteJSON_SetsHeadersAndStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	writeJSON(rec, http.StatusCreated, map[string]string{"hello": "world"})
	if rec.Code != http.StatusCreated {
		t.Errorf("status = %d, want 201", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body is not valid JSON: %v (%q)", err, rec.Body.String())
	}
	if got["hello"] != "world" {
		t.Errorf("body = %v, want hello=world", got)
	}
}

func TestWriteErr_ShapeAndStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	writeErr(rec, http.StatusBadRequest, "bad input")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if got["error"] != "bad input" {
		t.Errorf("error field = %q, want bad input", got["error"])
	}
}

func TestHandleAuthConfig(t *testing.T) {
	a := &App{cfg: Config{
		AuthMode:        "password",
		MarketplaceName: "test-market",
		DefaultLicense:  "Apache-2.0",
	}}
	rec := httptest.NewRecorder()
	a.handleAuthConfig(rec, httptest.NewRequest("GET", "/api/auth/config", nil))
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	var got authConfigResp
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Mode != "password" || got.MarketplaceName != "test-market" || got.DefaultLicense != "Apache-2.0" {
		t.Errorf("config = %+v, want password/test-market/Apache-2.0", got)
	}
}

func TestHandleMe_ReturnsContextUser(t *testing.T) {
	a := &App{}
	user := &User{ID: "u1", Email: "a@b.com", Username: "alice"}
	r := httptest.NewRequest("GET", "/api/me", nil)
	r = r.WithContext(context.WithValue(r.Context(), ctxUserKey, user))
	rec := httptest.NewRecorder()
	a.handleMe(rec, r)
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"username":"alice"`) {
		t.Errorf("body = %q, want alice in payload", rec.Body.String())
	}
}
