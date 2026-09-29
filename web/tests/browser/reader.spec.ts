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

// Real anchors on pr_local (1-based URL numbering): page 5 block 6 is the
// Uniq(P) equation carrying the two-independent-discussions demo; page 1
// has 15 blocks (aside stamp, doc_title, authors, abstract …).
const READER_URL = `/papers/${READER_PAPER_ID}`

async function openReader(
  page: Page,
  language: 'en' | 'zh' = 'en',
  target: { page?: number; block?: number } = {},
) {
  const search = new URLSearchParams({ rev: 'pr_local' })
  if (target.page) search.set('page', String(target.page))
  if (target.block) search.set('block', String(target.block))
  await page.goto(`/${language}${READER_URL}?${search}`)
  await expect(page.locator(WORKBENCH)).toBeVisible()
  await expect(page.getByTestId('reader-mock-badge')).toBeVisible()
  // The canvas reports the rendered page number through data-rendered.
  await expect(page.locator(`canvas[data-rendered="${target.page ?? 1}"]`)).toBeAttached({ timeout: 15_000 })
}

async function frameRatio(overlay: Locator, canvas: Locator): Promise<number> {
  const frame = (await overlay.boundingBox())!
  const surface = (await canvas.boundingBox())!
  expect(frame.width).toBeGreaterThan(4)
  expect(frame.height).toBeGreaterThan(4)
  return (frame.width * frame.height) / (surface.width * surface.height)
}

test('renders the 17-page pdf with a full overlay on page 1 (15 real blocks)', async ({ page }) => {
  await openReader(page)
  // Page 1 of pr_local: every one of the 15 real top-level blocks gets a
  // frame — the cursor-following block query must not truncate any.
  for (let index = 1; index <= 15; index += 1) {
    await expect(page.getByTestId(`block-overlay-${index}`)).toBeVisible()
  }
  await expect(page.getByTestId('block-overlay-16')).toHaveCount(0)
  // Page 1: doc_title (block 2) carries the reading-note discussion.
  await expect(page.getByTestId('block-item-2')).toContainText('1 discussions')

  // Page navigation across the real 17-page document.
  await page.getByTestId('pdf-page-input').fill('5')
  await expect(page.locator('canvas[data-rendered="5"]')).toBeAttached({ timeout: 15_000 })
  // Page 5 carries 17 blocks, all framed (per-page budget is 20 in the mock
  // client; page 16's 21 blocks exercise the cursor path in the unit suite).
  for (let index = 1; index <= 17; index += 1) {
    await expect(page.getByTestId(`block-overlay-${index}`)).toBeVisible()
  }

  // Block 6 on page 5 is the anchored equation and carries 2 discussions.
  await expect(page.getByTestId('block-item-6')).toContainText('equation')
  await expect(page.getByTestId('block-item-6')).toContainText('2 discussions')
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
  // real-paper list instead — and the entry must click through into a
  // working reader. (The fixture boundary also fails the test if any
  // unmocked /api request is made from this page.)
  await page.goto('/zh/papers')
  await expect(page.getByTestId('papers-mock-badge')).toBeVisible()

  const fixtureRow = page.getByRole('link', { name: 'Fully dynamic data structure for LCE queries in compressed space' })
  await expect(fixtureRow).toBeVisible()
  await fixtureRow.click()

  await expect(page.locator(WORKBENCH)).toBeVisible()
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })
})

test('selecting a block shows its two independent discussions', async ({ page }) => {
  await openReader(page, 'en', { page: 5 })
  await page.getByTestId('block-overlay-6').click()

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
  expect(params.get('page')).toBe('5')
  expect(params.get('block')).toBe('6')

  // A block without discussions says so honestly.
  await page.getByTestId('block-item-7').click()
  await expect(panel.getByTestId('discussion-card')).toHaveCount(0)
})

test('switching to pr_remote re-anchors: same paper, different block order', async ({ page }) => {
  await openReader(page)
  // mineru 4.0.9 (pr_local) starts page 1 with the rotated arXiv stamp;
  // engine 3.4.4 (pr_remote) starts at the title. Both revisions render
  // every block — and the remote-only discussion lives at its own anchor.
  await page.getByTestId('parse-select').selectOption('pr_remote')
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })
  await expect(page.getByTestId('block-item-1')).toContainText('doc_title')
  await expect(page.getByTestId('block-item-1')).not.toContainText('aside_text')
  await expect(page.getByTestId('block-item-14')).toContainText('aside_text')

  // The remote equation anchor (dsc_06, same visual equation as pr_local's
  // page 5 block 6) is a DISTINCT anchor on page 5 block 6 of pr_remote.
  await page.getByTestId('pdf-page-input').fill('5')
  await expect(page.locator('canvas[data-rendered="5"]')).toBeAttached({ timeout: 15_000 })
  await page.getByTestId('block-item-6').click()
  const panel = page.getByTestId('discussion-panel')
  await expect(panel.getByTestId('discussion-card')).toHaveCount(1)
  await expect(panel.locator('[data-discussion-id="dsc_06"]')).toBeVisible()
})

test('bbox overlay stays glued to the block across zoom and rotation', async ({ page }) => {
  await openReader(page, 'en', { page: 5 })
  const overlay = page.getByTestId('block-overlay-6')
  const canvas = page.locator('canvas[aria-label="PDF page"]')

  const ratioBefore = await frameRatio(overlay, canvas)
  await page.getByRole('button', { name: 'Zoom in' }).click()
  await expect(page.locator('canvas[data-rendered="5"]')).toBeAttached({ timeout: 15_000 })
  const ratioAfterZoom = await frameRatio(overlay, canvas)
  expect(Math.abs(ratioAfterZoom - ratioBefore)).toBeLessThan(0.01)

  await page.getByRole('button', { name: 'Rotate 90°' }).click()
  await expect(page.locator('canvas[data-rendered="5"]')).toBeAttached({ timeout: 15_000 })
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
  expect(params.get('rev')).toBe('pr_local')
  expect(params.get('page')).toBe('7')
  expect(params.get('block')).toBe('14')
  await expect(page.locator('canvas[data-rendered="7"]')).toBeAttached({ timeout: 15_000 })
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
  await openReader(page, 'en', { page: 5 })
  await page.getByTestId('block-overlay-6').click()

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
