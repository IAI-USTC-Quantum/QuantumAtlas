// Mock reader dataset (plan §12.1 "原型先行", §12.5 fixtures).
//
// Everything here is synthetic. The parse data mirrors the repo fixtures
// tests/fixtures/blockcomments/{parse-a,parse-b}.middle.json (copied under
// src/mocks/fixtures/); the PDF bytes live at public/fixtures/blockcomments/
// minimal-2page.pdf. Discussions are handwritten to exercise: two
// independent discussions on the same block, all three statuses plus a
// no-status note, public/lean scopes, preset and custom types, model
// declarations, and the golden-anchor rule that parse-A and parse-B blocks
// never merge (§12.5 golden-anchors.json).
import parseA from './fixtures/parse-a.middle.json'
import parseB from './fixtures/parse-b.middle.json'
import sourcesFixture from './fixtures/sources.json'
import {
  MOCK_PAPER_ID,
  MOCK_PDF_PATH,
  ReaderApiError,
  scopeMatches,
  statusMatches,
} from '@/lib/reader-shared'
import type {
  BlockReading,
  BlocksPage,
  DiscussionDetail,
  DiscussionsPage,
  DiscussionSummary,
  PaperParsesResponse,
  PaperSource,
  PaperSourcesResponse,
  ParseRevision,
  ReaderClient,
  ReaderBlock,
} from '@/lib/reader-types'

// JSON modules are typed `any` by vite/client; re-narrow through a local
// structural type so the fixture files keep compile-time checking.
type FixtureBlock = {
  page_idx: number
  index: number
  type: string
  content: string
  bbox?: number[]
}
type FixtureFile = { schema: string; schema_version: string; blocks: FixtureBlock[] }

const parseAFixture = parseA as unknown as FixtureFile
const parseBFixture = parseB as unknown as FixtureFile
const sourcesFixtureTyped = sourcesFixture as unknown as {
  pdf_sha256: string
  sources: { source_id: string; origin: string; sha256: string; size_bytes: number }[]
}

const PDF_SHA = sourcesFixtureTyped.pdf_sha256

export const REVISION_A = 'rev_parse_a_01'
export const REVISION_B = 'rev_parse_b_02'

function asBlocks(file: FixtureFile): ReaderBlock[] {
  return file.blocks.map((block) => ({
    page_idx: block.page_idx,
    index: block.index,
    type: block.type,
    content: block.content,
    bbox: (block.bbox ?? null) as ReaderBlock['bbox'],
  }))
}

export const BLOCKS_A = asBlocks(parseAFixture)
export const BLOCKS_B = asBlocks(parseBFixture)

export const SOURCES: PaperSource[] = sourcesFixtureTyped.sources.map((source, i) => ({
  ...source,
  created_at: '2026-09-01T00:00:00Z',
  is_current: i === sourcesFixtureTyped.sources.length - 1,
}))

export const PARSES: ParseRevision[] = [
  {
    revision_id: REVISION_A,
    source_id: 'src_arxiv_v3',
    schema: parseAFixture.schema,
    schema_version: parseAFixture.schema_version,
    artifact_sha256: 'a'.repeat(64),
    created_at: '2026-09-20T10:00:00Z',
    is_current: true,
  },
  {
    revision_id: REVISION_B,
    source_id: 'src_arxiv_v2',
    schema: parseBFixture.schema,
    schema_version: parseBFixture.schema_version,
    artifact_sha256: 'b'.repeat(64),
    created_at: '2026-09-10T10:00:00Z',
    is_current: false,
  },
]

const userAlma = 'user_alma'
const userBo = 'user_bo'
const userCleo = 'user_cleo'

function discussion(
  id: string,
  partial: Omit<DiscussionSummary, 'paper_id' | 'discussion_id'>,
): DiscussionSummary {
  return { discussion_id: id, paper_id: MOCK_PAPER_ID, ...partial }
}

