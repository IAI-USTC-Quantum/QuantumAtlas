import { fileURLToPath } from 'node:url'
import { defineConfig, devices } from '@playwright/test'

// Build first; this suite never starts a backend or a development proxy.
// The helper intentionally bypasses vite.config.ts and every .env file.
const webRoot = fileURLToPath(new URL('.', import.meta.url))
const baseURL = 'http://127.0.0.1:4177'

export default defineConfig({
  testDir: './tests/browser',
  testMatch: '**/*.spec.ts',
  outputDir: './test-results/browser',
  fullyParallel: true,
  forbidOnly: Boolean(process.env.CI),
  retries: 0,
  workers: 2,
  timeout: 30_000,
  expect: { timeout: 10_000 },
  reporter: [['list'], ['html', { outputFolder: 'test-results/browser-report', open: 'never' }]],
  use: {
    baseURL,
    serviceWorkers: 'block',
    storageState: { cookies: [], origins: [] },
    timezoneId: 'UTC',
    contextOptions: { reducedMotion: 'reduce' },
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [{
    name: 'chromium',
    use: {
      ...devices['Desktop Chrome'],
      launchOptions: {
        args: ['--disable-background-networking', '--disable-component-update', '--no-proxy-server'],
      },
    },
  }],
  webServer: {
    command: `${JSON.stringify(process.execPath)} tests/browser/static-preview.mjs`,
    cwd: webRoot,
    url: baseURL,
    reuseExistingServer: false,
    timeout: 30_000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 5_000 },
  },
})
