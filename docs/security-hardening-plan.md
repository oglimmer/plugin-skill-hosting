# Security hardening plan — session & token layer

Status: **in progress** (S1 + S2 done; S3–S8 pending) · Last reviewed: 2026-05-30

## Scope

This plan covers security findings in the **shared session/token layer and the
production (OIDC + OAuth/MCP) auth paths**. It deliberately **excludes**
findings that are specific to the `password` sign-in flow, because that flow is
**development-only** and unsupported in production (see README → *Authentication*).

Password-mode-only items intentionally **out of scope** here: login brute-force
/ rate limiting on `/api/auth/login`, login timing oracle, open registration &
username/email enumeration on `/api/auth/register`, and password complexity
policy. They are not fixed because production never enables that flow.

Everything below applies regardless of `AUTH_MODE`, because both modes converge
on the same 30-day browser JWT (`issueToken`), the same long-lived API token,
and the same OAuth/MCP machinery.

---

## Findings & fixes

### P0 — do first (high impact, low effort/risk)

#### S1. `JWT_SECRET` has an insecure hardcoded default and is never validated ✅ DONE
- **Status:** Implemented. `config.Load` now rejects the in-repo default or any
  secret `< 32` chars via `insecureJWTSecret`, with an `ALLOW_INSECURE_JWT_SECRET=true`
  dev escape hatch (set in `compose.yml`). Covered by `TestInsecureJWTSecret`.
- **Severity:** High
- **Where:** `backend/internal/config/config.go:102` (default
  `"dev-secret-change-me-please-32-chars-min"`); consumed by
  `issueToken`/`issueShortToken`/`parseToken` in `backend/internal/server/auth.go`.
- **Risk:** If a deployment forgets to set `JWT_SECRET`, every session JWT, OAuth
  access token, and short token is signed with a secret that is published in the
  source tree → anyone can forge a token for any user (including admins). There is
  currently **no startup check** on length, entropy, or whether the default is
  still in use. (The signing algorithm itself is safe — `parseToken` already
  rejects non-HMAC algs, blocking `alg:none`/RS↔HS confusion.)
- **Fix:**
  - Fail fast at startup (`log.Fatalf`) when `JWTSecret == <default>` **or**
    `len(JWTSecret) < 32`. Allow an explicit `ALLOW_INSECURE_JWT_SECRET=true`
    escape hatch for local dev only.
  - Same treatment for any other secret with a baked-in default.
- **Effort:** ~1 hr. **Verification:** unit test on `config.Load` for the
  fatal/non-fatal cases; manual boot without `JWT_SECRET` set.

#### S2. CORS allows any origin ✅ DONE
- **Status:** Implemented. The wildcard is replaced by `config.AllowedOrigins`,
  derived by `deriveAllowedOrigins`: explicit `CORS_ALLOWED_ORIGINS` wins;
  otherwise `*` for localhost/loopback (Vite dev) and the app's own origin for a
  real host. Covered by `TestDeriveAllowedOrigins`. A startup WARN fires when the
  effective list is `*`.
- **Severity:** Low–Medium
- **Where:** `backend/internal/server/router.go:24` (`AllowedOrigins: ["*"]`).
- **Risk:** Currently low-impact because auth is `Authorization`-header based with
  `AllowCredentials:false`, so a wildcard origin can't ride ambient cookies. But
  it's a permissive default that becomes dangerous the moment any cookie-based
  auth is introduced (see S5/S4 below), and it allows any site to script
  token-bearing calls if a token is obtained.
- **Fix:** Replace `*` with an allowlist derived from `PUBLIC_BASE_URL` (+ an
  optional `CORS_ALLOWED_ORIGINS` env). Make this change *before* S5 if cookie
  storage is adopted.
- **Effort:** ~1 hr. **Verification:** preflight from an allowed vs disallowed origin.

### P1 — high value, moderate effort

#### S3. No session / token revocation ("logout" is client-side only)
- **Severity:** High
- **Where:** `auth.go` `issueToken`/`parseToken`; frontend `stores/auth.ts`
  `logout()` (clears `localStorage` only).
- **Risk:** Browser JWTs are stateless with no server-side check beyond the
  signature + `exp`. Consequences:
  - Logout does **not** invalidate the token server-side — it stays valid for the
    full 30 days.
  - Changing identity state (password change in dev, account
    suspension/rejection, regenerating the API token) does **not** invalidate
    existing JWTs.
  - There is **no "sign out everywhere"** and **no way to kill a leaked token**
    short of rotating `JWT_SECRET`, which logs out every user at once.
  - This is the standard best-practice gap behind "invalidate sessions on
    credential change."
- **Fix:** Add a `token_version INTEGER NOT NULL DEFAULT 0` column to `users`.
  Embed `ver` in the JWT claims at issue time; in `parseToken`/`resolveToken`
  compare the claim against the current DB value and reject on mismatch. Bump
  `token_version` to revoke all of a user's sessions — wire it into:
  password change, admin reject/delete/demote, and a new "sign out everywhere"
  action. (`userByID` is already on the hot path, so the version is available
  without an extra query.)
- **Effort:** ~0.5–1 day (migration + claim + invalidation hooks + tests).
- **Verification:** issue token → bump version → assert old token now 401s;
  assert admin-reject immediately invalidates an active session.

#### S4. API tokens are stored in plaintext at rest
- **Severity:** High–Medium
- **Where:** `auth.go:231` `userByAPIToken` does a direct equality match on the
  `api_token` column; tokens are generated by `generateAPIToken()` and stored
  verbatim. Contrast with OAuth refresh tokens, which are **already** stored as
  `sha256hex(...)` (`oauth.go:412`).
