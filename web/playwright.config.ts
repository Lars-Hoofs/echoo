import { defineConfig, devices } from '@playwright/test'

// Runs against a live stack (docker compose). README.md ("End-to-end tests") has the exact commands.
export default defineConfig({
  testDir: 'e2e',
  fullyParallel: false,
  workers: 1,
  // A sign-in may wait up to a minute for a free slot under the login rate limit (e2e/session.ts).
  timeout: 120_000,
  forbidOnly: !!process.env.CI,
  // CI also writes a JSON report, which the workflow reads to fail on skipped tests: a spec that
  // silently skips for lack of configuration would otherwise look like a pass.
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }], ['json', { outputFile: 'test-results/results.json' }]] : 'list',
  use: {
    baseURL: process.env.ECHOO_E2E_URL ?? 'http://localhost:8080',
    trace: 'retain-on-failure',
  },
  projects: [{ name: 'chromium', use: { ...devices['Desktop Chrome'] } }],
})
