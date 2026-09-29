import { beforeAll, describe, expect, it } from 'vitest'
import {
  BLOCKS_LOCAL,
  BLOCKS_REMOTE,
  DISCUSSIONS,
  mockClient,
  REVISION_LOCAL,
  REVISION_REMOTE,
} from '@/mocks/reader-mock-api'
import { MOCK_PAPER_ID } from '@/lib/reader-api'

// Contract tests for the mock reader API. The mock MUST obey the same
// semantics as the planned live backend (§12.2 + §12.5 golden anchors):
// these tests double as the executable statement of those rules for Q5.
// Block data is REAL: arXiv 1605.01488 parsed by mineru 4.0.9 (pr_local,
// current) and the remote engine 3.4.4 (pr_remote) — see
// tests/fixtures/realpaper/ and scripts/gen-realpaper-mocks.mjs.
const client = mockClient()

async function expectApiError(promise: Promise<unknown>, status: number) {
  await expect(promise).rejects.toMatchObject({ status })
}

beforeAll(async () => {
  // jsdom fetch can read the public fixture path via the test server only;
  // vitest serves nothing, so stub the single static fetch the mock makes.
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    expect(String(input)).toBe('/fixtures/realpaper/1605.01488.pdf')
    return new Response(new Uint8Array([0x25, 0x50, 0x44, 0x46, 0x2d]), { status: 200 })
  }) as typeof fetch
})

describe('mock reader: sources and parses', () => {
  it('lists the single real arXiv v2 source (one PDF, two parses)', async () => {
    const { sources } = await client.listSources(MOCK_PAPER_ID)
    expect(sources.map((s) => s.source_id)).toEqual(['src_arxiv_1605_01488_v2'])
    expect(sources[0].sha256).toBe('d41ff6f92ffb6e610435cef1d7cdb88c80aa796cccd982faf5c1b31c88977eaf')
    expect(sources[0].size_bytes).toBe(763_119)
    expect(sources[0].is_current).toBe(true)
  })

  it('lists pr_local (current) and pr_remote parse revisions', async () => {
    const { parses } = await client.listParses(MOCK_PAPER_ID)
    expect(parses.map((p) => p.revision_id)).toEqual([REVISION_LOCAL, REVISION_REMOTE])
    expect(parses.filter((p) => p.is_current)).toHaveLength(1)
    expect(parses.every((p) => p.schema === 'docvortex.middle' && p.schema_version === '2.0')).toBe(true)
    // artifact hashes are the sha256 of the committed raw middle.json files.
    expect(parses[0].artifact_sha256).toMatch(/^[0-9a-f]{64}$/)
    expect(parses[1].artifact_sha256).toMatch(/^[0-9a-f]{64}$/)
    expect(parses[0].artifact_sha256).not.toBe(parses[1].artifact_sha256)
  })

  it('404s for any paper outside the fixture (never fake coverage)', async () => {
    await expectApiError(client.listSources('qa_0otherpaper000000000000000'), 404)
    await expectApiError(client.listParses('qa_0otherpaper000000000000000'), 404)
  })
})

describe('mock reader: block listing and the golden anchors (§12.5)', () => {
  it('serves all 15 blocks of pr_local page 1 in one page of results', async () => {
    const page = await client.listBlocks(MOCK_PAPER_ID, REVISION_LOCAL, 0)
    expect(page.blocks.map((b) => b.index)).toEqual([1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15])
    expect(page.next_cursor).toBeNull()
    // Real order: block 1 is the rotated arXiv stamp, block 2 the title.
    expect(page.blocks[0].type).toBe('aside_text')
    expect(page.blocks[1].type).toBe('doc_title')
    expect(page.blocks[1].content).toBe('Fully dynamic data structure for LCE queries in compressed space')
  })

  it('paginates real pages that exceed the per-page budget (page 16: 21 blocks)', async () => {
    const first = await client.listBlocks(MOCK_PAPER_ID, REVISION_LOCAL, 15)
    expect(first.blocks).toHaveLength(20)
    expect(first.next_cursor).toBe('20')
    const second = await client.listBlocks(MOCK_PAPER_ID, REVISION_LOCAL, 15, '20')
    expect(second.blocks).toHaveLength(1)
    expect(second.next_cursor).toBeNull()
  })

  it('blocks 0 and 99 do NOT exist on page 1: 404, never nearest-block fallback', async () => {
    await expectApiError(client.readBlock(MOCK_PAPER_ID, REVISION_LOCAL, 0, 0), 404)
    await expectApiError(client.readBlock(MOCK_PAPER_ID, REVISION_LOCAL, 0, 99), 404)
    await expectApiError(client.readBlock(MOCK_PAPER_ID, REVISION_REMOTE, 99, 1), 404)
  })

  it('serves the combined block read of the anchored Uniq(P) equation', async () => {
    const reading = await client.readBlock(MOCK_PAPER_ID, REVISION_LOCAL, 4, 6)
    expect(reading.anchor).toEqual({ page_idx: 4, block_index: 6 })
    expect(reading.content.type).toBe('equation')
    expect(reading.content.content).toContain('U n i q (P)')
    expect(reading.source.parse_revision).toBe(REVISION_LOCAL)
    expect(reading.source.pdf_sha256).toBe('d41ff6f92ffb6e610435cef1d7cdb88c80aa796cccd982faf5c1b31c88977eaf')
    // Two INDEPENDENT discussions on the same block (plan §6.1).
    expect(reading.discussions.map((d) => d.discussion_id)).toEqual(['dsc_01', 'dsc_02'])
  })

  it('the two revisions disagree on page-1 block order (same paper, two parses)', async () => {
    // mineru 4.0.9 puts the arXiv stamp first; engine 3.4.4 starts at the title.
    expect(BLOCKS_LOCAL.find((b) => b.page_idx === 0 && b.index === 1)?.type).toBe('aside_text')
    expect(BLOCKS_REMOTE.find((b) => b.page_idx === 0 && b.index === 1)?.type).toBe('doc_title')
    expect(BLOCKS_LOCAL.find((b) => b.page_idx === 0 && b.index === 2)?.type).toBe('doc_title')
  })

  it('every real block carries a usable bbox (both revisions)', () => {
    for (const blocks of [BLOCKS_LOCAL, BLOCKS_REMOTE]) {
      expect(blocks).toHaveLength(241)
      for (const block of blocks) {
        expect(block.bbox, `block ${block.page_idx}/${block.index}`).toBeDefined()
        const [x0, y0, x1, y1] = block.bbox!
        expect(x0).toBeGreaterThanOrEqual(0)
        expect(y0).toBeGreaterThanOrEqual(0)
        expect(x1).toBeLessThanOrEqual(1)
        expect(y1).toBeLessThanOrEqual(1)
        expect(x1).toBeGreaterThan(x0)
        expect(y1).toBeGreaterThan(y0)
      }
      expect(blocks.filter((b) => b.type === 'equation')).toHaveLength(7)
    }
  })
})

