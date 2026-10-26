package server

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// getMarketplaceURLDescription is what lets a client set the marketplace up
// from a plain request ("set up the skills marketplace") with no slash command:
// MCP portals may pass tools through but not prompts.
const getMarketplaceURLDescription = "Returns the current user's personal marketplace URL for this plugin & skill " +
	"marketplace, and the command that adds it to Claude Code. Call this when the user asks to set up, " +
	"connect, add or install the skills marketplace or its plugins, then run the returned `command` in " +
	"the user's shell. The URL contains the user's personal API token: do not repeat it in your reply, " +
	"write it to files, or share it anywhere else."

type mcpMarketplaceURLOut struct {
	URL     string `json:"url"`
	Command string `json:"command"`
}

// marketplaceURL is the per-user marketplace URL shown on the home page,
// built with the same embedTokenInBase as the plugin source URLs in
// handleMarketplaceJSON so the two can't drift.
func (a *App) marketplaceURL(token string) string {
	base := strings.TrimRight(a.Cfg.PublicBaseURL, "/")
	return embedTokenInBase(base, token) + "/marketplace.json"
}

func (a *App) addToolGetMarketplaceURL(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{
		Name:        "get_marketplace_url",
		Title:       "Get marketplace URL",
		Description: getMarketplaceURLDescription,
	}, instrumentMCP("get_marketplace_url", a.getMarketplaceURL))
}

func (a *App) getMarketplaceURL(ctx context.Context, _ *mcp.CallToolRequest, _ mcpEmptyIn) (*mcp.CallToolResult, mcpMarketplaceURLOut, error) {
	var zero mcpMarketplaceURLOut
	u := userFromCtx(ctx)
	if u == nil {
		return nil, zero, errors.New("unauthenticated")
	}
	// Empty when api_token_enc can't be decrypted (e.g. the key was rotated);
	// a URL without the token would just 401, so say how to fix it instead.
	if u.APIToken == "" {
		return nil, zero, errors.New("no API token available: regenerate your API token in the web UI, then try again")
	}
	url := a.marketplaceURL(u.APIToken)
	out := mcpMarketplaceURLOut{
		URL:     url,
		Command: fmt.Sprintf("claude plugin marketplace add '%s'", url),
	}
	return okResult("run `command` in the user's shell to add the marketplace", out)
}

func (a *App) addPromptSetupMarketplace(s *mcp.Server) {
	s.AddPrompt(&mcp.Prompt{
		Name:        "setup_marketplace",
		Title:       "Set up the marketplace",
		Description: "Add this plugin & skill marketplace to Claude Code.",
	}, func(ctx context.Context, _ *mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
		return &mcp.GetPromptResult{
			Description: "Add this plugin & skill marketplace to Claude Code.",
			Messages: []*mcp.PromptMessage{{
				Role: "user",
				Content: &mcp.TextContent{Text: "Set up the skills marketplace: call the get_marketplace_url tool, " +
					"then run the returned command in my shell. Don't repeat the URL or the token in your reply."},
			}},
		}, nil
	})
}
