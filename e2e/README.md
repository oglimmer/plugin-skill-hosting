# End-to-end tests

Playwright tests that drive the **full stack** — the frontend nginx, the Go
backend, and Postgres, exactly as `compose.yml` builds them — through real user
journeys in a browser. This is the only layer that exercises the SPA, the HTTP
API, and the database together.

## What's covered

- `tests/auth.spec.ts` — protected-route redirect, first-user/admin registration,
  logout→login round-trip, wrong-password rejection.
- `tests/plugin-crud.spec.ts` — create a plugin, see it on its detail page and in
  the marketplace list, soft-delete it, confirm it leaves the active list.

## Running locally

Requires Docker (for the stack) and Node.

```bash
cd e2e
npm install
npx playwright install --with-deps chromium
npm test
```

`global-setup.ts` resets and rebuilds the stack (`docker compose down -v` then
`up --build -d`) and waits for `/api/auth/config` to answer; `global-teardown.ts`
tears it back down. A clean database is part of the contract — the first
registered account becomes the admin and plugin names are globally unique.

## Useful env vars

| Var | Effect |
| --- | --- |
| `E2E_NO_STACK=1` | Don't manage Docker; run against an already-running stack. |
| `E2E_BASE_URL` | Target origin (default `http://localhost:8080`). |
| `E2E_SKIP_BUILD=1` | `up -d` without `--build` (faster reruns of unchanged images). |
| `E2E_KEEP_STACK=1` | Leave a self-started stack up after the run for debugging. |

For a fast inner loop, start the stack once (`npm run stack:up`) and then run
`E2E_NO_STACK=1 E2E_SKIP_BUILD=1 npm test` repeatedly — but note the suite
assumes a fresh DB, so re-reset (`npm run stack:down && npm run stack:up`)
between full runs.
