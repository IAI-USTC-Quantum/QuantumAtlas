// Block-comments reader API types (plan §12.2, v1) — pure types, no runtime
// imports. Kept in a leaf module so the API client, the mock client and the
// shared helpers can all depend on them without import cycles.

// --- Q1: sources, parses, blocks ---------------------------------------------

export type PaperSource = {
  source_id: string
  origin: string // e.g. 'arxiv:v2' | 'upload'
  sha256: string
  size_bytes: number
  created_at?: string
  is_current?: boolean
}

export type PaperSourcesResponse = { sources: PaperSource[] }

export type ParseRevision = {
  revision_id: string
  source_id: string
  schema: string
  schema_version: string
  artifact_sha256: string
  created_at?: string
  is_current: boolean
}

export type PaperParsesResponse = { parses: ParseRevision[] }

// Top-level block of a parse JSON. bbox is [x0, y0, x1, y1] normalized to
// [0,1] relative to the source page (plan §5.1); absent bbox means the
// block cannot be located on the page — never fabricate a frame.
export type ReaderBlock = {
  page_idx: number
  index: number // public 1-based block number (block.index)
  type: string
  content: string
  bbox?: [number, number, number, number] | null
}

export type BlocksPage = {
  blocks: ReaderBlock[]
  next_cursor: string | null
}

export type BlockAnchor = {
  page_idx: number
  block_index: number
}

export type BlockSourceInfo = {
  paper_id: string
  source_id: string
  pdf_sha256: string
  parse_revision: string
  artifact_sha256: string
  schema: string
  schema_version: string
}

// Combined single-block read (GET …/blocks/{page_idx}/{block_index}):
// source + anchor + content + discussions + next_cursor (plan §7.1).
export type BlockReading = {
  source: BlockSourceInfo
  anchor: BlockAnchor
  content: { type: string; content: string; bbox?: [number, number, number, number] | null }
  discussions: DiscussionSummary[]
  next_cursor: string | null
}

// --- Q2: discussions ----------------------------------------------------------

export type DiscussionStatus = 'pending' | 'confirmed' | 'retracted'
export type DiscussionScope = 'public' | 'lean'

export type DiscussionSummary = {
  discussion_id: string
  paper_id: string
  parse_revision: string
  page_idx: number
  block_index: number
  type: string // bounded tag: preset slugs or custom [a-z0-9_:-]+
  scope: DiscussionScope
  status: DiscussionStatus | null
  body: string
  created_by: string
  model?: string | null
  revision: number
  reply_count: number
  created_at: string
  updated_at: string
}

export type ReplyEntry = {
  reply_id: string
  discussion_id: string
  body: string
  created_by: string
  model?: string | null
  revision: number
  created_at: string
  updated_at: string
}

export type DiscussionDetail = DiscussionSummary & {
  replies: ReplyEntry[]
  replies_next_cursor: string | null
}

export type DiscussionsPage = {
  discussions: DiscussionSummary[]
  next_cursor: string | null
}

export type DiscussionFilters = {
  scope?: DiscussionScope | ''
  type?: string
  status?: DiscussionStatus | 'none' | ''
  page_idx?: number
  block_index?: number
  parse_revision?: string
  cursor?: string
}

// --- Client contract -----------------------------------------------------------

export type ReaderClient = {
  listSources(paperId: string, signal?: AbortSignal): Promise<PaperSourcesResponse>
  listParses(paperId: string, signal?: AbortSignal): Promise<PaperParsesResponse>
  listBlocks(
    paperId: string,
    revision: string,
    pageIdx: number,
    cursor?: string,
    signal?: AbortSignal,
  ): Promise<BlocksPage>
  readBlock(
    paperId: string,
    revision: string,
    pageIdx: number,
    blockIndex: number,
    signal?: AbortSignal,
  ): Promise<BlockReading>
  listDiscussions(
    paperId: string,
    filters: DiscussionFilters,
    signal?: AbortSignal,
  ): Promise<DiscussionsPage>
  getDiscussion(discussionId: string, signal?: AbortSignal): Promise<DiscussionDetail>
  fetchPdfBytes(
    paperId: string,
    sourceId: string,
    version: string | undefined,
    signal?: AbortSignal,
  ): Promise<ArrayBuffer>
}
