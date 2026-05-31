# Security hardening plan — session & token layer

Status: **S1–S8 done** (only the deferred S5 browser session-shortening remains, optional) · Last reviewed: 2026-05-31

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

#### S3. No session / token revocation ("logout" is client-side only) ✅ DONE
- **Status:** Implemented. Migration `0014_token_version.sql` adds a per-user
  `token_version`; it's stamped into session JWTs and OAuth access tokens as the
  `ver` claim and compared in `resolveToken` (absent ⇒ 0, so pre-upgrade tokens
  keep working). A new `POST /api/me/sessions/revoke` ("sign out everywhere")
  bumps the counter, invalidating all of the user's tokens; the SPA exposes it
  in the connect tab and logs out locally, and `App.vue` now logs out + redirects
  on a startup 401 so *other* devices clean up on reload. Verified end-to-end
  against Postgres (register → /me 200 → revoke 204 → /me 401 "token revoked").
- **Scope note:** Admin reject/delete/demote do **not** bump the version — `status`
  and `is_admin` are read fresh from the row each request, so those already take
  effect immediately. `token_version` covers only the case nothing else does:
  revoking the sessions of an account that stays approved (lost/leaked token).
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

#### S4. API tokens are stored in plaintext at rest ✅ DONE
- **Status:** Implemented as **reversible encryption with a blind index** (chosen
  to keep the token re-displayable everywhere, unlike a one-way hash). Migration
  `0015_api_token_encryption.sql` adds `api_token_hash` (SHA-256, deterministic —
  backfilled in SQL via pgcrypto) for lookups and `api_token_enc` (AES-256-GCM)
  for display, and relaxes the plaintext column's NOT NULL/UNIQUE. The app
  encrypts the ciphertext + clears plaintext at startup (`BackfillAPITokenCiphertext`).
  Key from `API_TOKEN_ENC_KEY` or derived from `JWT_SECRET` (`deriveAPITokenKey`).
  Because auth uses the hash, the key governs *display only* — a key change never
  locks anyone out. Verified end-to-end: legacy plaintext token migrated +
  still authenticates; new users store no plaintext; auth + re-display + rotate
  all work. Covered by config + crypto unit tests.
- **Follow-up:** a later migration should `DROP COLUMN api_token` once every
  deployment has booted post-0015 (the column is now always NULL).
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

#### S5. Browser token lifetime is long and XSS-exfiltratable ✅ DONE (audit) · session-shortening deferred
- **Status:** **Markdown XSS audit found no sink** — the frontend has no `v-html`
  /`innerHTML`, the Milkdown/Crepe editor is ProseMirror-based (no raw-HTML
  passthrough), and all skill content (body, descriptions, diff/compare) is
  rendered via Vue's auto-escaping `{{ }}`. So user markdown is never executed as
  HTML today. Defense-in-depth **CSP/security headers were added** (see S7) to
  block inline/injected script even if a future change introduces a sink.
  - **Deferred (tracked, not done):** shortening the browser session to a
    short-lived access token + refresh (item 3 below). It's a sizable change and
    the immediate XSS exposure is low (no sink + CSP + S3 revocation now exists),
    so it's intentionally left as a follow-up rather than bundled here.
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

#### S6. No rate limiting on shared/abuse-prone endpoints ✅ DONE
- **Status:** Implemented with `go-chi/httprate` per-IP limiters (keyed off the
  real client IP via the existing `RealIP` middleware), scoped to the sensitive
  endpoints rather than globally — the marketplace's org users share egress IPs
  and the SPA/git/MCP paths are chatty, so a global per-IP cap would throttle
  legitimate use; volumetric DoS stays the ingress/CDN's job. Buckets:
  `/oauth/authorize` + `/oauth/token` and the sign-in endpoints (`/api/auth/*`)
  at **60/min**, the self-service `/me/token/regenerate` + `/me/sessions/revoke`
  at **20/min**. Discovery `.well-known` and read/data APIs are left unthrottled.
  Verified by a burst test asserting 429 past the bucket. (Limits are hardcoded;
  making them env-tunable is a possible follow-up.)
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

