# Plugin Marketplace Hosting

A self-hosted Claude Code plugin marketplace. Developers sign up, create plugins, write skills via the web UI, and Claude Code installs them via:

```
/plugin marketplace add http://your-host/marketplace.json
```

## How it works

When a Claude Code user adds a marketplace, two things happen:

1. **`GET /marketplace.json`** — Claude Code fetches a JSON file describing all plugins. This service generates that JSON from Postgres on every request. Each plugin entry has a `source` pointing at a git URL hosted by this same service.

2. **`git clone /git/<plugin-name>.git`** — when the user installs or updates a plugin, Claude Code clones the bare git repo served by the backend. The repo contains:
   - `.claude-plugin/plugin.json` — plugin manifest
   - `skills/<skill-name>/SKILL.md` — one file per skill, with YAML frontmatter
   - `README.md`

The backend keeps Postgres as the source of truth. Whenever you create, edit, or delete a plugin/skill via the API, it **materialises** the plugin into a working tree, commits, and force-pushes to a bare repo on disk under `/data/repos/<plugin>.git`. That bare repo is served via git smart HTTP using [`gitkit`](https://github.com/sosedoff/gitkit), which wraps `git http-backend`.

## Stack

- **Backend**: Go + chi + lib/pq + JWT (golang-jwt) + bcrypt + gitkit
- **Frontend**: Vue 3 + Vite + Pinia + vue-router (TypeScript)
- **Database**: Postgres 16
- **Reverse proxy**: nginx (in the frontend container) — proxies `/api`, `/git`, `/marketplace.json` to the backend

## Run locally with Docker Compose

```bash
cp .env.example .env
docker compose up --build
```

Then open <http://localhost:8080>:

1. Sign up
2. Create a plugin (e.g. `my-tools`)
3. Open it and add a skill (e.g. `summarize`) with a description and Markdown body
4. Visit <http://localhost:8080/marketplace.json> — the new plugin is listed
5. From any Claude Code project run:
   ```
   /plugin marketplace add http://localhost:8080/marketplace.json
   /plugin install my-tools
   ```

> **Note** — for Claude Code to clone from your host, the URL in `marketplace.json` must be reachable from the user's machine. For local testing, `http://localhost:8080` works only from your machine. For other users, set `PUBLIC_BASE_URL` in `.env` to a reachable URL (e.g. an ngrok tunnel or a public DNS name).

## Deploy to Kubernetes with Helm

A chart lives at `helm/plugin-skill-hosting/`. It ships:

- Backend `Deployment` + `Service` (the Go API and git smart-HTTP)
- Frontend `Deployment` + `Service` (nginx serving the SPA)
- A bundled Postgres 16 `Deployment` + `Service` + PVC (toggle off via `postgres.enabled=false` to use an external DB)
- Two PVCs: one for `/data` (bare git repos + worktrees), one for Postgres
- An `Ingress` (nginx + cert-manager) routing `/api`, `/git`, `/marketplace.json`, `/healthz` → backend, and `/` → frontend
- A `SealedSecret` template for `JWT_SECRET` and `POSTGRES_PASSWORD` (or `DATABASE_URL` when `postgres.enabled=false`)

### Prerequisites

- A cluster with `ingress-nginx`, `cert-manager`, and `sealed-secrets` installed
- A cluster issuer (default in `values.yaml` is `oglimmer-com-dns`)
- An image pull secret if pulling from a private registry (default: `oglimmerregistrykey`)

### Configure

Edit `helm/plugin-skill-hosting/values.yaml` — at minimum:

- `publicBaseURL` — must be **HTTPS** (Claude Code rejects `http://` plugin sources). Embedded in `marketplace.json`.
- `ingress.hosts[0].host` and `ingress.tls[0].hosts` — your DNS name
- `backend.image.repository` / `frontend.image.repository` — your container registry
- `cert-manager.io/cluster-issuer` annotation — your issuer name

### Seal the secret

The chart expects a sealed secret named `plugin-skill-hosting-secret`. Generate it before installing:

```bash
kubectl create secret generic plugin-skill-hosting-secret \
  --dry-run=client -o yaml \
  --from-literal=POSTGRES_PASSWORD=<db-password> \
  --from-literal=JWT_SECRET=<32+chars> \
  | kubeseal --format yaml > helm/plugin-skill-hosting/templates/sealed-secret.yaml
```

If `postgres.enabled=false`, replace `POSTGRES_PASSWORD` with `DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=require`.

### Install / upgrade

```bash
helm upgrade --install plugin-skill-hosting helm/plugin-skill-hosting \
  --namespace plugin-skill-hosting --create-namespace
```

### Build and push images

`oglimmer.sh` wraps the build/push/rollout cycle:

```bash
./oglimmer.sh build              # build + push both images, restart both deployments
./oglimmer.sh build -b           # backend only
./oglimmer.sh build -f --no-push # frontend, local only
./oglimmer.sh release            # bump version, tag, build, push
```

Override the registry with `--registries my-registry.com` or `DEFAULT_REGISTRIES_ENV=...`.

### Notes

- The backend container runs as UID 10001; `podSecurityContext.fsGroup: 10001` makes the `/data` PVC group-writable. If you change the image's user, update both.
- Ingress path order matters — backend prefixes (`/api`, `/git`, `/marketplace.json`, `/healthz`) must precede the `/` catch-all that goes to the frontend. The bundled values get this right; preserve order if you edit them.
- The git smart-HTTP endpoint needs `nginx.ingress.kubernetes.io/proxy-request-buffering: "off"` and a generous `proxy-body-size` — both already set in `values.yaml`.
- Both PVCs (`-data` for git, Postgres data) survive `helm uninstall`. Snapshot them before destroying the release.

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

## API surface

Public:
- `GET /marketplace.json` — the marketplace document
- `GET /git/<plugin>.git/...` — git smart HTTP (clone-only)
- `GET /api/plugins` — list all plugins
- `GET /api/plugins/:name` — plugin + its skills

Auth (JWT in `Authorization: Bearer …`):
- `POST /api/auth/register` `{email, username, password}` → `{token, user}`
- `POST /api/auth/login` `{email, password}` → `{token, user}`
- `GET  /api/me`
- `POST /api/plugins`
- `DELETE /api/plugins/:name`
- `POST /api/plugins/:name/skills` `{name, description, body}`
- `PUT  /api/plugins/:name/skills/:skill` `{description, body}`
- `DELETE /api/plugins/:name/skills/:skill`

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

This is an MVP / proof-of-concept:

- No email verification, password reset, or rate limiting
- No private plugins or per-user marketplaces — single global marketplace
- No SKILL.md frontmatter beyond `name` and `description` (no `allowed-tools`, `arguments`, etc.)
- No commands, agents, hooks, or MCP servers — only skills
- Force-push on every change (acceptable for a marketplace, not for a real git repo)

Each of these is straightforward to add later — the data model and API leave room.

## Trying it without Docker, end-to-end smoke test

Once both backend and Postgres are running and you have a plugin called `my-tools` with one skill, you should be able to:

```bash
git clone http://localhost:8080/git/my-tools.git
ls my-tools/.claude-plugin/plugin.json my-tools/skills/*/SKILL.md
```

If that works, Claude Code will be able to install it.