describe('mock reader: discussions', () => {
  it('scope=lean resolves to public+lean, not lean-only (§12.1)', async () => {
    const lean = await client.listDiscussions(MOCK_PAPER_ID, { scope: 'lean' })
    const scopes = new Set(lean.discussions.map((d) => d.scope))
    expect(scopes).toEqual(new Set(['public', 'lean']))
  })

  it('scope=public returns public only', async () => {
    const pub = await client.listDiscussions(MOCK_PAPER_ID, { scope: 'public' })
    expect(pub.discussions.every((d) => d.scope === 'public')).toBe(true)
  })

  it('status filters distinguish pending/confirmed/retracted and none', async () => {
    const confirmed = await client.listDiscussions(MOCK_PAPER_ID, { status: 'confirmed' })
    expect(confirmed.discussions.map((d) => d.discussion_id)).toEqual(['dsc_01'])
    const none = await client.listDiscussions(MOCK_PAPER_ID, { status: 'none' })
    expect(none.discussions.map((d) => d.discussion_id)).toEqual(['dsc_03'])
    const retracted = await client.listDiscussions(MOCK_PAPER_ID, { status: 'retracted' })
    expect(retracted.discussions.map((d) => d.discussion_id)).toEqual(['dsc_04'])
  })

  it('filters by type, including custom slugs', async () => {
    const custom = await client.listDiscussions(MOCK_PAPER_ID, { type: 'context_note' })
    expect(custom.discussions.map((d) => d.discussion_id)).toEqual(['dsc_05'])
  })

  it('filters by parse revision so pr_local and pr_remote anchors never mix', async () => {
    const onlyRemote = await client.listDiscussions(MOCK_PAPER_ID, { parse_revision: REVISION_REMOTE })
    expect(onlyRemote.discussions.map((d) => d.discussion_id)).toEqual(['dsc_06'])
    // Same VISUAL equation, TWO distinct anchors across parses (§12.5):
    // pr_remote (4,6) renders slightly different LaTeX/bbox than pr_local.
    const localReading = await client.readBlock(MOCK_PAPER_ID, REVISION_LOCAL, 4, 6)
    const remoteReading = await client.readBlock(MOCK_PAPER_ID, REVISION_REMOTE, 4, 6)
    expect(remoteReading.content.type).toBe('equation')
    expect(remoteReading.content.content).toContain('U n i q (P)')
    expect(remoteReading.content.bbox).not.toEqual(localReading.content.bbox)
    expect(localReading.discussions.map((d) => d.discussion_id)).not.toContain('dsc_06')
  })

  it('paginates with an opaque cursor and a terminal null', async () => {
    const first = await client.listDiscussions(MOCK_PAPER_ID, {})
    expect(first.discussions).toHaveLength(DISCUSSIONS.length)
    expect(first.next_cursor).toBeNull()
    const missing = await client.listDiscussions(MOCK_PAPER_ID, { cursor: '9999' })
    expect(missing.discussions).toEqual([])
  })

  it('serves discussion detail with replies and model declarations', async () => {
    const detail = await client.getDiscussion('dsc_01')
    expect(detail.status).toBe('confirmed')
    expect(detail.reply_count).toBe(2)
    expect(detail.replies).toHaveLength(2)
    expect(detail.replies.every((r) => r.discussion_id === 'dsc_01')).toBe(true)
    await expectApiError(client.getDiscussion('dsc_missing'), 404)
  })
})

describe('mock reader: pdf bytes', () => {
  it('fetches the real fixture pdf', async () => {
    const bytes = await client.fetchPdfBytes(MOCK_PAPER_ID, 'src_arxiv_1605_01488_v2', undefined)
    expect(bytes.byteLength).toBeGreaterThan(4)
    await expectApiError(
      client.fetchPdfBytes('qa_0otherpaper000000000000000', 'src_arxiv_1605_01488_v2', undefined),
      404,
    )
  })
})
