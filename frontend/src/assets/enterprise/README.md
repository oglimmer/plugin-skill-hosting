# Enterprise-mode walkthrough screenshots

Drop the four connect-tab walkthrough screenshots here, named exactly:

- `step-1.png` — register the marketplace & plugins (managed-settings.json)
- `step-2.png` — add the MCP server (.mcp.json / managed-mcp.json)
- `step-3.png` — plugins load for every member in Claude Code
- `step-4.png` — the team's skills in use

`PluginListView.vue` resolves whatever is present here via `import.meta.glob`, so:
- the build never breaks on a missing file, and
- a screenshot appears on the page as soon as you add it under the matching name.

`.png`, `.jpg`, `.jpeg` and `.webp` are all accepted. Prefer ~1200px-wide PNGs;
they're displayed as small thumbnails and shown full-size in a click-to-enlarge
lightbox.
