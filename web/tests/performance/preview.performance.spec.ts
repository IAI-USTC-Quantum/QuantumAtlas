import { createHash } from 'node:crypto'
import { readFileSync, readdirSync } from 'node:fs'
import { createRequire } from 'node:module'
import { cpus, platform, arch, totalmem } from 'node:os'
import { fileURLToPath } from 'node:url'
import { test, expect, PAPER_ID, expectExactSource } from '../browser/fixtures'
import { corpus, CORPUS_VERSION, slowCaseIds, TERMINAL_SENTINEL } from './corpus'
import { installProbe, readProbe, settlePage } from './probe'

const require = createRequire(import.meta.url)
const playwrightVersion = (require('@playwright/test/package.json') as { version: string }).version
const PINNED_PLAYWRIGHT = '1.62.0'
const PINNED_CHROMIUM = '151.0.7922.34'
const label = process.env.MARKDOWN_BENCH_LABEL!
const dist = fileURLToPath(new URL('../../dist/', import.meta.url))
const buildFiles = ['index.html', ...readdirSync(`${dist}/assets`).sort().map((file) => `assets/${file}`)]
const buildHash = createHash('sha256')
for (const file of buildFiles) buildHash.update(file).update('\0').update(readFileSync(`${dist}/${file}`))
const distSha256 = buildHash.digest('hex')
const metadata = {
  label, corpusVersion: CORPUS_VERSION, distSha256, buildFiles,
  node: process.version, playwrightVersion, platform: platform(), arch: arch(),
  logicalCPUs: cpus().length, cpuModel: cpus()[0]?.model, totalMemoryBytes: totalmem(),
  viewport: { width: 1280, height: 900 },
  scope: 'Synthetic paper-detail API fixture, local static production dist, headless Chromium; no real auth/backend/storage/external network. Not a production latency SLA.',
  methodology: 'One browser process; new context and page per CPU/corpus/repeat. First-open includes lazy renderer, document fetch, font load; reopen uses same page/module/font caches but refetches document and remounts preview (gcTime:0). HTTP cache disabled by routing/no-store. Timed capture-phase actual click through rendered/rejected DOM then fonts.ready and two rAF callbacks (paint opportunity, not a screenshot/raster guarantee). DOM counts collected after timing fence. Three measured repeats; no discarded warmups.',
}
const rates = process.env.MARKDOWN_BENCH_NATIVE_ONLY === '1' ? [1] : [1, 4]

test.describe.configure({ mode: 'default' })
for (const cpuRate of rates) {
  for (const sample of corpus) {
    if (cpuRate === 4 && !slowCaseIds.has(sample.id)) continue
    for (let repeat = 1; repeat <= 3; repeat++) {
      test(`${sample.id} / cpu-${cpuRate}x / repeat-${repeat}`, async ({ page, context, browser, api }, testInfo) => {
        expect(playwrightVersion, 'Explicitly update benchmark pin when changing browser toolchain').toBe(PINNED_PLAYWRIGHT)
        expect(browser.version(), 'Never silently compare different Chromium versions').toBe(PINNED_CHROMIUM)
        api.documents[PAPER_ID] = { title: `Synthetic benchmark ${sample.id}`, markdown: sample.markdown }
        await page.goto(`/en/papers/${PAPER_ID}`)
        const open = page.getByRole('button', { name: 'Preview Markdown', exact: true })
        await expect(open).toBeVisible()
        await settlePage(page)
        const cdp = await context.newCDPSession(page)
        await cdp.send('Emulation.setCPUThrottlingRate', { rate: cpuRate })
        await settlePage(page)

        for (const cacheState of ['first-open', 'reopen'] as const) {
          const fetchesBefore = api.deliveredAssets.length
          await installProbe(page, TERMINAL_SENTINEL)
          await open.click()
          const result = await readProbe(page)
          const assetFetches = api.deliveredAssets.length - fetchesBefore
          await testInfo.attach(`measurement-${cacheState}`, {
            body: JSON.stringify({
              ...metadata, browserVersion: browser.version(), cpuRate, caseId: sample.id,
              dimensions: sample.dimensions, repeat, cacheState, assetFetches, ...result,
            }, null, 2),
            contentType: 'application/json',
          })
          expect(result.longTasksSupported, 'A missing longtask API must not become a zero-blocking result').toBe(true)
          expect(result.outcome, 'An operational timeout is not rendering or a controlled rejection').not.toBe('timeout')
          expect(assetFetches, 'The actual paper route intentionally refetches on both first-open and reopen').toBe(1)
          if (result.outcome === 'success') {
            expect(result.hasTerminalSentinel, 'A partial document is not success').toBe(true)
            expect(result.formulaCount, 'All formula delimiters must render, except literals in fenced code').toBe(sample.dimensions.expectedFormulas)
            expect(result.tableCount).toBe(sample.dimensions.expectedTables)
            expect(result.codeBlockCount).toBe(sample.dimensions.expectedCodeBlocks)
            expect(result.headingCount).toBe(sample.dimensions.expectedHeadings)
            expect(result.domElements).toBeGreaterThan(0)
          } else {
            expect(result.rejectionText, 'Controlled rejection must be visible, not silent').toBeTruthy()
            expect(result.domElements, 'Rejected content is not a rendered document').toBe(0)
          }
          // Correct source preservation remains mandatory even on tree/size rejection.
          // This check runs OUTSIDE the render timing window.
          await expectExactSource(page, 'en', sample.markdown)
          await page.keyboard.press('Escape')
          await expect(page.getByRole('dialog')).toHaveCount(0)
          await settlePage(page)
        }
        await cdp.detach()
      })
    }
  }
}
