package server

import (
	"context"
	"strings"
	"testing"

	"marketplace/internal/config"
)

func TestGetMarketplaceURLReturnsHomePageURL(t *testing.T) {
	a := &App{Cfg: config.Config{PublicBaseURL: "https://skills.example.com/"}}
	ctx := context.WithValue(context.Background(), ctxUserKey, &User{ID: "u1", APIToken: "abc123"})

	res, out, err := a.getMarketplaceURL(ctx, nil, mcpEmptyIn{})
	if err != nil {
		t.Fatalf("getMarketplaceURL: %v", err)
	}
	// Same shape the home page builds in PluginListView.vue.
	const want = "https://_:abc123@skills.example.com/marketplace.json"
	if out.URL != want {
		t.Errorf("url = %q, want %q", out.URL, want)
	}
	if out.Command != "claude plugin marketplace add '"+want+"'" {
		t.Errorf("command = %q", out.Command)
	}
	if !strings.Contains(resultText(t, res), out.Command) {
		t.Errorf("text content missing the command")
	}
}

func TestGetMarketplaceURLUnauthenticated(t *testing.T) {
	a := &App{Cfg: config.Config{PublicBaseURL: "https://skills.example.com"}}
	if _, _, err := a.getMarketplaceURL(context.Background(), nil, mcpEmptyIn{}); err == nil {
		t.Fatal("expected error for unauthenticated call")
	}
}

func TestGetMarketplaceURLMissingToken(t *testing.T) {
	a := &App{Cfg: config.Config{PublicBaseURL: "https://skills.example.com"}}
	ctx := context.WithValue(context.Background(), ctxUserKey, &User{ID: "u1"})
	_, _, err := a.getMarketplaceURL(ctx, nil, mcpEmptyIn{})
	if err == nil || !strings.Contains(err.Error(), "regenerate") {
		t.Fatalf("err = %v, want a regenerate hint", err)
	}
}
