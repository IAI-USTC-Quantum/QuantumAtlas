import { createHash } from 'node:crypto'
import { readFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'
import { transformMiddle } from '../scripts/gen-realpaper-mocks.mjs'
import { normalizedBBoxToRect } from '@/lib/reader-geometry'
import { BLOCKS_LOCAL, BLOCKS_REMOTE } from '@/mocks/reader-mock-api'

// Real-data properties for the reader mock dataset (arXiv 1605.01488v2,
// "Fully dynamic data structure for LCE queries in compressed space").
// The raw parse artifacts live in tests/fixtures/realpaper/ (committed,
// sha-pinned); the app fixtures are generated from them. These tests pin:
//   1. no drift — regenerating from the committed raw reproduces the
//      committed slim fixtures byte-for-byte,
//   2. the real document shape (17 pages, 241 blocks, type mix),
//   3. the bbox coordinate system against independent ground truth
//      (pdftotext word extents, tests/fixtures/realpaper/local/meta.txt),
//   4. overlay geometry under rotation using the SAME real bboxes.

// NOTE: build paths by string-slicing import.meta.url, NOT `new URL(rel, import.meta.url)`:
// in vitest's jsdom environment the global URL constructor is jsdom's, whose
// file://-base resolution yields http://localhost URLs (the scheme error this
// file originally crashed with). node:url's fileURLToPath parses the plain
// file:// STRING with Node's own parser, which stays correct.
const here = fileURLToPath(`${import.meta.url.replace(/[^/]*$/, '')}`)
const RAW_LOCAL = join(here, 'fixtures/realpaper/local/middle.json')
const RAW_REMOTE = join(here, 'fixtures/realpaper/remote/middle.json')
const SLIM_LOCAL = join(here, '../src/mocks/fixtures/realpaper-local.middle.json')
const SLIM_REMOTE = join(here, '../src/mocks/fixtures/realpaper-remote.middle.json')

const PDF_SHA = 'd41ff6f92ffb6e610435cef1d7cdb88c80aa796cccd982faf5c1b31c88977eaf'

function sha256(path: string): string {
  return createHash('sha256').update(readFileSync(path)).digest('hex')
}

describe('realpaper fixtures: generation stays reproducible', () => {
  it('regenerating from the committed raw artifacts reproduces the committed slim fixtures', () => {
    for (const [rawUrl, slimUrl, variant] of [
      [RAW_LOCAL, SLIM_LOCAL, 'local'],
      [RAW_REMOTE, SLIM_REMOTE, 'remote'],
    ] as const) {
      const raw = JSON.parse(readFileSync(rawUrl, 'utf8'))
      const expected = transformMiddle(raw, {
        variant,
        raw_middle_json_sha256: sha256(rawUrl),
        source_pdf_sha256: PDF_SHA,
      })
      const committed = JSON.parse(readFileSync(slimUrl, 'utf8'))
      expect(committed).toEqual(expected)
    }
  })

  it('the source pdf pinned in provenance is the committed one', async () => {
    const pdf = join(here, '../public/fixtures/realpaper/1605.01488.pdf')
    expect(sha256(pdf)).toBe(PDF_SHA)
  })
})

describe('realpaper fixtures: real document shape', () => {
  it('is the 17-page / 241-block document with the expected type mix', () => {
    for (const blocks of [BLOCKS_LOCAL, BLOCKS_REMOTE]) {
      const pages = new Set(blocks.map((b) => b.page_idx))
      expect(pages.size).toBe(17)
      expect(blocks).toHaveLength(241)
      const byType = new Map<string, number>()
      for (const block of blocks) byType.set(block.type, (byType.get(block.type) ?? 0) + 1)
      expect(Object.fromEntries(byType)).toEqual({
        text: 149,
        paragraph_title: 32,
        ref_text: 25,
        page_number: 17,
        equation: 7,
        image: 7,
        page_footnote: 2,
        doc_title: 1,
        aside_text: 1,
      })
    }
  })

  it('every page 1..17 has at least one block in both revisions', () => {
    for (const blocks of [BLOCKS_LOCAL, BLOCKS_REMOTE]) {
      for (let page = 0; page < 17; page += 1) {
        expect(blocks.some((b) => b.page_idx === page)).toBe(true)
      }
    }
  })

  it('public block numbers are 1-based and per-page unique (raw index+1)', () => {
    for (const blocks of [BLOCKS_LOCAL, BLOCKS_REMOTE]) {
      const seen = new Set<string>()
      for (const block of blocks) {
        const key = `${block.page_idx}/${block.index}`
        expect(seen.has(key)).toBe(false)
        seen.add(key)
        expect(block.index).toBeGreaterThanOrEqual(1)
      }
    }
  })
})

describe('realpaper fixtures: bbox ground truth (pdftotext cross-check)', () => {
  // Ground truth from tests/fixtures/realpaper/local/meta.txt: the doc_title
  // bbox converts to (88, 107, 507, 150) pt on the A4 595×842 pt page, and
  // pdftotext -bbox independently measured the title words at
  // (88.8, 111.9, 506.4, 149.3) pt — same top-based origin. This pins the
  // y-axis direction reader-geometry.ts assumes: if anyone flips the axis,
  // this fails with a ~2×(1−y) mirrored position.
  const PAGE_W_PT = 595
  const PAGE_H_PT = 842

  const docTitle = BLOCKS_LOCAL.find((b) => b.page_idx === 0 && b.type === 'doc_title')
  // Capture into consts so the null-guard narrows INSIDE the it() closures.
  const docTitleBbox = docTitle?.bbox
  if (!docTitleBbox) throw new Error('doc_title block missing or without bbox')

  it('doc_title bbox × page size lands on the pdftotext-verified word extent', () => {
    const [x0, y0, x1, y1] = docTitleBbox
    expect(x0 * PAGE_W_PT).toBeCloseTo(88, 0)
    expect(y0 * PAGE_H_PT).toBeCloseTo(107, 0)
    expect(x1 * PAGE_W_PT).toBeCloseTo(507, 0)
    expect(y1 * PAGE_H_PT).toBeCloseTo(150, 0)
  })

  it('the rotated aside_text stamp maps onto the left margin, not mirrored', () => {
    // The vertical arXiv stamp hugs the left edge (normalized x ≈ 0.024..0.06).
    // Under a 90° clockwise rotation its normalized footprint must stay the
    // SAME shape on the swapped canvas — the top-based mapping in
    // reader-geometry.ts, exercised with real coordinates.
    const stamp = BLOCKS_LOCAL.find((b) => b.page_idx === 0 && b.type === 'aside_text')
    const stampBbox = stamp?.bbox
    if (!stampBbox) throw new Error('aside_text block missing or without bbox')
    const rect = normalizedBBoxToRect(stampBbox, 90, PAGE_H_PT, PAGE_W_PT)
    // x-extent [0.024, 0.06] becomes the v-extent (top band of the rotated
    // canvas); y-extent [0.288, 0.7] becomes the u-extent.
    expect(rect.top).toBeCloseTo(0.024 * PAGE_W_PT, 1)
    expect(rect.height).toBeCloseTo((0.06 - 0.024) * PAGE_W_PT, 1)
    expect(rect.left).toBeCloseTo((1 - 0.7) * PAGE_H_PT, 1)
    expect(rect.width).toBeCloseTo((0.7 - 0.288) * PAGE_H_PT, 1)
    // And back at rotation 0 the frame is the plain page-space box.
    const upright = normalizedBBoxToRect(stampBbox, 0, PAGE_W_PT, PAGE_H_PT)
    expect(upright.left).toBeCloseTo(0.024 * PAGE_W_PT, 1)
    expect(upright.top).toBeCloseTo(0.288 * PAGE_H_PT, 1)
  })
})
