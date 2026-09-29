import { beforeAll, describe, expect, it } from 'vitest'
import {
  BLOCKS_A,
  BLOCKS_B,
  DISCUSSIONS,
  mockClient,
  REVISION_A,
  REVISION_B,
} from '@/mocks/reader-mock-api'
import { MOCK_PAPER_ID } from '@/lib/reader-api'

// Contract tests for the mock reader API. The mock MUST obey the same
// semantics as the planned live backend (§12.2 + §12.5 golden anchors):
// these tests double as the executable statement of those rules for Q5.
const client = mockClient()

async function expectApiError(promise: Promise<unknown>, status: number) {
  await expect(promise).rejects.toMatchObject({ status })
}

beforeAll(async () => {
  // jsdom fetch can read the public fixture path via the test server only;
  // vitest serves nothing, so stub the single static fetch the mock makes.
  globalThis.fetch = (async (input: RequestInfo | URL) => {
    expect(String(input)).toBe('/fixtures/blockcomments/minimal-2page.pdf')
    return new Response(new Uint8Array([0x25, 0x50, 0x44, 0x46, 0x2d]), { status: 200 })
  }) as typeof fetch
})

describe('mock reader: sources and parses', () => {
  it('lists the two arXiv v2/v3 sources as distinct source_ids', async () => {
    const { sources } = await client.listSources(MOCK_PAPER_ID)
    expect(sources.map((s) => s.source_id).sort()).toEqual(['src_arxiv_v2', 'src_arxiv_v3'])
    expect(sources.filter((s) => s.is_current)).toHaveLength(1)
  })

  it('lists parse revisions with exactly one current pointer', async () => {
    const { parses } = await client.listParses(MOCK_PAPER_ID)
    expect(parses.map((p) => p.revision_id)).toEqual([REVISION_A, REVISION_B])
    expect(parses.filter((p) => p.is_current)).toHaveLength(1)
    expect(parses.every((p) => p.schema === 'docvortex.middle' && p.schema_version === '2.0')).toBe(true)
  })

  it('404s for any paper outside the fixture (never fake coverage)', async () => {
    await expectApiError(client.listSources('qa_0otherpaper000000000000000'), 404)
    await expectApiError(client.listParses('qa_0otherpaper000000000000000'), 404)
  })
})

describe('mock reader: block listing and the golden anchors (§12.5)', () => {
  it('preserves non-contiguous block indexes 1,2,5 on parse-A page 1', async () => {
    const page = await client.listBlocks(MOCK_PAPER_ID, REVISION_A, 0)
    expect(page.blocks.map((b) => b.index)).toEqual([1, 2, 5])
    expect(page.next_cursor).toBeNull()
  })

  it('parse-A page 2 has blocks 1,2', async () => {
    const page = await client.listBlocks(MOCK_PAPER_ID, REVISION_A, 1)
    expect(page.blocks.map((b) => b.index)).toEqual([1, 2])
  })

  it('parse-B lacks page 2 entirely: readBlock must 404, listBlocks is empty', async () => {
    await expectApiError(client.readBlock(MOCK_PAPER_ID, REVISION_B, 1, 1), 404)
    const page = await client.listBlocks(MOCK_PAPER_ID, REVISION_B, 1)
    expect(page.blocks).toEqual([])
  })

  it('parse-A page 1 blocks 3/4 do NOT exist: 404, never nearest-block fallback', async () => {
    await expectApiError(client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 3), 404)
    await expectApiError(client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 4), 404)
  })

  it('serves the combined block read: source+anchor+content+discussions', async () => {
    const reading = await client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 2)
    expect(reading.anchor).toEqual({ page_idx: 0, block_index: 2 })
    expect(reading.content.type).toBe('equation')
    expect(reading.content.content).toBe('E = m c^{2}')
    expect(reading.source.parse_revision).toBe(REVISION_A)
    expect(reading.source.pdf_sha256).toBe('5eb6980e0eb27b236cdefd9ecbd557b003a8ab9eb23e5c247e04c0f2a3caedb2')
    // Two INDEPENDENT discussions on the same block (plan §6.1).
    expect(reading.discussions.map((d) => d.discussion_id)).toEqual(['dsc_01', 'dsc_02'])
  })

  it('parse-B block 7 carries no bbox: reported as null, not fabricated', async () => {
    expect(BLOCKS_B.find((b) => b.index === 7)?.bbox).toBeNull()
    const reading = await client.readBlock(MOCK_PAPER_ID, REVISION_B, 0, 7)
    expect(reading.content.bbox).toBeNull()
  })

  it('fixture blocks mirror parse-a.middle.json shapes', () => {
    expect(BLOCKS_A).toHaveLength(5)
    expect(BLOCKS_A.filter((b) => b.bbox)).toHaveLength(5)
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

  it('filters by parse revision so parse-A and parse-B anchors never mix', async () => {
    const onlyB = await client.listDiscussions(MOCK_PAPER_ID, { parse_revision: REVISION_B })
    expect(onlyB.discussions.map((d) => d.discussion_id)).toEqual(['dsc_06'])
    // Same-looking equation, TWO distinct anchors across parses (§12.5).
    const aBlock2 = await client.readBlock(MOCK_PAPER_ID, REVISION_A, 0, 2)
    const bBlock1 = await client.readBlock(MOCK_PAPER_ID, REVISION_B, 0, 1)
    expect(aBlock2.content.content).toBe(bBlock1.content.content)
    expect(aBlock2.discussions.map((d) => d.discussion_id)).not.toContain('dsc_06')
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
  it('fetches the fixture pdf', async () => {
    const bytes = await client.fetchPdfBytes(MOCK_PAPER_ID, 'src_arxiv_v3', undefined)
    expect(bytes.byteLength).toBeGreaterThan(4)
    await expectApiError(
      client.fetchPdfBytes('qa_0otherpaper000000000000000', 'src_arxiv_v3', undefined),
      404,
    )
  })
})
