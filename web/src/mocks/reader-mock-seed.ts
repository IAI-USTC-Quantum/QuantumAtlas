// Seed dataset for the mock reader (leaf module: types + shared helpers
// only). Kept separate from both the api client and the mutable store so
// they can import it without a runtime import cycle (a cycle here
// previously crashed the page with a TDZ ReferenceError).
import parseA from './fixtures/parse-a.middle.json'
import parseB from './fixtures/parse-b.middle.json'
import sourcesFixture from './fixtures/sources.json'
import type { PaperDetail } from '@/lib/api'
import { MOCK_PAPER_ID, ReaderApiError } from '@/lib/reader-shared'
import type {
  DiscussionSummary,
  PaperSource,
  ParseRevision,
  ReaderBlock,
  ReplyEntry,
} from '@/lib/reader-types'

// JSON modules are typed `any` by vite/client; re-narrow through local
// structural types so the fixture files keep compile-time checking.
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

export const PDF_SHA = sourcesFixtureTyped.pdf_sha256

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

export function requireMockPaper(paperId: string): void {
  if (paperId !== MOCK_PAPER_ID) {
    throw new ReaderApiError(404, `reader mock: paper ${paperId} has no fixture data`)
  }
}

// Synthetic PaperDetail so `npm run dev` can preview the workbench with
// zero backend (the real /api/papers/{id} would 401/404 in mock mode).
export function mockPaperDetail(): PaperDetail {
  return {
    paper_id: MOCK_PAPER_ID,
    status: 'ready',
    title: 'Synthetic Block-Comments Fixture Paper',
    authors: ['Synthetic Author'],
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-23T12:00:00Z',
    assets: [
      {
        asset_id: 1,
        source: 'arxiv',
        arxiv_version: 3,
        pdf_sha256: PDF_SHA,
        pdf_size: 854,
        fetched_at: '2026-09-01T00:00:00Z',
      },
    ],
    acquisition: { state: 'ready', phase: 'done', active: false, events: [], updated_at: '2026-09-01T00:00:00Z' },
  }
}

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

export const REPLY_FEED: Record<string, Omit<ReplyEntry, 'discussion_id'>[]> = {
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
