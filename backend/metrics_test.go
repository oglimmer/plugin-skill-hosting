package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsHandlerOpen(t *testing.T) {
	srv := httptest.NewServer(metricsHandler(""))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("status=%d", res.StatusCode)
	}
	body, _ := io.ReadAll(res.Body)
	for _, want := range []string{"go_goroutines", "psh_http_requests_in_flight", "psh_git_materialize_duration_seconds"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("expected %q in /metrics output", want)
		}
	}
}

func TestMetricsHandlerTokenGuard(t *testing.T) {
	srv := httptest.NewServer(metricsHandler("s3cret"))
	defer srv.Close()
	res, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if res.StatusCode != 401 {
		t.Fatalf("expected 401 without token, got %d", res.StatusCode)
	}
	req, _ := http.NewRequest("GET", srv.URL, nil)
	req.Header.Set("Authorization", "Bearer s3cret")
	res2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if res2.StatusCode != 200 {
		t.Fatalf("expected 200 with token, got %d", res2.StatusCode)
	}
}
