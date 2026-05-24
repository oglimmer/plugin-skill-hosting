# Plugin Marketplace Hosting

A self-hosted, **token-gated** Claude Code plugin marketplace built for **organizations to share and collaborate on plugins and skills**. Instead of every engineer collecting their own private skills, your team publishes them to a shared marketplace that any Claude Code user in the org can install with one command:

```
/plugin marketplace add https://_:<api-token>@your-host/marketplace.json
```

Anyone in the organization can sign in, create a plugin, author skills via the web UI (or via MCP from inside Claude), and the moment they hit save the new version is available to every other user's Claude Code — no review queue, no packaging step, no redeploy. The model is deliberately collaborative: plugins are visible to everyone, skill edit history is preserved, and soft-deletes mean nothing is ever truly lost.

The same per-user token also unlocks a built-in **MCP server** at `/mcp` so Claude (or any MCP-aware client) can read plugins and create / modify skills directly — useful for evolving a shared skill from inside an editing session instead of bouncing back to the web UI.

Every endpoint that exposes plugin data — `marketplace.json`, the git smart-HTTP repos, `/mcp`, and the read APIs — requires a valid token. The token is generated per user and shown on the front page. Anyone holding the token can clone repos, drive the MCP server, and read the marketplace as that user, so distribute it like any other org credential.

## How it works

When a Claude Code user adds a marketplace, two things happen:

1. **`GET /marketplace.json`** — Claude Code fetches a JSON file describing all plugins. This service generates that JSON from Postgres on every request. Each plugin entry has a `source` pointing at a git URL hosted by this same service.

2. **`git clone /git/<plugin-name>.git`** — when the user installs or updates a plugin, Claude Code clones the bare git repo served by the backend. The repo contains:
   - `.claude-plugin/plugin.json` — plugin manifest
   - `skills/<skill-name>/SKILL.md` — one file per skill, with YAML frontmatter
   - `skills/<skill-name>/{scripts,references,assets}/…` — optional supporting files for multi-file skills
   - `README.md`