// Two independent discussions anchor parse-A page 1 block 2 (the E=mc²
// equation): withdrawing one must never affect the other (plan §6.1).
export const DISCUSSIONS: DiscussionSummary[] = [
  discussion('dsc_01', {
    parse_revision: REVISION_A,
    page_idx: 0,
    block_index: 2,
    type: 'transcription_error',
    scope: 'public',
    status: 'confirmed',
    body: 'The transcribed exponent renders as “E = m c^{2}”, but the original scan shows the 2 slightly offset — worth confirming the exponent placement against the source image before quoting this transcription.',
    created_by: userAlma,
    model: 'agent-reader/0.9',
    revision: 3,
    reply_count: 2,
    created_at: '2026-09-21T08:00:00Z',
    updated_at: '2026-09-22T08:30:00Z',
  }),
  discussion('dsc_02', {
    parse_revision: REVISION_A,
    page_idx: 0,
    block_index: 2,
    type: 'typo_in_original',
    scope: 'lean',
    status: 'pending',
    body: '与另一份排版对照后怀疑原 PDF 本身在此处缺少单位说明；先挂起待查证，等拿到 v3 原件再核对。Lean 侧暂勿依赖该公式的单位约定。',
    created_by: userBo,
    model: null,
    revision: 1,
    reply_count: 1,
    created_at: '2026-09-21T09:15:00Z',
    updated_at: '2026-09-21T09:15:00Z',
  }),
  discussion('dsc_03', {
    parse_revision: REVISION_A,
    page_idx: 0,
    block_index: 1,
    type: 'normal',
    scope: 'public',
    status: null,
    body: '阅读笔记：PAGE ONE TEST 是合成夹具的标记行，不承载语义。记录在此避免后续读者误当结论引用。',
    created_by: userCleo,
    model: null,
    revision: 1,
    reply_count: 0,
    created_at: '2026-09-21T10:00:00Z',
    updated_at: '2026-09-21T10:00:00Z',
  }),
  discussion('dsc_04', {
    parse_revision: REVISION_A,
    page_idx: 1,
    block_index: 2,
    type: 'transcription_error',
    scope: 'public',
    status: 'retracted',
    body: '最初怀疑求和公式的分数线转录有误；重新对照原图后确认转录忠实，撤回该质疑。保留原文与回复作为查证记录。',
    created_by: userAlma,
    model: null,
    revision: 2,
    reply_count: 1,
    created_at: '2026-09-21T11:00:00Z',
    updated_at: '2026-09-23T12:00:00Z',
  }),
  discussion('dsc_05', {
    parse_revision: REVISION_A,
    page_idx: 1,
    block_index: 1,
    type: 'context_note',
    scope: 'lean',
    status: 'pending',
    body: 'Lean formalization note: the synthetic page-two heading has no mathematical content; a finite-dimensional scaffold should skip it entirely.',
    created_by: userBo,
    model: 'lean-agent/1.2',
    revision: 1,
    reply_count: 0,
    created_at: '2026-09-22T14:00:00Z',
    updated_at: '2026-09-22T14:00:00Z',
  }),
  // Anchored to parse-B page 1 block 1: same visual equation as parse-A's
  // block 2, but a DISTINCT anchor — never merged or auto-migrated (§12.5).
  discussion('dsc_06', {
    parse_revision: REVISION_B,
    page_idx: 0,
    block_index: 1,
    type: 'transcription_error',
    scope: 'public',
    status: 'pending',
    body: 'Parse B renders this equation block with a slightly different bbox than parse A. Both stay readable; flagging so nobody assumes the two anchors share comments.',
    created_by: userCleo,
    model: 'agent-reader/0.9',
    revision: 1,
    reply_count: 0,
    created_at: '2026-09-23T09:00:00Z',
    updated_at: '2026-09-23T09:00:00Z',
  }),
]

export const REPLY_FEED: Record<string, Omit<DiscussionDetail['replies'][number], 'discussion_id'>[]> = {
  dsc_01: [
    {
      reply_id: 'rpl_0101',
      body: 'Checked the cropped original image at this anchor: the exponent is a superscript 2 with a small horizontal offset — transcription is faithful, the offset is a scan artifact.',
      created_by: userBo,
      model: null,
      revision: 1,
      created_at: '2026-09-21T08:30:00Z',
      updated_at: '2026-09-21T08:30:00Z',
    },
    {
      reply_id: 'rpl_0102',
      body: '标记为已确认：具体问题是扫描伪影而非转录错误，引用时请直接对照原图裁片。',
      created_by: userAlma,
      model: null,
      revision: 1,
      created_at: '2026-09-22T08:30:00Z',
      updated_at: '2026-09-22T08:30:00Z',
    },
  ],
  dsc_02: [
    {
      reply_id: 'rpl_0201',
      body: 'v3 source is the current pointer; re-check once the v2 bytes are re-fetched for comparison.',
      created_by: userCleo,
      model: 'agent-reader/0.9',
      revision: 1,
      created_at: '2026-09-21T09:30:00Z',
      updated_at: '2026-09-21T09:30:00Z',
    },
  ],
  dsc_04: [
    {
      reply_id: 'rpl_0401',
      body: '复核无误：分数线位置与原图一致。同意撤回，理由已记录在状态历史中。',
      created_by: userBo,
      model: null,
      revision: 1,
      created_at: '2026-09-23T12:00:00Z',
      updated_at: '2026-09-23T12:00:00Z',
    },
  ],
}

