// Mock reader client (plan §12.1 "原型先行", §12.5 fixtures).
//
// Reads come from the mutable in-memory store (seeded from
// reader-mock-seed.ts, whose block data is GENERATED from the real
// arXiv 1605.01488 parse artifacts in tests/fixtures/realpaper/);
// writes implement the §12.2 semantics. Discussions are synthetic but
// anchored to REAL equation blocks.
import {
  BLOCKS_LOCAL,
  BLOCKS_REMOTE,
  PARSES,
  PDF_SHA,
  REVISION_LOCAL,
  REVISION_REMOTE,
  requireMockPaper,
  SOURCES,
} from './reader-mock-seed'
import {
  blocksForRevision,
  listStoredDiscussions,
  mockAddReply,
  mockCreateDiscussion,
  mockEditDiscussionBody,
  mockEditReplyBody,
  mockSetStatus,
  storedDiscussion,
  storedRevisions,
} from './reader-mock-store'
import { MOCK_PAPER_ID, MOCK_PDF_PATH, ReaderApiError, scopeMatches, statusMatches } from '@/lib/reader-shared'
import type {
  BlockReading,
  BlocksPage,
  DiscussionDetail,
  DiscussionsPage,
  PaperParsesResponse,
  PaperSourcesResponse,
  ReaderClient,
  ReaderBlock,
} from '@/lib/reader-types'

// Re-exported for the unit tests and the preview helper.
export { BLOCKS_LOCAL, BLOCKS_REMOTE, REVISION_LOCAL, REVISION_REMOTE }
export { mockPaperDetail } from './reader-mock-seed'
export { DISCUSSIONS } from './reader-mock-seed'

const PER_PAGE = 20

function paginate<T>(items: T[], cursor?: string): { slice: T[]; next: string | null } {
  const start = cursor ? Number(cursor) : 0
  if (!Number.isInteger(start) || start < 0) {
    throw new Error('reader mock: bad cursor')
  }
  const slice = items.slice(start, start + PER_PAGE)
  const next = start + PER_PAGE < items.length ? String(start + PER_PAGE) : null
  return { slice, next }
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
      const onPage: ReaderBlock[] = blocksForRevision(revision).filter((b) => b.page_idx === pageIdx)
      const { slice, next } = paginate(onPage, cursor)
      return { blocks: slice, next_cursor: next } satisfies BlocksPage
    },
    async readBlock(paperId, revision, pageIdx, blockIndex) {
      requireMockPaper(paperId)
      const block = blocksForRevision(revision).find(
        (b) => b.page_idx === pageIdx && b.index === blockIndex,
      )
      // Golden-anchor rule: a missing block 404s — never fall back to the
      // nearest block (§12.5).
      if (!block) {
        throw new ReaderApiError(
          404,
          `block page_idx=${pageIdx} block_index=${blockIndex} not found in ${revision}`,
        )
      }
      const parse = PARSES.find((p) => p.revision_id === revision)
      if (!parse) throw new ReaderApiError(404, `unknown parse revision ${revision}`)
      const discussions = listStoredDiscussions().filter(
        (d) =>
          d.paper_id === paperId &&
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
      const filtered = listStoredDiscussions().filter(
        (d) =>
          d.paper_id === paperId &&
          scopeMatches(filters.scope, d.scope) &&
          statusMatches(filters.status, d.status) &&
          (!filters.type || d.type === filters.type) &&
          (filters.parse_revision === undefined || d.parse_revision === filters.parse_revision) &&
          (filters.page_idx === undefined || d.page_idx === filters.page_idx) &&
          (filters.block_index === undefined || d.block_index === filters.block_index),
      )
      const { slice, next } = paginate(filtered, filters.cursor)
      return { discussions: slice, next_cursor: next } satisfies DiscussionsPage
    },
    async getDiscussion(discussionId) {
      const stored = storedDiscussion(discussionId)
      return {
        ...stored,
        replies: stored.replies.map((r) => ({ ...r })),
        replies_next_cursor: null,
      } satisfies DiscussionDetail
    },
    async fetchPdfBytes(paperId) {
      requireMockPaper(paperId)
      const response = await fetch(MOCK_PDF_PATH)
      if (!response.ok) {
        throw new ReaderApiError(response.status, `mock pdf fetch failed: ${response.status}`)
      }
      return response.arrayBuffer()
    },
    // async wrappers so validation throws surface as rejected promises,
    // exactly like fetch-based failures would (sync throws would escape
    // callers that only await).
    createDiscussion: async (paperId, input, key) => mockCreateDiscussion(paperId, input, key),
    addReply: async (discussionId, input, key) => mockAddReply(discussionId, input, key),
    setStatus: async (discussionId, input) => mockSetStatus(discussionId, input),
    editDiscussionBody: async (discussionId, body, ifMatch) =>
      mockEditDiscussionBody(discussionId, body, ifMatch),
    editReplyBody: async (discussionId, replyId, body, ifMatch) =>
      mockEditReplyBody(discussionId, replyId, body, ifMatch),
    listRevisions: async (discussionId) => storedRevisions(discussionId),
  }
}

export { MOCK_PAPER_ID }
