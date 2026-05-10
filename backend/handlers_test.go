package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestIsUniqueViolation(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{nil, false},
		{errors.New("pq: duplicate key value violates unique constraint \"users_email_key\""), true},
		{errors.New("ERROR: unique constraint violated"), true},
		{errors.New("connection refused"), false},
	}
	for _, c := range cases {
		got := isUniqueViolation(c.err)
		if got != c.want {
			t.Errorf("isUniqueViolation(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestRespondDBOrConflict_UniqueViolation(t *testing.T) {
	rec := httptest.NewRecorder()
	respondDBOrConflict(rec, errors.New("duplicate key"), "name already taken")
	if rec.Code != http.StatusConflict {
		t.Errorf("status = %d, want 409", rec.Code)
	}
	var body map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("body not JSON: %v", err)
	}
	if body["error"] != "name already taken" {
		t.Errorf("error = %q, want conflict message", body["error"])
	}
}

func TestRespondDBOrConflict_Generic(t *testing.T) {
	rec := httptest.NewRecorder()
	respondDBOrConflict(rec, errors.New("connection refused"), "name already taken")
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body["error"] != "db error" {
		t.Errorf("error = %q, want db error", body["error"])
	}
}
