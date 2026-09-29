// Seed dataset for the mock reader (leaf module: types + shared helpers
// only). Kept separate from both the api client and the mutable store so
// they can import it without a runtime import cycle (a cycle here
// previously crashed the page with a TDZ ReferenceError).
//
// REAL data (plan §12.5): arXiv 1605.01488v2, "Fully dynamic data structure
// for LCE queries in compressed space", parsed twice — mineru 4.0.9 local
// (the plan §11 pinned version, pr_local, current) and the remote engine
// 3.4.4 (pr_remote). The slim fixtures under src/mocks/fixtures/ are
// GENERATED from the committed raw artifacts by scripts/gen-realpaper-mocks.mjs;
// never hand-edit block data here.
import parseLocal from './fixtures/realpaper-local.middle.json'
import parseRemote from './fixtures/realpaper-remote.middle.json'
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
  bbox?: number[] | null
}

type FixtureFile = {
  _provenance?: { raw_middle_json_sha256?: string }
} & RealFixtureFile

type RealFixtureFile = {
  schema: string
  schema_version: string
  producer: { name: string; version: string } | null
  pages: number
  blocks: FixtureBlock[]
}

const parseLocalFixture = parseLocal as unknown as FixtureFile
const parseRemoteFixture = parseRemote as unknown as FixtureFile
const sourcesFixtureTyped = sourcesFixture as unknown as {
  pdf_sha256: string
  sources: { source_id: string; origin: string; sha256: string; size_bytes: number }[]
}

export const PDF_SHA = sourcesFixtureTyped.pdf_sha256

// Two parse revisions of ONE real paper: pr_local is the current pointer
// (mineru 4.0.9, plan §11 pinned version); pr_remote is the same PDF parsed
// by the remote engine 3.4.4 — same docvortex.middle 2.0 schema, different
// block order on page 1 and slightly different LaTeX/bboxes.
export const REVISION_LOCAL = 'pr_local'
export const REVISION_REMOTE = 'pr_remote'

function asBlocks(file: FixtureFile): ReaderBlock[] {
  return file.blocks.map((block) => ({
    page_idx: block.page_idx,
    index: block.index,
    type: block.type,
    content: block.content,
    bbox: (block.bbox ?? null) as ReaderBlock['bbox'],
  }))
}

export const BLOCKS_LOCAL = asBlocks(parseLocalFixture)
export const BLOCKS_REMOTE = asBlocks(parseRemoteFixture)

export const SOURCES: PaperSource[] = sourcesFixtureTyped.sources.map((source) => ({
  ...source,
  created_at: '2026-09-01T00:00:00Z',
  is_current: true,
}))

