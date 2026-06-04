import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const composeFile = resolve(__dirname, '..', 'compose.yml')
const baseURL = process.env.E2E_BASE_URL || 'http://localhost:8080'

function compose(args: string[]) {
  execFileSync('docker', ['compose', '-f', composeFile, ...args], {
    stdio: 'inherit',
  })
}

async function waitForReady(url: string, timeoutMs: number) {
  const deadline = Date.now() + timeoutMs
  // /api/auth/config is unauthenticated and only returns once the backend has
  // finished booting + migrating, so it's the cleanest readiness signal for the
  // whole stack (a 200 means nginx, the backend, and the DB are all live).
  const probe = `${url}/api/auth/config`
  let lastErr: unknown
  while (Date.now() < deadline) {
    try {
      const res = await fetch(probe)
      if (res.ok) return
      lastErr = new Error(`status ${res.status}`)
    } catch (e) {
      lastErr = e
    }
    await new Promise((r) => setTimeout(r, 2000))
  }
  throw new Error(`stack not ready at ${probe} after ${timeoutMs}ms: ${lastErr}`)
}

export default async function globalSetup() {
  if (process.env.E2E_NO_STACK === '1') {
    console.log(`[e2e] E2E_NO_STACK=1 — using existing stack at ${baseURL}`)
    await waitForReady(baseURL, 30_000)
    return
  }

  // Reset first: a clean DB is part of the suite's contract (first registered
  // user must be the admin; plugin names start unused).
  console.log('[e2e] resetting stack (down -v)…')
  compose(['down', '-v', '--remove-orphans'])

  console.log('[e2e] starting stack (up --build -d)…')
  const upArgs = ['up', '-d']
  if (process.env.E2E_SKIP_BUILD !== '1') upArgs.push('--build')
  compose(upArgs)

  console.log(`[e2e] waiting for ${baseURL} to become ready…`)
  await waitForReady(baseURL, 180_000)
  console.log('[e2e] stack ready.')
}
