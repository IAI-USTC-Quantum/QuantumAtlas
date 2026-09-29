import type { Locator, Page } from '@playwright/test'
import { readdirSync, readFileSync } from 'node:fs'
import { expect, test, READER_PAPER_ID } from './fixtures'

// Reader-workbench browser regression (plan §8 Q4): the three-pane
// prototype runs against the real production build with the mock reader
// dataset — no backend, no live API (VITE_READER_API unset ⇒ mock). These
// tests exercise acceptance-relevant behaviors: clickable bbox overlay,
// block→discussion wiring, independent discussions per anchor, status
// badges, filters, deep links back to the exact block, and bbox stability
// under zoom/rotation.

const WORKBENCH = '[data-testid="reader-workbench"]'

async function openReader(page: Page, language: 'en' | 'zh' = 'en') {
  await page.goto(`/${language}/papers/${READER_PAPER_ID}`)
  await expect(page.locator(WORKBENCH)).toBeVisible()
  await expect(page.getByTestId('reader-mock-badge')).toBeVisible()
  // The canvas reports the rendered page number through data-rendered.
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })
}

async function frameRatio(overlay: Locator, canvas: Locator): Promise<number> {
  const frame = (await overlay.boundingBox())!
  const surface = (await canvas.boundingBox())!
  expect(frame.width).toBeGreaterThan(4)
  expect(frame.height).toBeGreaterThan(4)
  return (frame.width * frame.height) / (surface.width * surface.height)
}

test('renders the pdf and a clickable overlay for non-contiguous blocks', async ({ page }) => {
  await openReader(page)
  // Page 1 of parse A: non-contiguous blocks 1, 2, 5 (§12.5).
  for (const index of [1, 2, 5]) {
    await expect(page.getByTestId(`block-overlay-${index}`)).toBeVisible()
  }
  await expect(page.getByTestId('block-overlay-3')).toHaveCount(0)

  // Block list mirrors the same blocks; block 2 carries 2 discussions.
  await expect(page.getByTestId('block-item-2')).toContainText('2 discussions')
  await expect(page.getByTestId('block-item-1')).toContainText('1 discussions')
})

// pdf.js ≥5.4.624 standard unconditionally calls Uint8Array.prototype.toHex()
// (Chrome 140+); on older browsers the PDF area goes white with
// "n.toHex is not a function". The official fix is the legacy build
// (Babel + core-js) for BOTH the main module and the worker. This local
// Chromium is too new to reproduce the crash, so we assert structurally on
// the served bytes: the worker the browser actually spawns, and the shipped
// main-thread chunk, must both carry the core-js payload.
test('the pdf.js worker and main module served to the browser are the legacy build', async ({ page }) => {
  await openReader(page)

  // The worker is spawned via new Worker(workerSrc); find its script among
  // the resource timing entries.
  const workerUrlHandle = await page.waitForFunction(() =>
    performance
      .getEntriesByType('resource')
      .map((entry) => entry.name)
      .find((url) => url.includes('pdf.worker')) ?? null,
  )
  let workerUrl = (await workerUrlHandle.jsonValue()) as string | null
  expect(workerUrl, 'a pdf.worker asset must have been requested').toBeTruthy()

  // Vite serves `?url` imports behind a tiny JS wrapper that re-exports the
  // real asset path; follow at most one hop to the actual worker bytes.
  let workerText = await page.evaluate(
    (target) => fetch(target).then((response) => response.text()),
    workerUrl as string,
  )
  if (!workerText.includes('core-js_shared__')) {
    const realPath = workerText.match(/\/assets\/[A-Za-z0-9._-]+\.mjs/)?.[0]
    expect(realPath, 'the worker wrapper must point at a real asset').toBeTruthy()
    workerUrl = new URL(realPath!, page.url()).href
    workerText = await page.evaluate(
      (target) => fetch(target).then((response) => response.text()),
      workerUrl,
    )
  }
  // core-js_shared__ only exists in the transpiled legacy build.
  expect(workerText).toContain('core-js_shared__')

  // Main thread: the production bundle must embed the legacy pdf.js module
  // (a standard-build chunk carries no core-js marker). Chunks may be .js or
  // .mjs; the 68-byte ?url wrappers are excluded by the marker requirement.
  const assetsDir = new URL('../../dist/assets/', import.meta.url)
  const chunks = readdirSync(assetsDir).filter(
    (name) => (name.endsWith('.mjs') || name.endsWith('.js')) && !name.includes('worker'),
  )
  const legacyChunks = chunks.filter((name) =>
    readFileSync(new URL(`../../dist/assets/${name}`, import.meta.url), 'utf8').includes('core-js_shared__'),
  )
  expect(legacyChunks.length, 'at least one main-thread chunk must carry core-js').toBeGreaterThan(0)
})