#### S7. Missing HTTP security headers ✅ DONE
- **Status:** Implemented in two layers. **(1) nginx** (`frontend/nginx.conf` +
  `frontend/nginx-security-headers.conf`, copied to `/etc/nginx/snippets/` by the
  Dockerfile) sets the SPA-document headers: a CSP with `script-src 'self'`
  (Vite emits only external scripts) + `style-src 'self' 'unsafe-inline'` (the
  Crepe editor injects inline styles), `frame-ancestors 'none'`,
  `X-Content-Type-Options`, `X-Frame-Options`, `Referrer-Policy`, and HSTS gated
  on `X-Forwarded-Proto: https` (so localhost dev is never pinned). The snippet is
  re-included in the `/assets/` and `=/index.html` blocks because nginx replaces
  rather than merges inherited `add_header`s. **(2) Backend** (`securityHeaders`
  middleware in `router.go`) sets `nosniff` / `X-Frame-Options` / `Referrer-Policy`
  and a strict `default-src 'none'` CSP on every API/OAuth/error response — this
  covers production, where the Ingress routes those paths straight to the backend,
  bypassing nginx. Verified: headers emitted live via nginx (HSTS only with XFP
  https); backend covered by a router test.
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

#### S8. OIDC account-linking trusts unverified email ✅ DONE
- **Status:** Fixed. `findOrCreateOIDCUser` now links by email only when
  `email_verified` is explicitly true (absent/false fails closed); an unverified
  email that collides with an existing account is refused with a distinct
  `email_conflict` reason instead of either linking (takeover) or leaking a raw
  DB error. All OIDC sign-in failures were also reworked to emit **stable reason
  codes** (`provider_error`, `domain_not_allowed`, `account_rejected`,
  `account_deleted`, `email_conflict`, `account_error`) instead of raw text, and
  the workspace-domain rejection now redirects to the SPA callback rather than
  returning a raw JSON 401. `OIDCCallbackView.vue` renders friendly,
  per-reason pages with appropriate retry/contact-admin guidance.
- **Also caught & fixed here:** a latent **S4 regression** — `findOrCreateOIDCUser`
  still `SELECT`ed the (now-NULL) plaintext `api_token` column, which would have
  broken *all* OIDC logins after migration 0015 by scanning NULL into a string.
  Both selects now read `api_token_enc` and decrypt via the shared
  `apiTokenForDisplay` helper. (Missed in S4 because the OIDC path can't be
  driven without a live IdP; surfaced while building the S8 integration test.)
- **Verified:** DB-backed integration test (`TestFindOrCreateOIDCUser_EmailVerificationGating`,
  gated on `TEST_DATABASE_URL`) confirms unverified+collision is rejected with the
  victim's binding intact, and verified links correctly; plus a no-DB test that a
  failed callback redirects with a stable reason code.
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

1. ✅ **S1 + S2** — config guards (done).
2. ✅ **S3 (token_version revocation)** — done.
3. ✅ **S4 (encrypt API tokens at rest)** — done.
4. ✅ **S5 audit + S7 headers** — done (no XSS sink found; CSP added).
5. ✅ **S6 (rate limiting)** — done.
6. ✅ **S8 (OIDC unverified-email linking)** — done (+ fixed a latent S4
   OIDC-login regression found along the way).
7. **Remaining:** only the **deferred S5 session-shortening** (browser
   short-token + refresh), optional, when prioritised.

## Out of scope (password mode is dev-only)

Login rate limiting, login timing equalization, registration enumeration, and
password complexity are **not** planned, because production runs `AUTH_MODE=oidc`
and the password flow never serves real users. If that ever changes, this
section must be revisited and those items promoted to P0.