function requireMockPaper(paperId: string): void {
  if (paperId !== MOCK_PAPER_ID) {
    throw new ReaderApiError(404, `reader mock: paper ${paperId} has no fixture data`)
  }
}

function blocksFor(revision: string): ReaderBlock[] {
  if (revision === REVISION_A) return BLOCKS_A
  if (revision === REVISION_B) return BLOCKS_B
  throw new ReaderApiError(404, `reader mock: unknown parse revision ${revision}`)
}

export function mockClient(): ReaderClient {
  return {
    async listSources(paperId) {
      requireMockPaper(paperId)
      return { sources: SOURCES } satisfies PaperSourcesResponse
    },
    async listParses(paperId) {
      requireMockPaper(paperId)
      return { parses: PARSES } satisfies PaperParsesResponse
    },
    async listBlocks(_paperId, revision, pageIdx, cursor) {
      const all = blocksFor(revision)
      const onPage = all.filter((b) => b.page_idx === pageIdx)
      const start = cursor ? Number(cursor) : 0
      if (!Number.isInteger(start) || start < 0) {
        throw new ReaderApiError(400, 'reader mock: bad cursor')
      }
      const perPage = 20
      const slice = onPage.slice(start, start + perPage)
      const next = start + perPage < onPage.length ? String(start + perPage) : null
      return { blocks: slice, next_cursor: next } satisfies BlocksPage
    },
    async readBlock(paperId, revision, pageIdx, blockIndex) {
      requireMockPaper(paperId)
      const block = blocksFor(revision).find(
        (b) => b.page_idx === pageIdx && b.index === blockIndex,
      )
      // Golden-anchor rule: a missing block 404s — never fall back to the
      // nearest block (§12.5 parse-A page 1 blocks 3/4, parse-B page 2).
      if (!block) {
        throw new ReaderApiError(
          404,
          `block page_idx=${pageIdx} block_index=${blockIndex} not found in ${revision}`,
        )
      }
      const parse = PARSES.find((p) => p.revision_id === revision)
      if (!parse) throw new ReaderApiError(404, `unknown parse revision ${revision}`)
      const discussions = DISCUSSIONS.filter(
        (d) =>
          d.parse_revision === revision &&
          d.page_idx === pageIdx &&
          d.block_index === blockIndex,
      )
      return {
        source: {
          paper_id: paperId,
          source_id: parse.source_id,
          pdf_sha256: PDF_SHA,
          parse_revision: revision,
          artifact_sha256: parse.artifact_sha256,
          schema: parse.schema,
          schema_version: parse.schema_version,
        },
        anchor: { page_idx: pageIdx, block_index: blockIndex },
        content: { type: block.type, content: block.content, bbox: block.bbox ?? null },
        discussions,
        next_cursor: null,
      } satisfies BlockReading
    },
    async listDiscussions(paperId, filters) {
      requireMockPaper(paperId)
      const filtered = DISCUSSIONS.filter(
        (d) =>
          scopeMatches(filters.scope, d.scope) &&
          statusMatches(filters.status, d.status) &&
          (!filters.type || d.type === filters.type) &&
          (filters.parse_revision === undefined || d.parse_revision === filters.parse_revision) &&
          (filters.page_idx === undefined || d.page_idx === filters.page_idx) &&
          (filters.block_index === undefined || d.block_index === filters.block_index),
      )
      const perPage = 20
      const start = filters.cursor ? Number(filters.cursor) : 0
      const slice = filtered.slice(start, start + perPage)
      const next = start + perPage < filtered.length ? String(start + perPage) : null
      return { discussions: slice, next_cursor: next } satisfies DiscussionsPage
    },
    async getDiscussion(discussionId) {
      const found = DISCUSSIONS.find((d) => d.discussion_id === discussionId)
      if (!found) throw new ReaderApiError(404, `discussion ${discussionId} not found`)
      const replies = (REPLY_FEED[discussionId] ?? []).map((reply) => ({
        ...reply,
        discussion_id: discussionId,
      }))
      return { ...found, replies, replies_next_cursor: null } satisfies DiscussionDetail
    },
    async fetchPdfBytes(paperId) {
      requireMockPaper(paperId)
      const response = await fetch(MOCK_PDF_PATH)
      if (!response.ok) {
        throw new ReaderApiError(response.status, `mock pdf fetch failed: ${response.status}`)
      }
      return response.arrayBuffer()
    },
  }
}