export const PARSES: ParseRevision[] = [
  {
    revision_id: REVISION_LOCAL,
    source_id: sourcesFixtureTyped.sources[0].source_id,
    schema: parseLocalFixture.schema,
    schema_version: parseLocalFixture.schema_version,
    // sha256 of the raw local/middle.json the fixture was generated from.
    artifact_sha256: parseLocalFixture._provenance?.raw_middle_json_sha256 ?? '',
    created_at: '2026-09-20T10:00:00Z',
    is_current: true,
  },
  {
    revision_id: REVISION_REMOTE,
    source_id: sourcesFixtureTyped.sources[0].source_id,
    schema: parseRemoteFixture.schema,
    schema_version: parseRemoteFixture.schema_version,
    artifact_sha256: parseRemoteFixture._provenance?.raw_middle_json_sha256 ?? '',
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
// Identity fields (title/authors/arxiv id/pdf sha/size) are the REAL ones
// from arXiv 1605.01488v2; timestamps are registry-side mock dates.
export function mockPaperDetail(): PaperDetail {
  return {
    paper_id: MOCK_PAPER_ID,
    status: 'ready',
    arxiv_id: '1605.01488',
    title: 'Fully dynamic data structure for LCE queries in compressed space',
    authors: ['Takaaki Nishimoto', 'Tomohiro I', 'Shunsuke Inenaga', 'Hideo Bannai', 'Masayuki Takeda'],
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-23T12:00:00Z',
    assets: [
      {
        asset_id: 1,
        source: 'arxiv',
        arxiv_version: 2,
        pdf_sha256: PDF_SHA,
        pdf_size: 763119,
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

// Real anchors on pr_local (1-based public block numbers):
//   page 5 block 6  — equation  Uniq(P) = L̂₀·L₀ ⋯ (signature factors)
//   page 1 block 2  — doc_title (the paper title block)
//   page 1 block 9  — paragraph_title "Abstract"
//   page 7 block 14 — equation  ŷ_t^P case split
// pr_remote page 5 block 6 is the SAME displayed equation parsed by the
// remote engine — a DISTINCT anchor that must never merge with pr_local's.
export const DISCUSSIONS: DiscussionSummary[] = [
  discussion('dsc_01', {
    parse_revision: REVISION_LOCAL,
    page_idx: 4,
    block_index: 6,
    type: 'transcription_error',
    scope: 'public',
    status: 'confirmed',
    body: 'mineru 4.0.9 的 txt 模式把这条 Uniq(P) 公式转录成了逐字间隔的形式（“X S h r i n k _ {t} ^ {P}”）。与 PDF 原图对照后确认：字形与下标位置忠实，间隔是解析伪影而非原文排版；引用该公式前请以页面叠框对应的原图区域为准。',
    created_by: userAlma,
    model: 'agent-reader/0.9',
    revision: 3,
    reply_count: 2,
    created_at: '2026-09-21T08:00:00Z',
    updated_at: '2026-09-22T08:30:00Z',
  }),
  discussion('dsc_02', {
    parse_revision: REVISION_LOCAL,
    page_idx: 4,
    block_index: 6,
    type: 'typo_in_original',
    scope: 'lean',
    status: 'pending',
    body: 'Lean 形式化注意：该公式定义 Uniq(P) 为签名因子的连接，转录里的 h^P 上标层级在渲染中略有偏移。在核对 v2 原件扫描前，Lean 侧暂勿依赖此转录的记号约定，先以 Definition 3 的文字叙述为准。',
    created_by: userBo,
    model: null,
    revision: 1,
    reply_count: 1,
    created_at: '2026-09-21T09:15:00Z',
    updated_at: '2026-09-21T09:15:00Z',
  }),
  discussion('dsc_03', {
    parse_revision: REVISION_LOCAL,
    page_idx: 0,
    block_index: 2,
    type: 'normal',
    scope: 'public',
    status: null,
    body: '阅读笔记：这是 doc_title 块，左侧竖排的 arXiv 水印（块 1 aside_text）不在标题内。记录在此避免把水印行误当作者信息引用。',
    created_by: userCleo,
    model: null,
    revision: 1,
    reply_count: 0,
    created_at: '2026-09-21T10:00:00Z',
    updated_at: '2026-09-21T10:00:00Z',
  }),
  discussion('dsc_04', {
    parse_revision: REVISION_LOCAL,
    page_idx: 6,
    block_index: 14,
    type: 'transcription_error',
    scope: 'public',
    status: 'retracted',
    body: '最初怀疑 ŷ_t^P 分段定义的换行转录有误（cases 环境的行序）。重新对照第 7 页原图叠框后确认转录忠实，撤回该质疑；保留原文与回复作为查证记录。',
    created_by: userAlma,
    model: null,
    revision: 2,
    reply_count: 1,
    created_at: '2026-09-21T11:00:00Z',
    updated_at: '2026-09-23T12:00:00Z',
  }),
  discussion('dsc_05', {
    parse_revision: REVISION_LOCAL,
    page_idx: 0,
    block_index: 9,
    type: 'context_note',
    scope: 'lean',
    status: 'pending',
    body: 'Lean formalization note: the Abstract heading block carries no mathematical content — a finite-dimensional scaffold should skip it and start at Section 1.',
    created_by: userBo,
    model: 'lean-agent/1.2',
    revision: 1,
    reply_count: 0,
    created_at: '2026-09-22T14:00:00Z',
    updated_at: '2026-09-22T14:00:00Z',
  }),
  // Same VISUAL equation as pr_local page 5 block 6, but a DISTINCT anchor
  // in the pr_remote revision — never merged or auto-migrated (§12.5).
  discussion('dsc_06', {
    parse_revision: REVISION_REMOTE,
    page_idx: 4,
    block_index: 6,
    type: 'transcription_error',
    scope: 'public',
    status: 'pending',
    body: '远程引擎 3.4.4 对同一条 Uniq(P) 公式给出了略不同的 bbox 和 LaTeX（\\text { Shrink } 风格）。两个修订的锚点各自独立，评论不迁移——引用时注意区分 rev。',
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
      body: 'Checked the page-5 overlay against the cropped original: the letter spacing is a txt-mode parse artifact, glyph identity and subscript placement are faithful. Quote with the spacing normalized.',
      created_by: userBo,
      model: null,
      revision: 1,
      created_at: '2026-09-21T08:30:00Z',
      updated_at: '2026-09-21T08:30:00Z',
    },
    {
      reply_id: 'rpl_0102',
      body: '标记为已确认：具体问题是解析伪影而非转录错误，引用时请直接对照叠框对应的原图区域。',
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
      body: 'v2 source is the current pointer; re-check the h^P superscript layering once the original scan is re-rendered for comparison.',
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
      body: '复核无误：cases 环境行序与原图一致。同意撤回，理由已记录在状态历史中。',
      created_by: userBo,
      model: null,
      revision: 1,
      created_at: '2026-09-23T12:00:00Z',
      updated_at: '2026-09-23T12:00:00Z',
    },
  ],
}