- **Risk:** The API token gates `/git`, `/mcp`, `/marketplace.json`, and the
  read APIs, and is long-lived with no expiry. A read-only DB compromise (backup
  leak, SQL injection elsewhere, log spill) hands the attacker directly usable
  credentials for every user.
- **Fix:** Store `sha256(token)` (tokens are high-entropy random, so a fast hash
  is appropriate — no bcrypt needed) and look up by hash, mirroring the refresh-
  token design. Migration: add `api_token_hash`, backfill is impossible for
  existing plaintext tokens, so rotate-on-next-use or force a one-time
  regeneration. Stop returning the raw token after creation except once.
- **Effort:** ~1 day (migration + lookup change + rollout strategy).
- **Verification:** new token authenticates; DB row shows only the hash.

#### S5. Browser token lifetime is long and XSS-exfiltratable
- **Severity:** Medium
- **Where:** 30-day lifetime `auth.go:22`; stored in `localStorage`
  (`frontend/src/stores/auth.ts`, `frontend/src/api.ts`).
- **Risk:** `localStorage` is readable by any injected script. Because skills
  render **user-authored markdown** (Milkdown/Crepe), the XSS surface is real;
  combined with the 30-day, currently-unrevocable token (S3), one XSS yields a
  month-long credential. Compounds S3.
- **Fix (layered):**
  1. **Confirm/enforce markdown sanitization** on every render path of
     user-supplied skill content (audit the Milkdown/Crepe pipeline; ensure no
     raw HTML passthrough). This is the highest-leverage XSS control.
  2. Add **security headers** (see S7) including a CSP that blocks inline/script
     injection.
  3. Shorten the session: pair a short-lived (e.g. 1 h) access token with a
     server-side refresh — the OAuth path already implements exactly this
     pattern (`issueOAuthTokenPair`), so reuse it for the browser. This also
     makes S3's revocation cheap (revoke at refresh, like the OAuth gate does).
  4. Optionally move the session to an `HttpOnly`, `Secure`, `SameSite` cookie so
     script can't read it (requires the S2 CORS tightening and CSRF protection).
- **Effort:** sanitization audit ~0.5 day; refresh-token rotation ~1–2 days;
  cookie migration ~2 days (larger, do last).
- **Verification:** XSS probe in a skill body cannot read the token; expired
  access token transparently refreshes.

### P2 — defense in depth

#### S6. No rate limiting on shared/abuse-prone endpoints
- **Severity:** Medium
- **Where:** none of the routes in `router.go` are throttled.
- **Risk:** Independent of the (out-of-scope) password brute-force concern,
  several production endpoints deserve limits: `/oauth/token` (client-secret
  guessing, refresh abuse), `/api/auth/oidc/callback`, `/api/me/token/regenerate`,
  and general request flooding. API tokens are 32-byte random (brute-force
  infeasible) so this is primarily DoS/abuse hardening, not credential guessing.
- **Fix:** Add a per-IP limiter (e.g. `go-chi/httprate`) as router middleware,
  with tighter buckets on the auth/OAuth group. Front with the ingress/CDN rate
  limiter too where available.
- **Effort:** ~0.5 day. **Verification:** burst test returns 429 past the bucket.

#### S7. Missing HTTP security headers
- **Severity:** Low–Medium
- **Where:** `router.go` middleware stack — no security headers set.
- **Risk:** No `Content-Security-Policy`, `X-Content-Type-Options`,
  `Referrer-Policy`, `Strict-Transport-Security`, or frame-ancestors control.
  These are the cheap second line behind S5's sanitization.
- **Fix:** Add a headers middleware: a strict CSP (script-src self, no inline),
  `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`,
  `Strict-Transport-Security` (when served over HTTPS), and
  `frame-ancestors 'none'`. Tune CSP against the Milkdown editor's needs.
- **Effort:** ~0.5 day (CSP tuning is the variable). **Verification:** header
  assertions in a router test; manual check the SPA + editor still load.

#### S8. OIDC account-linking trusts unverified email
- **Severity:** Low (context-dependent)
- **Where:** `oidc.go:372` — links an incoming OIDC identity to an existing user
  by email when `email_verified` is **absent or true**.
- **Risk:** With a generic/multi-tenant IdP that doesn't assert `email_verified`,
  an attacker could register an IdP account with a victim's email and get linked
  to the victim's existing marketplace account (account takeover). Low risk in
  the **Google Workspace-restricted** production config (domain allowlist +
  Google always sets the claim), but unsafe if a non-Google IdP is ever used.
- **Fix:** Only auto-link by email when `email_verified == true` (treat absent as
  *unverified* and fall through to new-account creation, or require explicit
  linking). Document that generic IdPs must assert `email_verified`.
- **Effort:** ~1 hr. **Verification:** callback with `email_verified:false`
  creates a new account instead of linking.

---

## Suggested sequencing

1. **S1 + S2** — same PR, both small config guards, immediate risk reduction.
2. **S3 (token_version revocation)** — unblocks proper logout and powers S5's
   refresh-based revocation.
3. **S4 (hash API tokens at rest)** — independent; schedule alongside S3's
   migration work.
4. **S5** — start with the markdown-sanitization audit (cheap, high value), then
   S7 headers, then the refresh-token migration.
5. **S6, S8** — fold in as smaller follow-ups.

## Out of scope (password mode is dev-only)

Login rate limiting, login timing equalization, registration enumeration, and
password complexity are **not** planned, because production runs `AUTH_MODE=oidc`
and the password flow never serves real users. If that ever changes, this
section must be revisited and those items promoted to P0.
