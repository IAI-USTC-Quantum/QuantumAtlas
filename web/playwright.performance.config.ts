import { fileURLToPath } from 'node:url'
import { defineConfig, devices } from '@playwright/test'

// Opt-in only: the ordinary config is scoped to tests/browser, not this tree.
// Build separately; never rebuild between the first and last baseline sample.
const label = process.env.MARKDOWN_BENCH_LABEL
if (!label || !/^[a-z0-9][a-z0-9_-]*$/.test(label)) {
  throw new Error('Set MARKDOWN_BENCH_LABEL to a safe explicit label (worker-baseline or react-markdown).')
}
const webRoot = fileURLToPath(new URL('.', import.meta.url))
const resultRoot = fileURLToPath(new URL(`../build/react-markdown-results/${label}/`, import.meta.url))

export default defineConfig({
  testDir: './tests/performance',
  testMatch: '**/*.spec.ts',
  outputDir: `${resultRoot}/artifacts`,
  fullyParallel: false,
  forbidOnly: true,
  retries: 0,
  workers: 1,
  timeout: 90_000,
  expect: { timeout: 15_000 },
  reporter: [
    ['list'],
    ['json', { outputFile: `${resultRoot}/playwright.json` }],
    ['./tests/performance/reporter.ts', { outputDir: resultRoot }],
  ],
  use: {
    baseURL: 'http://127.0.0.1:4177',
    serviceWorkers: 'block',
    storageState: { cookies: [], origins: [] },
    timezoneId: 'UTC',
    contextOptions: { reducedMotion: 'reduce' },
    // Tracing/video/screenshots change timings. Network and metrics remain attached.
    trace: 'off',
    screenshot: 'off',
    video: 'off',
  },
  projects: [{
    name: 'chromium-performance',
    use: {
      ...devices['Desktop Chrome'],
      viewport: { width: 1280, height: 900 },
      launchOptions: {
        args: ['--disable-background-networking', '--disable-component-update', '--no-proxy-server'],
      },
    },
  }],
  webServer: {
    command: `${JSON.stringify(process.execPath)} tests/browser/static-preview.mjs`,
    cwd: webRoot,
    url: 'http://127.0.0.1:4177',
    reuseExistingServer: false,
    timeout: 30_000,
    gracefulShutdown: { signal: 'SIGTERM', timeout: 5_000 },
  },
})
