// Block-comments reader API client (plan §12.2, v1).
//
// Types live in reader-types.ts; runtime helpers shared with the mock live
// in reader-shared.ts — both are leaf modules, which keeps this file's
// import of the mock client acyclic at runtime. The `live` fetchers hit
// the real /api endpoints; the `mock` dispatcher serves handwritten
// fixtures with the exact same response shapes so the UI can be developed
// and browser-tested before the backend lands (plan §12.1 “原型先行”).
// Mode is controlled by VITE_READER_API ('mock' default).
import { authHeaders } from './api'
import { mockClient } from '@/mocks/reader-mock-api'
import { MOCK_PAPER_ID, MOCK_PDF_PATH, ReaderApiError, scopeMatches, statusMatches } from './reader-shared'
import type {
  BlockReading,
  BlocksPage,
  DiscussionDetail,
  DiscussionsPage,
  PaperParsesResponse,
  PaperSourcesResponse,
  ReaderClient,
} from './reader-types'

export * from './reader-types'
export { MOCK_PAPER_ID, MOCK_PDF_PATH, ReaderApiError, scopeMatches, statusMatches }

export type ReaderApiMode = 'mock' | 'live'

export const READER_API_MODE: ReaderApiMode =
  import.meta.env.VITE_READER_API === 'live' ? 'live' : 'mock'

async function getReaderJson<T>(url: string, signal?: AbortSignal): Promise<T> {
  const response = await fetch(url, { headers: { ...authHeaders() }, signal })
  if (!response.ok) {
    let detail = ''
    try {
      const j = (await response.json()) as { detail?: string }
      detail = j.detail ?? ''
    } catch {
      // not JSON; fall through to bare status
    }
    throw new ReaderApiError(
      response.status,
      detail || `${response.status} ${response.statusText}`,
    )
  }
  return (await response.json()) as T
}

// Live client: real §12.2 endpoints. Only wired for the read paths the
// prototype needs; write paths (create/reply/status/edit) are added in the
// second delivery step.
function liveClient(): ReaderClient {
  return {
    listSources: (paperId, signal) =>
      getReaderJson<PaperSourcesResponse>(
        `/api/papers/${encodeURIComponent(paperId)}/sources`,
        signal,
      ),
    listParses: (paperId, signal) =>
      getReaderJson<PaperParsesResponse>(
        `/api/papers/${encodeURIComponent(paperId)}/parses`,
        signal,
      ),
    listBlocks: (paperId, revision, pageIdx, cursor, signal) => {
      const qs = new URLSearchParams({ page_idx: String(pageIdx) })
      if (cursor) qs.set('cursor', cursor)
      return getReaderJson<BlocksPage>(
        `/api/papers/${encodeURIComponent(paperId)}/parses/${encodeURIComponent(revision)}/blocks?${qs}`,
        signal,
      )
    },
    readBlock: (paperId, revision, pageIdx, blockIndex, signal) =>
      getReaderJson<BlockReading>(
        `/api/papers/${encodeURIComponent(paperId)}/parses/${encodeURIComponent(revision)}/blocks/${pageIdx}/${blockIndex}`,
        signal,
      ),
    listDiscussions: (paperId, filters, signal) => {
      const qs = new URLSearchParams()
      if (filters.scope) qs.set('scope', filters.scope)
      if (filters.type) qs.set('type', filters.type)
      if (filters.status) qs.set('status', filters.status)
      if (filters.parse_revision) qs.set('parse_revision', filters.parse_revision)
      if (filters.page_idx !== undefined) qs.set('page_idx', String(filters.page_idx))
      if (filters.block_index !== undefined) qs.set('block_index', String(filters.block_index))
      if (filters.cursor) qs.set('cursor', filters.cursor)
      return getReaderJson<DiscussionsPage>(
        `/api/papers/${encodeURIComponent(paperId)}/discussions?${qs}`,
        signal,
      )
    },
    getDiscussion: (discussionId, signal) =>
      getReaderJson<DiscussionDetail>(
        `/api/discussions/${encodeURIComponent(discussionId)}`,
        signal,
      ),
    fetchPdfBytes: async (paperId, sourceId, version, signal) => {
      const qs = version ? `?version=${encodeURIComponent(version)}` : ''
      const response = await fetch(
        `/api/papers/${encodeURIComponent(paperId)}/sources/${encodeURIComponent(sourceId)}/pdf${qs}`,
        { headers: { ...authHeaders() }, signal },
      )
      if (!response.ok) {
        throw new ReaderApiError(response.status, `${response.status} ${response.statusText}`)
      }
      return response.arrayBuffer()
    },
  }
}

export const readerClient: ReaderClient =
  READER_API_MODE === 'live' ? liveClient() : mockClient()
