import type { Locator, Page } from '@playwright/test'
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
