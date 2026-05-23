# Terraform — AWS deployment

**Optional.** This is one of several ways to run plugin-skill-hosting — see
[`compose.yml`](../compose.yml) for local Docker and
[`helm/`](../helm/plugin-skill-hosting/) for Kubernetes. Use this module if
you want a turn-key AWS deployment on managed services.

Single root module; flat layout — every `.tf` file is a chunk of the same
plan.

> **No custom DNS support yet.** The stack serves traffic from CloudFront's
> default `*.cloudfront.net` hostname over its bundled certificate. Adding
> a custom domain (Route53 zone, ACM cert in us-east-1, CloudFront
> `aliases`, viewer cert switch) is not wired up here. The CloudFront URL
> is HTTPS and Claude Code accepts it as a plugin source, so the stack is
> fully functional without a domain.

## Architecture

```
                 viewer (HTTPS)
                       │
                  ┌────▼─────────┐
                  │  CloudFront  │  *.cloudfront.net, default cert
                  │   + WAFv2    │
                  └──┬────────┬──┘
   default ("/")     │        │  /api/*, /git/*, /mcp*, /marketplace.json,
   ┌─────────────────┘        │  /healthz, /readyz
   ▼                          ▼
┌────────┐                ┌───────────────────┐
│ S3 SPA │                │  ALB (HTTP :80)   │
│  OAC   │                │  CF prefix list + │
└────────┘                │  X-CloudFront-    │
                          │  Secret header    │
                          └─────────┬─────────┘
                                    │
                          ┌─────────▼─────────┐
                          │ ECS Fargate task  │ Go backend :8080
                          │ (private subnets) │ rematerialize on start
                          └─────────┬─────────┘
                                    │
                          ┌─────────▼─────────┐
                          │  RDS Postgres 16  │ private, gp3, encrypted
                          │  (private subnets)│ Performance Insights on
                          └───────────────────┘
```

### Why these choices

- **Frontend on S3 + CloudFront.** The SPA uses `window.location.origin` and
  relative paths (`/api/*`, `/mcp`, `/git/*`). One CloudFront distribution
  routes the dynamic paths to the ALB origin and serves everything else from
  S3. No nginx container needed.
- **Backend on ECS Fargate, single task, ephemeral `/data`.** The chart's
  `rematerializeOnStartup` mode rebuilds every git repo from Postgres at
  startup, so per-task ephemeral storage is enough. Postgres is the source
  of truth. Cold-start cost: O(repos) on every task replacement.
- **RDS Postgres 16, `gp3`, encrypted, `force_ssl=1`.** Standard managed
  Postgres. Multi-AZ off by default — flip `db_multi_az = true` for prod.
- **ALB locked down two ways.** Security group ingress is restricted to
  AWS's `com.amazonaws.global.cloudfront.origin-facing` prefix list, and
  the listener rule rejects requests without an `X-CloudFront-Secret`
  header. Direct hits to the ALB DNS name return 403.
- **No custom domain (yet).** CloudFront's default cert covers
  `*.cloudfront.net` and Claude Code accepts HTTPS plugin sources regardless
  of domain. Adding a custom domain means replacing `viewer_certificate`
  with a us-east-1 ACM cert, setting `aliases = [...]`, and creating the
  Route53 alias record — not wired up in this module.

### Well-Architected coverage

| Pillar | What this enforces |
| --- | --- |
| Operational excellence | Container Insights, CloudWatch logs for ECS / VPC flow / RDS, deployment circuit breaker with auto-rollback, ECS Exec for debugging, structured outputs incl. deploy commands |
| Security | Private subnets for ECS+RDS, security groups least-privilege, no public IP on tasks, ALB ingress restricted to CloudFront prefix list + shared header, S3 OAC (no public reads), KMS-encrypted RDS + Secrets Manager with key rotation, `rds.force_ssl=1`, WAFv2 with managed rule sets + rate limit, VPC flow logs |
| Reliability | Multi-AZ subnets, optional Multi-AZ RDS, 7-day automated backups, deletion protection, ECS rolling deploy with circuit breaker, ALB health check on `/readyz` so traffic only flows once rematerialization finishes |
| Performance | Graviton (ARM64) Fargate, CloudFront caching for static assets, HTTP/2+3, IPv6, RDS Performance Insights, ALB idle timeout aligned with MCP SSE streams |
| Cost | Single NAT gateway by default, `db.t4g.micro` default, `PriceClass_100` (US/EU edges), `gp3` storage, S3 lifecycle expiry (90d logs, 30d noncurrent versions), Fargate Spot capacity provider available |
| Sustainability | ARM64 by default, right-sized defaults, log retention bounded |

