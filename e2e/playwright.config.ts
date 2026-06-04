import { defineConfig, devices } from '@playwright/test'

// The full stack is served by the frontend nginx (which reverse-proxies /api,
// /git, /mcp to the backend), so a single origin covers every journey.
const baseURL = process.env.E2E_BASE_URL || 'http://localhost:8080'

export default defineConfig({
  testDir: './tests',
  // The journeys mutate shared global state (the first registered user becomes
  // admin; plugin names are globally unique), so the suite is sequential and
  // relies on global-setup starting from a freshly-reset stack. Don't parallelise.
  fullyParallel: false,
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',

  // Bring the docker-compose stack up once before the suite and tear it down
  // after. Set E2E_NO_STACK=1 to point at an already-running stack instead
  // (then E2E_BASE_URL controls the target).
  globalSetup: './global-setup.ts',
  globalTeardown: './global-teardown.ts',

  timeout: 30_000,
  expect: { timeout: 10_000 },

  use: {
    baseURL,
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
  },

  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],
})
