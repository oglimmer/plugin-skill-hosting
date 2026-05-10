# plugin-skill-hosting

Self-hosted Claude Code plugin marketplace — backend (Go API + git smart-HTTP + MCP), frontend (Vue SPA behind nginx), Postgres, ingress, and a sealed secret for the application's secrets. The chart deploys everything needed to expose `https://<host>/marketplace.json` and `git clone https://<host>/git/<plugin>.git` to Claude Code clients.

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
| `Deployment` *backend* | Go API at `/api`, git smart-HTTP at `/git`, MCP server at `/mcp`, marketplace JSON, healthz |
| `Deployment` *frontend* | nginx serving the Vue SPA |
| `Deployment` *postgres* (optional) | Bundled Postgres 16 — set `postgres.enabled=false` to use an external DB |
| `Service` × 3 | ClusterIP services for backend / frontend / postgres |
| `Ingress` | Routes `/api`, `/git`, `/mcp`, `/marketplace.json`, `/healthz` → backend, `/` → frontend |
| `PersistentVolumeClaim` × 2 | `/data` for bare git repos, Postgres data dir |
| `SealedSecret` | `JWT_SECRET`, `ANTHROPIC_API_KEY`, `OIDC_CLIENT_SECRET`, `POSTGRES_PASSWORD` (or `DATABASE_URL`) |
| `ServiceAccount` | Pod identity for backend + frontend |
| `ConfigMap` | nginx config for the frontend |

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

Path order in `ingress.hosts[].paths` matters: backend prefixes (`/api`, `/git`, `/mcp`, `/marketplace.json`, `/healthz`) must precede the `/` catch-all. The defaults are correct — preserve order if you edit them.

### Sealed secret

The chart references a sealed secret named `plugin-skill-hosting-secret` (see `templates/sealed-secret.yaml`). Generate it before installing:

```bash
kubectl create secret generic plugin-skill-hosting-secret \
  --dry-run=client -o yaml \
  --from-literal=JWT_SECRET=$(openssl rand -hex 32) \
  --from-literal=POSTGRES_PASSWORD=<db-password> \
  --from-literal=ANTHROPIC_API_KEY=<optional, enables /api/skills/validate> \
  --from-literal=OIDC_CLIENT_SECRET=<only when auth.mode=oidc> \
  | kubeseal --format yaml > templates/sealed-secret.yaml
```

When `postgres.enabled=false`, replace `POSTGRES_PASSWORD` with `DATABASE_URL=postgres://user:pass@host:5432/db?sslmode=require`.

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
- **`replicaCount`** — leave at `1`. The backend writes to a single ReadWriteOnce PVC and force-pushes to bare git repos on disk; multi-replica needs RWX storage and is not currently supported.
- **External Postgres** — set `postgres.enabled=false` and put `DATABASE_URL` in the sealed secret. The chart's `postgres.*` block is then ignored.

## Values

This table is hand-maintained against `values.yaml`. Update both when adding or renaming keys. See [Keeping the values table in sync](#keeping-the-values-table-in-sync) below if you'd like to automate this with `helm-docs`.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `replicaCount` | int | `1` | Number of backend / frontend pods. Leave at 1 — backend uses RWO storage. |
| `publicBaseURL` | string | `"https://ai-plugins.oglimmer.com"` | Public HTTPS URL. Exposed to backend as `PUBLIC_BASE_URL` and embedded in `marketplace.json`. Must be `https://`. |
| `marketplaceName` | string | `"oglimmer-marketplace"` | Name shown in `marketplace.json` (also used as the owner name). |
| `defaultLicense` | string | `"MIT"` | Default license prefilled in the "new plugin" form. |
| `anthropic.model` | string | `"claude-sonnet-4-6"` | Model id used by `/api/skills/validate`. The API key itself goes in the sealed secret as `ANTHROPIC_API_KEY`. |
| `auth.mode` | string | `"oidc"` | `password` (built-in email/password + JWT) or `oidc` (OIDC Authorization Code flow). |
| `auth.oidc.issuerURL` | string | `"https://id.oglimmer.de/realms/oglimmer"` | OIDC discovery URL. Required when `auth.mode=oidc`. |
| `auth.oidc.clientID` | string | `"plugin-skill-hosting"` | OIDC client id. Required when `auth.mode=oidc`. |
| `auth.oidc.redirectURL` | string | `""` | Defaults to `${publicBaseURL}/api/auth/oidc/callback` when empty. |
| `auth.oidc.scopes` | string | `"openid email profile"` | Space-separated OIDC scopes. |
| `backend.image.repository` | string | `"registry.oglimmer.com/plugin-skill-hosting-backend"` | Backend container image repository. |
| `backend.image.tag` | string | `"latest"` | Backend image tag. Pin to a git sha in production for clean rollbacks. |
| `backend.image.pullPolicy` | string | `"Always"` | Backend image pull policy. |
| `backend.service.type` | string | `"ClusterIP"` | Backend Service type. |
| `backend.service.port` | int | `8080` | Backend Service port. |
| `backend.persistence.enabled` | bool | `true` | Mount a PVC at `/data` for bare git repos and worktrees. |
| `backend.persistence.size` | string | `"5Gi"` | Backend PVC size. |
| `backend.persistence.storageClass` | string | `""` | StorageClass for the backend PVC. Empty uses the cluster default. |
| `backend.persistence.accessMode` | string | `"ReadWriteOnce"` | Backend PVC access mode. |
| `backend.resources` | object | requests 100m/128Mi, limits 500m/512Mi | Backend container resources. |
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
| `ingress.hosts` | list | one host with `/api`, `/git`, `/mcp`, `/marketplace.json`, `/healthz` → backend, `/` → frontend | Hosts and path → backend mapping. **Path order matters** — backend prefixes must precede the `/` catch-all. |
| `ingress.tls` | list | one entry | TLS hosts and the secret cert-manager populates. |
| `serviceAccount.create` | bool | `true` | Create a ServiceAccount for the pods. |
| `serviceAccount.annotations` | object | `{}` | Annotations on the ServiceAccount. |
| `serviceAccount.name` | string | `""` | Override the generated ServiceAccount name. |
| `podSecurityContext.fsGroup` | int | `10001` | fsGroup matching the backend UID — required for the `/data` PVC to be group-writable. |
| `securityContext` | object | non-root, runAsUser 10001, drop ALL caps | Backend container security context. |
| `frontendSecurityContext` | object | drop ALL caps | Frontend container security context. |
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
