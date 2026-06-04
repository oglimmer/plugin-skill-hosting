import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'
import { dirname, resolve } from 'node:path'

const __dirname = dirname(fileURLToPath(import.meta.url))
const composeFile = resolve(__dirname, '..', 'compose.yml')

export default async function globalTeardown() {
  // When pointing at an externally-managed stack we never started it, so leave
  // it alone. Set E2E_KEEP_STACK=1 to keep a self-started stack up for
  // post-mortem debugging of a failed run.
  if (process.env.E2E_NO_STACK === '1' || process.env.E2E_KEEP_STACK === '1') {
    console.log('[e2e] leaving stack running.')
    return
  }
  console.log('[e2e] tearing down stack (down -v)…')
  execFileSync('docker', ['compose', '-f', composeFile, 'down', '-v', '--remove-orphans'], {
    stdio: 'inherit',
  })
}