The backend keeps Postgres as the source of truth. Whenever you create, edit, or delete a plugin/skill via the API (or via MCP), it **materialises** the plugin into a working tree, commits, and force-pushes to a bare repo on disk under `/data/repos/<plugin>.git`. That bare repo is served via git smart HTTP using [`gitkit`](https://github.com/sosedoff/gitkit), which wraps `git http-backend`.

Independently, **`/mcp`** exposes a Model Context Protocol server (Streamable HTTP transport) bound to the same per-user token. Tools cover read-only access to plugins and full create/update access to skills and their supporting files — every write goes through the same materialise-and-commit pipeline, so MCP edits show up in the git repo and the marketplace immediately. See [§MCP server](#mcp-server) below.

Plugin and skill versions are managed automatically: the first plugin a user creates starts at `0.1.0`, every subsequent plugin starts at `1.0.0`, and edits bump major / minor / patch based on the kind of change (skill add or delete → major; large body edits → minor; small edits and file uploads → patch). Deletes are **soft** — both plugins and skills can be restored — and every skill edit snapshots the description, body, and file tree into a versions table so you can revert to any earlier point.

## Stack

- **Backend**: Go 1.26 + chi + lib/pq + JWT (golang-jwt) + bcrypt + gitkit + the official [`modelcontextprotocol/go-sdk`](https://github.com/modelcontextprotocol/go-sdk) for the MCP server
- **Frontend**: Vue 3 + Vite + Pinia + vue-router (TypeScript)
- **Database**: Postgres 16
- **Reverse proxy**: nginx (in the frontend container) — proxies `/api`, `/git`, `/mcp`, `/marketplace.json` to the backend
- **Optional**: an `ANTHROPIC_API_KEY` enables the in-app skill validator (POST `/api/skills/validate`) which runs a skill body through the Anthropic API for static review

## Authentication

The backend supports two **sign-in** modes, picked at startup via `AUTH_MODE`:

- `password` (default — used in dev): the built-in email/username/password flow with bcrypt + JWT.
- `oidc`: server-side OpenID Connect Authorization Code flow. Users are auto-provisioned in the local `users` table on first login (matched by `(issuer, sub)`, then by verified `email`).

Inside the SPA, sessions ride on a JWT in `localStorage` sent as `Authorization: Bearer <jwt>`.

In addition, every user is issued a long-lived **API token** at registration. This token gates `/marketplace.json`, `/git/<plugin>.git/...`, `/mcp`, and the read-only plugin APIs. It is accepted via:

- `Authorization: Bearer <api-token>` — for API calls and MCP calls
- HTTP Basic Auth where the **password** is the token (username can be anything, e.g. `_`) — for `git clone` and Claude Code's marketplace fetch

Unauthenticated `/marketplace.json` and `/git/...` answer with `WWW-Authenticate: Basic` so `git clone` and `curl` prompt for credentials. Unauthenticated `/mcp` answers with `WWW-Authenticate: Bearer realm="plugin-marketplace"` so MCP clients pick the bearer flow rather than fall back to OAuth discovery.

The token is shown on the home page after sign-in and can be regenerated from there.

The frontend calls `GET /api/auth/config` on load to learn which sign-in mode is active and renders either the password form or the "Sign in with SSO" button.

### OIDC config

Set on the backend container:

| Var | Required | Default |
| --- | --- | --- |
| `AUTH_MODE` | yes (set to `oidc`) | `password` |
| `OIDC_ISSUER_URL` | yes | — |
| `OIDC_CLIENT_ID` | yes | — |
| `OIDC_CLIENT_SECRET` | yes | — |
| `OIDC_REDIRECT_URL` | no | `${PUBLIC_BASE_URL}/api/auth/oidc/callback` |
| `OIDC_SCOPES` | no | `openid email profile` |
| `OIDC_GOOGLE_WORKSPACE_DOMAINS` | no | — |

Register `${PUBLIC_BASE_URL}/api/auth/oidc/callback` as an allowed redirect URI in your IdP. After a successful exchange the backend redirects the browser to `${PUBLIC_BASE_URL}/auth/callback#token=…&user=…` (the SPA reads the hash and stores the session).

### Google Workspace restriction

When the IdP is Google, you can pin sign-in to one or more Google Workspace domains via `OIDC_GOOGLE_WORKSPACE_DOMAINS` (comma-separated, e.g. `yourcompany.com,subsidiary.com`). The check has two parts:

- **UI hint** — if exactly one domain is configured, the backend appends `hd=<domain>` to the Google authorisation URL so the account chooser pre-filters to that workspace. With multiple domains the hint is omitted (Google only honours a single value); backend validation still applies.
- **Authoritative check** — after the ID token is verified, the backend reads the `hd` claim and rejects sign-ins whose domain is not in the allowlist. Rejections respond `HTTP 401` with `{"error":"workspace domain not allowed"}` and write a `WARN` audit log line containing the rejected `hd`, email, sub, and issuer. The error message is intentionally generic and does not leak the configured domains.

If the list is empty, no restriction is applied and a startup warning is logged. The check is also a no-op for non-Google issuers, so generic OIDC providers (Keycloak, Auth0 dev tenants, etc.) used for local testing are unaffected.

## MCP server

`/mcp` is a Streamable HTTP MCP endpoint authenticated by the same per-user API token. The home page renders a copy-paste setup card with the token pre-filled. The server announces itself as `plugin-skill-hosting`; the MCP server name your client uses is up to you (the home page suggests `MARKETPLACE_NAME`).

One-line install for Claude Code:

```
claude mcp add --transport http <server-name> https://your-host/mcp \
  -H "Authorization: Bearer <api-token>"
```

JSON config snippet for Claude Desktop and other MCP clients (paste under `mcpServers`):

```json
{
  "mcpServers": {
    "<server-name>": {
      "type": "http",
      "url": "https://your-host/mcp",
      "headers": { "Authorization": "Bearer <api-token>" }
    }
  }
}
```

Tools exposed:

| Tool | Mode | Purpose |
| --- | --- | --- |
| `list_plugins` | read | List all active plugins |
| `get_plugin` | read | Plugin metadata + skill list |
| `get_skill` | read | A skill's description, SKILL.md body, and file list |
| `list_skill_files` | read | Paths + sizes of supporting files |
| `get_skill_file` | read | One file's content; binary returned as base64 |
| `create_skill` | write | Add a new skill to a plugin |
| `update_skill` | write | Replace a skill's description and body |
| `upsert_skill_file` | write | Create or overwrite a supporting file under `scripts/`, `references/`, or `assets/` |

Plugins themselves are read-only over MCP (no `create_plugin` / `delete_plugin`), and **nothing can be deleted via MCP** — destructive operations stay behind the web UI. Every write tool runs the same code path as the corresponding REST handler: it bumps the plugin version, snapshots a new skill version row, and re-materialises the bare git repo, so changes are visible to `git clone` and `marketplace.json` immediately.

Behind a reverse proxy, the `/mcp` location needs response buffering off and long read/send timeouts because the MCP transport keeps a long-lived SSE GET stream open. Both `frontend/nginx.conf` (for Compose) and the helm chart's ingress annotations (`nginx.ingress.kubernetes.io/proxy-buffering`, `proxy-read-timeout`, `proxy-send-timeout`) already set those.

## Run locally with Docker Compose

```bash
cp .env.example .env
docker compose up --build
```

Then open <http://localhost:8080>:

1. Sign up — your API token is generated and shown on the home page
2. Create a plugin (e.g. `my-tools`)
3. Open it and add a skill (e.g. `summarize`) with a description and Markdown body
4. Copy the marketplace command from the home page — it includes your token, e.g.
   `/plugin marketplace add http://_:<token>@localhost:8080/marketplace.json`
5. From any Claude Code project run:
   ```
   /plugin marketplace add http://_:<token>@localhost:8080/marketplace.json
   /plugin install my-tools
   ```

Without the token, every `marketplace.json` and `/git/...` request gets a `401 Unauthorized`.

> **Note** — for Claude Code to clone from your host, the URL in `marketplace.json` must be reachable from the user's machine. For local testing, `http://localhost:8080` works only from your machine. For other users, set `PUBLIC_BASE_URL` in `.env` to a reachable URL (e.g. an ngrok tunnel or a public DNS name).

## Deploy to Kubernetes with Helm

A chart lives at [`helm/plugin-skill-hosting/`](helm/plugin-skill-hosting/README.md) — see its README for prerequisites, the full values reference, sealing the application secret, and ingress / PVC gotchas.

Two product-level points worth knowing before you read the chart docs:

- `publicBaseURL` must be **HTTPS** — Claude Code rejects `http://` plugin sources, and the URL is embedded in `marketplace.json`.
- The chart deploys backend + frontend + Postgres + ingress, but **does not create** the application `Secret` itself — you supply one out-of-band (plain `Secret`, `SealedSecret`, ExternalSecrets, …) named to match `psh.secretName`. Postgres can be turned off (`postgres.enabled=false`) to use an external DB; in that case `DATABASE_URL` belongs in that secret instead of `POSTGRES_PASSWORD`.

### Deploy via ArgoCD

A starter ArgoCD `Application` manifest lives at [`helm/argocd/plugin-skill-hosting-app.yaml`](helm/argocd/plugin-skill-hosting-app.yaml). It points at this repo's chart on `master`, sets `backend.image.tag=latest` / `frontend.image.tag=latest`, and is annotated for [argocd-image-updater](https://argocd-image-updater.readthedocs.io/) (digest strategy) so new pushes of `:latest` roll out automatically.

```bash
# 1. apply the application secret (env-scoped — name + namespace + ciphertext
#    are pinned to the controller key in the target cluster)
kubectl apply -f helm/argocd/plugin-skill-hosting-sealed-secret.yaml

# 2. apply the ArgoCD Application — it syncs the chart from this repo
kubectl apply -f helm/argocd/plugin-skill-hosting-app.yaml
```

### Build and push images

Images live at `ghcr.io/oglimmer/plugin-skill-hosting-{backend,frontend}`.

**Releasing a new version** — `oglimmer.sh release` bumps `frontend/package.json`, commits, creates an annotated git tag, and pushes both to `origin`. The tag push triggers the GitHub Actions `release` workflow (`.github/workflows/release.yml`), which builds multi-arch (`linux/amd64` + `linux/arm64`) images, pushes them to ghcr.io tagged as both `:v<version>` and `:latest`, and creates a GitHub Release with auto-generated notes.

```bash
./oglimmer.sh release            # interactive semver bump → tag → CI builds images
./oglimmer.sh release --bump minor  # non-interactive
```

**Local builds** push `:latest` directly to ghcr.io and optionally restart the in-cluster deployments. Authenticate once with a GitHub PAT that has `write:packages` scope:

```bash
echo YOUR_PAT | docker login ghcr.io -u oglimmer --password-stdin
```

Then use `oglimmer.sh` as before:

```bash
./oglimmer.sh build              # build + push both images, restart both deployments
./oglimmer.sh build -b           # backend only
./oglimmer.sh build -f --no-push # frontend, local only
```

Override the registry with `--registries my-registry.com` or `DEFAULT_REGISTRIES_ENV=...`.

## Run for development (no Docker)

Backend:
```bash
cd backend
# Need a Postgres running on localhost:5432 (db=marketplace, user=marketplace, pw=marketplace)
DATABASE_URL=postgres://marketplace:marketplace@localhost:5432/marketplace?sslmode=disable \
JWT_SECRET=dev-secret-please-32-chars-minimum \
DATA_DIR=./data \
PUBLIC_BASE_URL=http://localhost:8080 \
go run .
```

Frontend:
```bash
cd frontend
npm install
npm run dev    # http://localhost:5173 with proxy to backend
```

## Tests

Backend (pure unit tests, no DB needed):
```bash
cd backend && go test ./...
```

Frontend (Vitest + Testing Library, jsdom):
```bash
cd frontend && npm test           # one-shot
cd frontend && npm run test:watch # watch mode
```

## API surface

Public:
- `GET /api/auth/config` → `{ mode: "password" | "oidc", marketplaceName, defaultLicense }`
- `POST /api/auth/register` `{email, username, password}` → `{token, user}` *(only when `AUTH_MODE=password`)*
- `POST /api/auth/login` `{email, password}` → `{token, user}` *(only when `AUTH_MODE=password`)*
- `GET  /api/auth/oidc/login` → 302 to IdP *(only when `AUTH_MODE=oidc`)*
- `GET  /api/auth/oidc/callback` → 302 to `${PUBLIC_BASE_URL}/auth/callback#token=…&user=…` *(only when `AUTH_MODE=oidc`)*
- `GET  /healthz` — always `200 ok`; used by the liveness and startup probes
- `GET  /readyz` — `200 ok` when ready, `503 Rematerializing` while startup re-materialization is in progress; used by the readiness probe

Token-gated (Bearer JWT or API token; HTTP Basic with token as password is also accepted on the marketplace + git endpoints; Bearer-only on `/mcp`):

*Marketplace + git*
- `GET /marketplace.json` — the marketplace document. URLs inside embed the requesting user's token as Basic-Auth credentials so subsequent `git clone` works.
- `GET /git/<plugin>.git/...` — git smart HTTP (clone-only). On unauthenticated requests responds with `WWW-Authenticate: Basic` so `git clone` prompts.
- `POST/GET/DELETE /mcp` — Streamable HTTP MCP server. See [§MCP server](#mcp-server) for tools and config.

*User*
- `GET /api/me` — returns the user incl. `apiToken`
- `POST /api/me/token/regenerate` → `{ apiToken }` (invalidates the previous token)
- `GET /api/me/deleted-plugins` — soft-deleted plugins owned by the caller (drives the restore UI)

*Plugins*
- `GET /api/plugins` — list all active plugins
- `GET /api/plugins/:name` — plugin + its active skills
- `POST /api/plugins` — version is assigned automatically (no `version` field needed)
- `DELETE /api/plugins/:name` — soft-delete (owner only); the bare repo is wiped on disk
- `POST /api/plugins/:name/restore` — un-soft-delete (owner only); re-materialises the repo

*Skills*
- `POST /api/plugins/:name/skills` `{name, description, body}`
- `PUT  /api/plugins/:name/skills/:skill` `{description, body}`
- `DELETE /api/plugins/:name/skills/:skill` — soft-delete
- `GET /api/plugins/:name/deleted-skills` — list soft-deleted skills for the restore UI
- `POST /api/plugins/:name/skills/:skill/restore` — un-soft-delete
- `GET /api/plugins/:name/skills/:skill/versions` — full edit history (newest first)
- `POST /api/plugins/:name/skills/:skill/revert/:version` — restore description + body + file tree from a snapshot

*Skill files (multi-file skills)*
- `GET /api/plugins/:name/skills/:skill/files` — list paths/sizes (no content)
- `GET /api/plugins/:name/skills/:skill/files/<path>` — fetch one file (binary as base64)
- `PUT /api/plugins/:name/skills/:skill/files/<path>` `{content, isBinary?}` — create or update; max 10 MB per file, 100 MB / 50 files per skill, paths must live under `scripts/`, `references/`, or `assets/`
- `DELETE /api/plugins/:name/skills/:skill/files/<path>`

*Validator*
- `POST /api/skills/validate` `{description, body, files?}` — runs the skill through the Anthropic API for static review. Requires `ANTHROPIC_API_KEY` on the backend; controlled by `ANTHROPIC_MODEL` (default `claude-sonnet-4-6`).

## Plugin layout produced

For a plugin `my-tools` with two skills `foo` and `bar`:

```
my-tools/
├── .claude-plugin/
│   └── plugin.json          # name, description, version, author, license, homepage
├── skills/
│   ├── foo/
│   │   └── SKILL.md         # frontmatter (name, description) + body
│   └── bar/
│       └── SKILL.md
└── README.md
```

`SKILL.md` shape:
```markdown
---
name: foo
description: One-line summary Claude uses to decide when to apply this skill
---

## Instructions

…body markdown…
```

## What this is *not*

- A SaaS product with clear tenant separation — it is a single-tenant sharing platform for one organization, with no isolation between users
- User/Password has no email verification, password reset as it's only for dev testing
- No SKILL.md frontmatter beyond `name` and `description` (no `allowed-tools`, `arguments`, etc.)
- A plugin may contain skills only — no commands, agents, hooks, or bundled MCP servers as plugin contents. (This service *exposes* its own MCP server at `/mcp` so clients can edit skills, but the plugins it hosts can still only ship skills.)

Each of these is straightforward to add later — the data model and API leave room.

## Trying it without Docker, end-to-end smoke test

Once both backend and Postgres are running and you have a plugin called `my-tools` with one skill, you should be able to:

```bash
TOKEN=<copy-from-the-home-page>
curl -s -u _:$TOKEN http://localhost:8080/marketplace.json | jq .
git clone http://_:$TOKEN@localhost:8080/git/my-tools.git
ls my-tools/.claude-plugin/plugin.json my-tools/skills/*/SKILL.md
```

Without the token, both requests return `401 Unauthorized`.

If both work, Claude Code will be able to install the plugin via:

```
/plugin marketplace add http://_:$TOKEN@localhost:8080/marketplace.json
```