test('mock mode: the papers list degrades gracefully and launches the reader', async ({ page }) => {
  // With no backend, /api/papers would return the SPA fallback and crash
  // the JSON parse ("Unexpected token '<'"). Mock mode must serve the
  // synthetic list instead — and the fixture paper must click through into
  // a working reader. (The fixture boundary also fails the test if any
  // unmocked /api request is made from this page.)
  await page.goto('/zh/papers')
  await expect(page.getByTestId('papers-mock-badge')).toBeVisible()

  const fixtureRow = page.getByRole('link', { name: 'Synthetic Block-Comments Fixture Paper' })
  await expect(fixtureRow).toBeVisible()
  await fixtureRow.click()

  await expect(page.locator(WORKBENCH)).toBeVisible()
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })
})

test('selecting a block shows its two independent discussions', async ({ page }) => {
  await openReader(page)
  await page.getByTestId('block-overlay-2').click()

  const panel = page.getByTestId('discussion-panel')
  await expect(panel.getByTestId('discussion-card')).toHaveCount(2)
  // dsc_01 confirmed + dsc_02 pending: two statuses, one block, no merging.
  await expect(panel.locator('[data-discussion-id="dsc_01"] [data-status="confirmed"]')).toBeVisible()
  await expect(panel.locator('[data-discussion-id="dsc_02"] [data-status="pending"]')).toBeVisible()
  // Model declaration and account are shown as data.
  await expect(panel.locator('[data-discussion-id="dsc_01"]')).toContainText('agent-reader/0.9')
  await expect(panel.locator('[data-discussion-id="dsc_01"]')).toContainText('user_alma')
  // Selection is URL state (?page=&block=): deep-linkable.
  const params = new URL(page.url()).searchParams
  expect(params.get('page')).toBe('1')
  expect(params.get('block')).toBe('2')

  // A block without discussions says so honestly.
  await page.getByTestId('block-item-5').click()
  await expect(panel.getByTestId('discussion-card')).toHaveCount(0)
})

test('blocks without bbox are flagged, never framed', async ({ page }) => {
  await openReader(page)
  // Parse B page 1 block 7 has no bbox (§12.5).
  await page.getByTestId('parse-select').selectOption('rev_parse_b_02')
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })
  await expect(page.getByTestId('block-overlay-7')).toHaveCount(0)
  await page.getByTestId('block-item-7').click()
  // Still selectable from the list; the panel shows the block context.
  await expect(page.getByTestId('block-context')).toContainText('Block without bbox')
})

test('bbox overlay stays glued to the block across zoom and rotation', async ({ page }) => {
  await openReader(page)
  const overlay = page.getByTestId('block-overlay-2')
  const canvas = page.locator('canvas[aria-label="PDF page"]')

  const ratioBefore = await frameRatio(overlay, canvas)
  await page.getByRole('button', { name: 'Zoom in' }).click()
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })
  const ratioAfterZoom = await frameRatio(overlay, canvas)
  expect(Math.abs(ratioAfterZoom - ratioBefore)).toBeLessThan(0.01)

  await page.getByRole('button', { name: 'Rotate 90°' }).click()
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })
  const ratioAfterRotate = await frameRatio(overlay, canvas)
  // Rotation maps normalized (x,y)→(1−y,x): the frame keeps its normalized
  // footprint relative to the (now swapped) canvas extents.
  expect(Math.abs(ratioAfterRotate - ratioBefore)).toBeLessThan(0.01)
})