## Usage

### Prerequisites

- Terraform ≥ 1.11 (uses cross-variable validation, `dynamic` blocks)
- AWS credentials with admin or equivalent (first run creates IAM, KMS, VPC,
  RDS, ECS, CloudFront, WAFv2 in us-east-1 + chosen region)
- A built backend image already pushed somewhere ECS can pull from
  (default points at `ghcr.io/oglimmer/plugin-skill-hosting-backend:latest`;
  GHCR is public so no auth is needed)
- Node + npm locally (only to build the SPA after `terraform apply`)

### First apply

```bash
cd terraform
cp terraform.tfvars.example terraform.tfvars
# Edit terraform.tfvars — at minimum set jwt_secret (32+ chars).
# Generate one with: openssl rand -base64 48 | tr -d '/+=' | head -c 64

terraform init
terraform plan -out=tfplan
terraform apply tfplan
```

Roughly 15-25 minutes — RDS provisioning dominates.

Find the public URL with `terraform output cloudfront_domain` — that's the
`*.cloudfront.net` hostname to point Claude Code at.

### Deploy the SPA

`terraform output deploy_spa_commands` prints the exact commands for the
current bucket and distribution. Roughly:

```bash
cd frontend && npm ci && npm run build

aws s3 sync dist/ s3://$(cd terraform && terraform output -raw frontend_bucket)/ \
  --delete \
  --cache-control "public, max-age=31536000, immutable" \
  --exclude "index.html"

aws s3 cp dist/index.html s3://$(cd terraform && terraform output -raw frontend_bucket)/index.html \
  --cache-control "no-cache, no-store, must-revalidate"

aws cloudfront create-invalidation \
  --distribution-id $(cd terraform && terraform output -raw cloudfront_distribution_id) \
  --paths "/index.html" "/"
```

### Rolling a new backend image

```bash
# Bump the image (or pin a tag) and re-apply — TF rewrites the task def
# and ECS does a rolling deploy with the circuit breaker armed.
terraform apply -var "backend_image=ghcr.io/oglimmer/plugin-skill-hosting-backend:v1.2.3"
```

### Bringing an existing VPC

```hcl
create_vpc                  = false
existing_vpc_id             = "vpc-0abc..."
existing_public_subnet_ids  = ["subnet-1...", "subnet-2..."]
existing_private_subnet_ids = ["subnet-3...", "subnet-4..."]
```

### Tear-down

```bash
terraform apply -var "db_deletion_protection=false"
terraform destroy
```

RDS final snapshot is taken as `<project>-<env>-db-final` when
`db_deletion_protection = true` and skipped otherwise.

## Notes / known limits

- **MCP stream length.** CloudFront's default origin read timeout is 60s.
  The helm chart uses 3600s; matching it requires an AWS service quota
  increase for "Origin response timeout".
- **Single backend task.** `backend_desired_count` and `backend_max_count`
  should both stay at 1 unless the backend grows shared storage (EFS) — the
  current design keeps git repos on per-task ephemeral disk.
- **First task start time.** ALB health check grace period defaults to 300s
  to cover rematerialization. Bump it for marketplaces with many plugins.
- **Secret rotation.** `jwt_secret` and the DB password are written once at
  apply time. Rotate via Secrets Manager (the secret JSON contains the full
  `DATABASE_URL`) and restart the ECS service to pick up changes.
