# plugin-skill-hosting

Self-hosted Claude Code plugin marketplace — backend (Go API + git smart-HTTP + MCP), frontend (Vue SPA behind nginx), Postgres, and ingress. The chart deploys everything needed to expose `https://<host>/marketplace.json` and `git clone https://<host>/git/<plugin>.git` to Claude Code clients. The application's `Secret` (containing `JWT_SECRET`, `POSTGRES_PASSWORD`, etc.) is **not** rendered by the chart — you bring your own; see [§Sealed secret](#sealed-secret).

For an end-to-end product overview see the project [README](../../README.md).

## TL;DR

```bash
# 1. Override the values you care about
cat > my-values.yaml <<EOF
publicBaseURL: "https://plugins.example.com"
ingress:
  hosts:
    - host: plugins.example.com
      paths: *defaultPaths
  tls:
    - secretName: tls-plugins-example
      hosts: [plugins.example.com]
backend:
  image:
    repository: ghcr.io/example/plugin-skill-hosting-backend
frontend:
  image:
    repository: ghcr.io/example/plugin-skill-hosting-frontend
EOF

# 2. Seal the application secret (see "Sealed secret" below)

# 3. Install
helm upgrade --install plugin-skill-hosting . \
  --namespace plugin-skill-hosting --create-namespace \
  -f my-values.yaml
```

## Prerequisites