test('collapsible panes and the discussions page round-trip back to the exact block', async ({ page }) => {
  await openReader(page)
  // Collapse both side panes: the workbench degrades to the PDF column.
  await page.getByRole('button', { name: 'Collapse block pane' }).click()
  await page.getByRole('button', { name: 'Collapse discussion pane' }).click()
  await expect(page.getByTestId('block-list')).toHaveCount(0)
  await expect(page.getByTestId('discussion-panel')).toHaveCount(0)
  await page.getByRole('button', { name: 'Expand block pane' }).click()
  await expect(page.getByTestId('block-list')).toBeVisible()

  // Issue-style list: all six mock discussions, then filtered.
  await page.getByRole('link', { name: 'All discussions' }).click()
  await expect(page.getByTestId('discussions-list')).toBeVisible()
  await expect(page.getByTestId('discussion-row')).toHaveCount(6)

  await page.getByTestId('filter-status').selectOption('confirmed')
  await expect(page.getByTestId('discussion-row')).toHaveCount(1)
  await expect(page.getByTestId('discussion-row')).toHaveAttribute('data-discussion-id', 'dsc_01')

  // Lean resolves to public+lean (§12.1): every mock discussion survives.
  await page.getByTestId('filter-status').selectOption('')
  await page.getByTestId('filter-scope').selectOption('lean')
  await expect(page.getByTestId('discussion-row')).toHaveCount(6)

  // Deep link back into the reader: exact revision+page+block.
  await page.getByTestId('filter-scope').selectOption('')
  await page
    .locator('[data-discussion-id="dsc_04"]')
    .getByTestId('anchor-link')
    .click()
  const params = new URL(page.url()).searchParams
  expect(params.get('rev')).toBe('rev_parse_a_01')
  expect(params.get('page')).toBe('2')
  expect(params.get('block')).toBe('2')
  await expect(page.locator('canvas[data-rendered="2"]')).toBeAttached({ timeout: 15_000 })
  const panel = page.getByTestId('discussion-panel')
  await expect(panel.locator('[data-discussion-id="dsc_04"] [data-status="retracted"]')).toBeVisible()
})

test('expanded discussion thread shows replies with model declarations (zh)', async ({ page }) => {
  await page.goto(`/zh/papers/${READER_PAPER_ID}/discussions`)
  await expect(page.getByTestId('discussions-list')).toBeVisible()
  await page.getByTestId('discussion-row').first().getByTestId('discussion-row-toggle').click()
  const detail = page.getByTestId('discussion-detail')
  await expect(detail).toBeVisible()
  await expect(detail.getByTestId('reply-item')).toHaveCount(2)
  await expect(detail).toContainText('已确认')
})

test('write flow: create discussion, reply, change status with reason, see history', async ({ page }) => {
  await openReader(page)
  await page.getByTestId('block-overlay-2').click()

  // Create a new discussion on this exact anchor (default actor user_alma).
  await page.getByTestId('composer-toggle').click()
  await page.getByTestId('composer-body').fill('New verification note from the browser test.')
  await page.getByTestId('composer-submit').click()
  const newCard = page.getByTestId('discussion-card').filter({ hasText: 'New verification note from the browser test.' })
  await expect(newCard).toBeVisible()
  await expect(newCard.locator('[data-status="pending"]')).toBeVisible()

  // Expand dsc_01 (user_alma is its root author → status control shows).
  const thread = page.locator('[data-discussion-id="dsc_01"]')
  await thread.getByTestId('discussion-toggle').click()
  await expect(thread.getByTestId('reply-item')).toHaveCount(2)

  // Reply with evidence.
  await thread.getByTestId('reply-body').fill('Browser test reply with evidence.')
  await thread.getByTestId('reply-submit').click()
  await expect(thread.getByTestId('reply-item')).toHaveCount(3)

  // Status change requires a reason (apply stays disabled until filled);
  // the badge flips after applying.
  await thread.getByTestId('status-select').selectOption('retracted')
  await expect(thread.getByTestId('status-apply')).toBeDisabled()
  await thread.getByTestId('status-reason').fill('retracting after re-verification')
  await thread.getByTestId('status-apply').click()
  await expect(thread.getByTestId('status-badge')).toHaveAttribute('data-status', 'retracted')

  // History keeps the transition and the reason.
  await thread.getByTestId('history-toggle').click()
  await expect(
    thread.getByTestId('history-event').filter({ hasText: 'retracting after re-verification' }),
  ).toBeVisible()
})

test('papers without fixture data get an honest unavailable state', async ({ page }) => {
  // Any registry paper without reader fixture data (mock mode) must say so
  // instead of faking a reader.
  await page.goto('/en/papers/markdown-paper-0001')
  await expect(page.getByTestId('reader-unavailable')).toBeVisible()
  await page.getByRole('link', { name: 'Open the fixture paper' }).click()
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })
})