- Kubernetes 1.24+
- [`ingress-nginx`](https://kubernetes.github.io/ingress-nginx/) controller
- [`cert-manager`](https://cert-manager.io/) with a `ClusterIssuer` for the chart's TLS host
- [`sealed-secrets`](https://github.com/bitnami-labs/sealed-secrets) controller and the matching `kubeseal` CLI
- An image pull secret in the target namespace if your registry is private (default name: `oglimmerregistrykey`)

## What's deployed

| Workload | Purpose |
| --- | --- |
| `Deployment` *backend* | Go API at `/api`, git smart-HTTP at `/git`, MCP server at `/mcp`, marketplace JSON, `/healthz` (liveness), `/readyz` (readiness) |
| `Deployment` *frontend* | nginx serving the Vue SPA |
| `Deployment` *postgres* (optional) | Bundled Postgres 16 — set `postgres.enabled=false` to use an external DB |
| `Service` × 3 | ClusterIP services for backend / frontend / postgres |
| `Ingress` | Routes `/api`, `/git`, `/mcp`, `/marketplace.json`, `/healthz`, `/oauth`, `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource` → backend, `/` → frontend |
| `PersistentVolumeClaim` × 0–2 | `/data` for bare git repos (omitted when `backend.persistence.enabled=false`), Postgres data dir |
| `ServiceAccount` | Pod identity for backend + frontend |
| `ConfigMap` | nginx config for the frontend |

> **Not deployed by the chart** — the application `Secret` carrying `JWT_SECRET`, `POSTGRES_PASSWORD` (or `DATABASE_URL`), and the optional `ANTHROPIC_API_KEY`, `OIDC_CLIENT_SECRET`, and `METRICS_TOKEN`. Supply your own (plain `Secret`, `SealedSecret`, ExternalSecrets, …) — see [§Sealed secret](#sealed-secret).

## Decisions you must make before installing

The chart's `values.yaml` reflects the maintainer's production setup. Before installing in your own cluster, work through these five choices — every install touches all of them.

### 1. Authentication mode (`auth.mode`)

> **Production must use `oidc`.** `password` mode is a development-only convenience (no login rate limiting / brute-force protection, open self-service registration) and is unsupported for production deployments. See the main [README → Authentication](../../README.md#authentication). Pick one of the `oidc` rows below.

| Mode | When to pick it | What you ship |
| --- | --- | --- |
| `oidc` | You already run an IdP (Keycloak, Auth0, Okta, Google, Azure AD, …). OIDC Authorization Code flow. | `auth.oidc.issuerURL` + `auth.oidc.clientID` in values; `OIDC_CLIENT_SECRET` in the sealed secret. |
| `oidc` **+ Google Workspace allowlist** | Like `oidc` but limited to specific Google Workspace domains. | Same as `oidc`, plus `auth.oidc.googleWorkspaceDomains: [yourcompany.com, …]`. With a single domain the chart appends `hd=<domain>` to the auth URL so Google pre-filters the account chooser. The allowlist is only enforced when the issuer is Google — it's a no-op against other IdPs. |
| `password` | **Local development only** — not for production. Users register with email + password; backend signs its own JWTs. | Just `JWT_SECRET` in the sealed secret. |

Switching modes later invalidates existing sessions but does not touch user records.

### 2. Git-repo storage (`backend.persistence.enabled`)

The backend keeps bare git repos and worktrees under `/data`. Two ways to back it:

| Setting | Behaviour | Trade-off |
| --- | --- | --- |
| `true` (default), `rematerializeOnStartup: false` | A PVC is mounted at `/data`; repos survive restarts and upgrades. | Needs `ReadWriteOnce` storage. Forces `backend.replicaCount: 1` unless you switch the PVC to `ReadWriteMany`. |
| `false`, `rematerializeOnStartup: true` | `/data` is an `emptyDir`; on every restart the backend rebuilds all repos from Postgres. | No PVC needed (handy for clusters without a storage class). Cold-start time grows with plugin count — usually a few seconds for small catalogues. Readiness probe holds traffic until rebuild finishes. |

Postgres remains the source of truth either way. Full mechanics in [§Storage modes for git repos](#storage-modes-for-git-repos).

### 3. Postgres — bundled or external (`postgres.enabled`)

- `true` (default): the chart deploys a single-replica Postgres 16 with its own PVC. Set `postgres.user` / `postgres.database` / `postgres.persistence.size` to taste; put `POSTGRES_PASSWORD` in the sealed secret.
- `false`: bring your own database (RDS, Cloud SQL, a managed Postgres next door, etc.). Ship the full DSN in the sealed secret as `DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=require`. The chart's `postgres.*` block is ignored.

There is no built-in migration path between the two — pick once before you start writing data.

### 4. JWT and other secret material (sealed secret)

The chart references but never creates the application `Secret`. Before the pods start you must put one in the namespace with at minimum:

- **`JWT_SECRET`** — signs session JWTs. Generate once with `openssl rand -hex 32` and store it durably; rotating it invalidates every live session.
- **`POSTGRES_PASSWORD`** (bundled DB) **or `DATABASE_URL`** (external DB) — pick the one that matches decision 3.

And conditionally:

- **`OIDC_CLIENT_SECRET`** — required when `auth.mode=oidc`.
- **`ANTHROPIC_API_KEY`** — optional; enables server-side skill validation via `/api/skills/validate`.
- **`METRICS_TOKEN`** — required when you take option B in decision 5.
- **`EXTERNAL_GIT_TOKEN`** — required when `externalGit.remoteURL` is an HTTPS URL (see decision 6).
- **`MCP_OAUTH_CLIENT_SECRET`** — required when `mcpOAuth.clientID` is non-empty (enables OAuth 2.1 on `/mcp`; see the top-level README's [OAuth 2.1 for MCP](../../README.md#oauth-21-for-mcp-optional) section).
- **`API_TOKEN_ENC_KEY`** — optional; the AES key (`openssl rand -hex 32`) used to encrypt API tokens at rest. If omitted, the backend derives one from `JWT_SECRET`. Set a dedicated key so rotating `JWT_SECRET` doesn't also re-key token encryption. **Caveat:** because token *authentication* uses a separate hash, the key only affects *re-display* — changing or adding it later leaves existing tokens authenticating fine, but they become non-displayable until regenerated, so set it from the first deploy.

Sealing recipe and Argo CD layout in [§Sealed secret](#sealed-secret).

### 5. Prometheus metrics (optional, `backend.metrics.*`)

Three positions:

- **Off** — `backend.metrics.enabled: false`. No `/metrics` annotations on the pod. Pick this if you don't run Prometheus.
- **Cluster-internal scrape** (default) — `enabled: true`, `exposeOnIngress: false`. Adds `prometheus.io/scrape` annotations so a cluster-wide Prometheus picks `/metrics` off the in-cluster service. Nothing is exposed publicly; no token needed.
- **Exposed on the public ingress** — also set `exposeOnIngress: true`. Routes `/metrics` through the ingress and requires `METRICS_TOKEN` in the sealed secret; the backend then rejects requests without `Authorization: Bearer <token>`. Pick this only when your scraper lives outside the cluster.

Most installs stay on the default.

### 6. External git mirror (optional, `externalGit.*`)

When `externalGit.remoteURL` is non-empty the backend one-way mirrors the marketplace to that single git repo. Each plugin lands at `plugins/<name>/`. Every UI/API/MCP write pushes the changed subtree. Edits made directly in the external repo are NOT pulled back; they will be overwritten on the next outbound push. See the top-level README's [External git mirror](../../README.md#external-git-mirror-optional) section for the full mechanics.

#### Step-by-step setup (GitHub example)

**1. Create an empty repo** on GitHub (e.g. `your-org/marketplace-mirror`).

**2. Generate a fine-grained PAT** at [github.com/settings/tokens?type=beta](https://github.com/settings/tokens?type=beta):
- **Resource owner**: the account/org that owns the repo
- **Repository access**: "Only select repositories" → pick the mirror repo
- **Repository permissions**:
  - `Contents: Read and write`
  - `Metadata: Read` (auto-included)
- Nothing else needed (no Pull requests, Issues, Actions, Workflows, Administration).

**3. Add the PAT to the application Secret.** Re-run your `kubectl create secret generic ... --dry-run=client -o yaml` command (or your SealedSecret pipeline) with one extra key:
```
--from-literal=EXTERNAL_GIT_TOKEN='github_pat_xxx'
```

**4. Set helm values** (overlay or chart `values.yaml`):
```yaml
externalGit:
  remoteURL: "https://github.com/your-org/marketplace-mirror.git"
  branch: main
  # username defaults to x-access-token (GitHub PAT) — use "oauth2" for GitLab.
```

**5. Apply** (git commit + ArgoCD sync, or `helm upgrade`).

**6. Restart the backend pod** so it re-reads the Secret. Env-from-secretKeyRef is frozen at pod start — pods that started *before* the Secret update will see the old (often empty) values:
```bash
kubectl rollout restart deploy/plugin-skill-hosting-backend -n default
```

**7. Verify env wiring** (the Secret has the right key and the Deployment references it):
```bash
kubectl get secret plugin-skill-hosting-secret -n default \
  -o jsonpath='{.data}' | jq 'keys[]'
# Expect EXTERNAL_GIT_TOKEN in the list.

kubectl get deploy plugin-skill-hosting-backend -n default -o json | \
  jq -r '.spec.template.spec.containers[0].env[] |
         select(.name | startswith("EXTERNAL")) |
         "\(.name): \(.value // .valueFrom.secretKeyRef.key)"'
# Expect the EXTERNAL_GIT_* entries.
```

**8. Verify startup logs**:
```bash
kubectl logs deploy/plugin-skill-hosting-backend -n default | grep -i "external git"
```
Look for either:
- `external git: cloned ... into /data/external/marketplace` (remote already had a branch), or
- `external git: clone failed (... Remote branch main not found ...); initialising empty repo` followed by `external git sync: enabled, remote=... branch=main` (brand-new empty repo — backend pushes an initial README commit).

**9. Initial bootstrap** (only needed when enabling on an already-populated DB):
```bash
TOKEN=<an admin's API token from the home page>
curl -X POST -H "Authorization: Bearer $TOKEN" \
  https://<your-host>/api/external-git/sync-out | jq
```
Idempotent and admin-only. Pushes every active plugin in the DB to the external repo in one shot.

**10. Smoke test**: edit a skill in the UI → within seconds, GitHub shows a new commit by `marketplace <marketplace@local>`.

#### Troubleshooting

| Symptom | Cause | Fix |
|---|---|---|
| Log: `WARN: ... EXTERNAL_GIT_TOKEN is empty — push will likely fail` | Pod started before Secret was updated | `kubectl rollout restart deploy/plugin-skill-hosting-backend -n default` |
| Log: `remote: Write access to repository not granted` / `403` | PAT lacks `Contents: Read and write`, or repo not in PAT's "Repository access" allowlist | Re-issue PAT with correct permissions, update Secret, restart pod |
| Log: `Remote branch main not found in upstream origin` followed by `initialising empty repo` | Brand-new GitHub repo — no commits yet | Expected; backend creates the first commit automatically |
| `sync-out` returns `403` | Caller is not an admin | Promote via `POST /api/users/{id}/promote` or set `is_admin=true` directly in Postgres |
| `sync-out` returns `503` | `EXTERNAL_GIT_REMOTE_URL` empty or sync initialisation failed | Check startup logs; confirm Secret has `EXTERNAL_GIT_TOKEN` and pod has been restarted |

### 7. OAuth 2.1 for `/mcp` (optional, `mcpOAuth.*`)

For MCP clients that perform OAuth discovery instead of accepting a static bearer header — Claude.ai's remote MCP connector is the headline case — the backend can expose an OAuth 2.1 Authorization Code + PKCE server scoped to `/mcp`. Disabled until `mcpOAuth.clientID` is set. See the top-level README's [OAuth 2.1 for MCP](../../README.md#oauth-21-for-mcp-optional) section for the full contract (PKCE-S256, exact-match redirect URIs, 1-hour access tokens, 30-day rotating refresh tokens).

To enable:

1. Pick a `client_id` and a `client_secret` — both are static, deployment-level credentials.
2. Set `mcpOAuth.clientID: "<your-id>"` in values. Optionally override `mcpOAuth.redirectURIs` (defaults to Claude.ai's hosted MCP callback URL).
3. Add `MCP_OAUTH_CLIENT_SECRET=<your-secret>` to the sealed secret.
4. Restart the backend deployment.

The chart's Ingress already routes `/oauth/*`, `/.well-known/oauth-authorization-server`, and `/.well-known/oauth-protected-resource` to the backend; no path edits required. Verify with:

```bash
curl -sS https://<your-host>/.well-known/oauth-authorization-server | jq
```

Should return the RFC 8414 metadata document. A 404 means either the secret isn't mounted yet (pod started before it existed → restart) or `mcpOAuth.clientID` is still empty in values.

Static-bearer access to `/mcp` (the per-user API token) keeps working whether OAuth is enabled or not.

## Configure

The minimum overrides for a real deployment:

| Key | What to set |
| --- | --- |
| `publicBaseURL` | Public **HTTPS** URL — Claude Code rejects `http://` plugin sources |
| `ingress.hosts[0].host`, `ingress.tls[0].hosts` | Your DNS name |
| `ingress.tls[0].secretName` | The TLS secret cert-manager will populate |
| `ingress.annotations."cert-manager.io/cluster-issuer"` | Your `ClusterIssuer` name |
| `backend.image.repository`, `frontend.image.repository` | Your container registry |
| `imagePullSecrets[0].name` | Your pull-secret (or `[]` for public images) |
| `auth.mode` | `password` (default in chart `values.yaml` is `oidc`) — when `oidc`, fill `auth.oidc.*` and ship `OIDC_CLIENT_SECRET` in the sealed secret |

Path order in `ingress.hosts[].paths` matters: backend prefixes (`/api`, `/git`, `/mcp`, `/marketplace.json`, `/healthz`, `/oauth`, `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource`) must precede the `/` catch-all. The defaults are correct — preserve order if you edit them.

### Storage modes for git repos

The backend writes bare git repos and working trees to `/data` (one directory per plugin). Two modes control how that directory is backed:

**Mode 1 — Persistent (default, `backend.persistence.enabled: true`)**

A `PersistentVolumeClaim` is mounted at `/data`. Repos survive pod restarts and upgrades with no extra work. Use this whenever your cluster provides `ReadWriteOnce` storage.

```yaml
backend:
  persistence:
    enabled: true
    size: 5Gi          # tune to the number and size of your plugins
  rematerializeOnStartup: false
```

**Mode 2 — Ephemeral (no PVC, `backend.persistence.enabled: false`)**

`/data` is backed by an `emptyDir` volume — nothing survives a pod restart. To restore git access after a restart the backend re-builds every repo from Postgres at startup. Enable `rematerializeOnStartup` so this happens automatically:

```yaml
backend:
  persistence:
    enabled: false
  rematerializeOnStartup: true
```

How startup works in this mode:

1. The HTTP server starts listening immediately, so the **liveness probe** (`/healthz`) passes right away and Kubernetes never kills the pod during re-materialization.
2. The **readiness probe** (`/readyz`) returns `503 Rematerializing` until every plugin repo has been rebuilt, then flips to `200 ok`. Kubernetes holds all traffic back until that point — no 404s on `/git/...` reach clients.
3. Re-materialization time is proportional to the number of plugins and skills. For a small catalogue it typically completes in a few seconds.

> **Note** — Postgres (or your external DB) is still the source of truth for all plugin and skill data. The only thing re-built from scratch is the on-disk git repos. No user data is at risk when `/data` is ephemeral.

### Sealed secret

The chart references — but does not create — a `Secret` holding the application's sensitive values. The chart resolves the name via the `psh.secretName` helper:

- if `existingSecret` is set, that name is used;
- otherwise it derives `<release>-<chart>-secret` (e.g. release `plugin-skill-hosting` → `plugin-skill-hosting-secret`).

You are responsible for putting a matching `Secret` (or `SealedSecret`, or anything that produces one — ExternalSecrets, etc.) into the release namespace before the pods start. Keeping the secret outside the chart is deliberate: `SealedSecret` ciphertext is scoped to the controller's key and to a specific name + namespace, so it cannot be bundled with a reusable chart.

Required keys: `JWT_SECRET`, and either `POSTGRES_PASSWORD` (when `postgres.enabled=true`) or `DATABASE_URL` (when `false`). Optional keys: `ANTHROPIC_API_KEY`, `OIDC_CLIENT_SECRET` (required when `auth.mode=oidc`), `METRICS_TOKEN` (required when `backend.metrics.exposeOnIngress=true`), `MCP_OAUTH_CLIENT_SECRET` (required when `mcpOAuth.clientID` is non-empty), `EXTERNAL_GIT_TOKEN` (required when `externalGit.remoteURL` is an HTTPS URL).

Generate and seal one with `kubeseal`:

```bash
kubectl create secret generic plugin-skill-hosting-secret \
  --namespace plugin-skill-hosting \
  --dry-run=client -o yaml \
  --from-literal=JWT_SECRET=$(openssl rand -hex 32) \
  --from-literal=POSTGRES_PASSWORD=<db-password> \
  --from-literal=ANTHROPIC_API_KEY=<optional> \
  --from-literal=OIDC_CLIENT_SECRET=<when auth.mode=oidc> \
  --from-literal=MCP_OAUTH_CLIENT_SECRET=<when mcpOAuth.clientID is set> \
  | kubeseal --format yaml | kubectl apply -f -
```

When `postgres.enabled=false`, replace `POSTGRES_PASSWORD` with `DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=require`.

The production deployment in this repo keeps its `SealedSecret` at [`helm/argocd/plugin-skill-hosting-sealed-secret.yaml`](../argocd/plugin-skill-hosting-sealed-secret.yaml). It is reconciled by a dedicated Argo CD `Application` ([`plugin-skill-hosting-secret-app.yaml`](../argocd/plugin-skill-hosting-secret-app.yaml)) — separate from the chart's `Application` so the SealedSecret has `prune: false` while chart resources keep `prune: true`. Apply both with `kubectl apply -f helm/argocd/`.

## Install / upgrade / uninstall

```bash
# install or upgrade
helm upgrade --install plugin-skill-hosting . \
  --namespace plugin-skill-hosting --create-namespace \
  -f my-values.yaml

# render without applying
helm template plugin-skill-hosting . -f my-values.yaml | less

# uninstall (PVCs survive — snapshot before destroying)
helm uninstall plugin-skill-hosting --namespace plugin-skill-hosting
```

Both PVCs (`-data` for git, Postgres data) survive `helm uninstall`. Snapshot them before destroying the release if you want to keep the data.

## Notes & gotchas

- **Non-root UID** — the backend container runs as UID 10001; `podSecurityContext.fsGroup: 10001` makes the `/data` PVC group-writable. If you change the image's user, update both.
- **Git smart-HTTP** — needs `nginx.ingress.kubernetes.io/proxy-request-buffering: "off"` and a generous `proxy-body-size` for pushes. Already set.
- **MCP transport** — the `/mcp` endpoint keeps a long-lived SSE stream open, so `proxy-buffering: "off"` and `proxy-read-timeout: 3600` / `proxy-send-timeout: 3600` are required to keep the stream from being reaped after the default 60 s. Already set.
- **Replica counts are split per component.** `frontend.replicaCount` is safe to scale freely (nginx, stateless). `backend.replicaCount` must stay at `1` whenever `backend.persistence.enabled=true` and `backend.persistence.accessMode=ReadWriteOnce`, because the backend force-pushes to bare git repos on disk and multiple pods would either fail to attach the RWO PVC or race on the same files. The chart `fail`s at render time if you violate this. To run multiple backend pods, switch the PVC to `ReadWriteMany` on a shared storage class — or set `backend.persistence.enabled=false` plus `rematerializeOnStartup=true` (note: that gives each replica its *own* ephemeral `/data`, so pushes still aren't coordinated across pods).
- **Probes** — `/healthz` (liveness + startup) always returns `200` so Kubernetes never kills a pod during re-materialization. `/readyz` (readiness) returns `503` while `rematerializeOnStartup` is running and `200` once complete, so traffic is only routed when git repos are available.
- **External Postgres** — set `postgres.enabled=false` and put `DATABASE_URL` in the sealed secret. The chart's `postgres.*` block is then ignored.

## Values

This table is hand-maintained against `values.yaml`. Update both when adding or renaming keys. See [Keeping the values table in sync](#keeping-the-values-table-in-sync) below if you'd like to automate this with `helm-docs`.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `existingSecret` | string | `""` | Name of an existing `Secret`/`SealedSecret` holding the application's sensitive values. Empty = chart derives `<release>-<chart>-secret`. The chart never creates the secret itself — see [§Sealed secret](#sealed-secret). |
| `publicBaseURL` | string | `"https://ai-plugins.oglimmer.com"` | Public HTTPS URL. Exposed to backend as `PUBLIC_BASE_URL` and embedded in `marketplace.json`. Must be `https://`. |
| `marketplaceName` | string | `"oglimmer-marketplace"` | Name shown in `marketplace.json` (also used as the owner name). |
| `defaultLicense` | string | `"MIT"` | Default license prefilled in the "new plugin" form. |
| `anthropic.model` | string | `"claude-sonnet-4-6"` | Model id used by `/api/skills/validate`. The API key itself goes in the sealed secret as `ANTHROPIC_API_KEY`. |
| `auth.mode` | string | `"oidc"` | `password` (built-in email/password + JWT) or `oidc` (OIDC Authorization Code flow). |
| `auth.oidc.issuerURL` | string | `"https://id.oglimmer.de/realms/oglimmer"` | OIDC discovery URL. Required when `auth.mode=oidc`. |
| `auth.oidc.clientID` | string | `"plugin-skill-hosting"` | OIDC client id. Required when `auth.mode=oidc`. |
| `auth.oidc.redirectURL` | string | `""` | Defaults to `${publicBaseURL}/api/auth/oidc/callback` when empty. |
| `auth.oidc.scopes` | string | `"openid email profile"` | Space-separated OIDC scopes. |
| `auth.oidc.googleWorkspaceDomains` | list | `[]` | Allowlist of Google Workspace `hd` domains. Only enforced when the issuer is Google; ignored for any other IdP. Empty disables the check (a startup `WARN` is logged). With a single domain the auth URL also gets `hd=<domain>` so Google pre-filters the account chooser. |
| `backend.replicaCount` | int | `1` | Number of backend pods. Must stay at 1 with `backend.persistence.enabled=true` + `accessMode=ReadWriteOnce` (chart fails at render otherwise). |
| `backend.image.repository` | string | `"registry.oglimmer.com/plugin-skill-hosting-backend"` | Backend container image repository. |
| `backend.image.tag` | string | `"latest"` | Backend image tag. Pin to a git sha in production for clean rollbacks. |
| `backend.image.pullPolicy` | string | `"Always"` | Backend image pull policy. |
| `backend.service.type` | string | `"ClusterIP"` | Backend Service type. |
| `backend.service.port` | int | `8080` | Backend Service port. |
| `backend.rematerializeOnStartup` | bool | `false` | Re-build all git repos from Postgres in a background goroutine on startup. Set to `true` when `backend.persistence.enabled=false`. See [§Storage modes](#storage-modes-for-git-repos). |
| `backend.persistence.enabled` | bool | `true` | Mount a PVC at `/data` for bare git repos and worktrees. Set to `false` (with `rematerializeOnStartup=true`) when no PVC is available. |
| `backend.persistence.size` | string | `"5Gi"` | Backend PVC size. |
| `backend.persistence.storageClass` | string | `""` | StorageClass for the backend PVC. Empty uses the cluster default. |
| `backend.persistence.accessMode` | string | `"ReadWriteOnce"` | Backend PVC access mode. |
| `backend.resources` | object | requests 100m/128Mi, limits 500m/512Mi | Backend container resources. |
| `frontend.replicaCount` | int | `1` | Number of frontend (nginx) pods. Safe to scale freely — stateless. |
| `frontend.image.repository` | string | `"registry.oglimmer.com/plugin-skill-hosting-frontend"` | Frontend container image repository. |
| `frontend.image.tag` | string | `"latest"` | Frontend image tag. |
| `frontend.image.pullPolicy` | string | `"Always"` | Frontend image pull policy. |
| `frontend.service.type` | string | `"ClusterIP"` | Frontend Service type. |
| `frontend.service.port` | int | `80` | Frontend Service port. |
| `frontend.resources` | object | requests 50m/64Mi, limits 200m/256Mi | Frontend container resources. |
| `postgres.enabled` | bool | `true` | Deploy a bundled single-replica Postgres. Set `false` to use an external DB; then provide `DATABASE_URL` in the sealed secret. |
| `postgres.image.repository` | string | `"postgres"` | Postgres image repository. |
| `postgres.image.tag` | string | `"16-alpine"` | Postgres image tag. |
| `postgres.user` | string | `"marketplace"` | Postgres role / database owner. |
| `postgres.database` | string | `"marketplace"` | Postgres database name. |
| `postgres.service.port` | int | `5432` | Postgres Service port. |
| `postgres.persistence.size` | string | `"5Gi"` | Postgres PVC size. |
| `postgres.persistence.storageClass` | string | `""` | StorageClass for the Postgres PVC. Empty uses the cluster default. |
| `postgres.persistence.accessMode` | string | `"ReadWriteOnce"` | Postgres PVC access mode. |
| `postgres.resources` | object | requests 100m/128Mi, limits 500m/512Mi | Postgres container resources. |
| `ingress.enabled` | bool | `true` | Create the Ingress resource. |
| `ingress.className` | string | `""` | IngressClass name. Empty uses the cluster default. |
| `ingress.annotations` | object | cert-manager + nginx tunings | Ingress annotations — defaults wire up cert-manager and the nginx settings required by git smart-HTTP and the MCP SSE stream. |
| `ingress.hosts` | list | one host with `/api`, `/git`, `/mcp`, `/marketplace.json`, `/healthz`, `/oauth`, `/.well-known/oauth-authorization-server`, `/.well-known/oauth-protected-resource` → backend, `/` → frontend | Hosts and path → backend mapping. **Path order matters** — backend prefixes must precede the `/` catch-all. |
| `ingress.tls` | list | one entry | TLS hosts and the secret cert-manager populates. |
| `serviceAccount.create` | bool | `true` | Create a ServiceAccount for the pods. |
| `serviceAccount.annotations` | object | `{}` | Annotations on the ServiceAccount. |
| `serviceAccount.name` | string | `""` | Override the generated ServiceAccount name. |
| `podSecurityContext.fsGroup` | int | `10001` | fsGroup matching the backend UID — required for the `/data` PVC to be group-writable. |
| `securityContext` | object | non-root, runAsUser 10001, drop ALL caps | Backend container security context. |
| `frontendSecurityContext` | object | `{}` | Frontend container security context. Empty by default because the stock `nginx:alpine` entrypoint needs `CHOWN`/`SETUID`/`SETGID`/`DAC_OVERRIDE` to set up `/var/cache/nginx` — see comment in `values.yaml` for the opt-in patterns. |
| `imagePullSecrets` | list | `[{name: oglimmerregistrykey}]` | Image pull secrets. Set `[]` for public images. |
| `nodeSelector` | object | `{}` | Node selector for pods. |
| `tolerations` | list | `[]` | Tolerations for pods. |
| `affinity` | object | `{}` | Affinity rules for pods. |
| `podAnnotations` | object | `{}` | Annotations applied to all pods. |

## Keeping the values table in sync

The table above is hand-curated. The cleanest way to automate it is [`helm-docs`](https://github.com/norwoodj/helm-docs), but it requires reformatting the comments in `values.yaml` to its `# --` annotation convention, e.g.:

```yaml
# -- Number of backend / frontend pods. Leave at 1 — backend uses RWO storage.
replicaCount: 1

backend:
  image:
    # -- Backend container image repository.
    repository: registry.oglimmer.com/plugin-skill-hosting-backend
```

Once the comments are in that form, drop a `README.md.gotmpl` next to this file with `{{ template "chart.valuesTable" . }}` where the table should appear, then run:

```bash
brew install norwoodj/tap/helm-docs    # or: go install github.com/norwoodj/helm-docs/cmd/helm-docs@latest
helm-docs --chart-search-root=helm
```

A pre-commit hook keeps it from drifting:

```yaml
# .pre-commit-config.yaml
- repo: https://github.com/norwoodj/helm-docs
  rev: v1.14.2
  hooks:
    - id: helm-docs
      args: ["--chart-search-root=helm"]
```

Until the comment-style migration is done, treat the values table here as the source of truth and update it together with `values.yaml`.
