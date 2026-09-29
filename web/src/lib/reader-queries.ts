// TanStack Query layer for the block-comments reader (plan §12.2 shapes).
//
// Query keys always include the paper id (and, where relevant, the parse
// revision) so switching papers or revisions can never serve another
// paper's data, mirroring the account/paper isolation rules the markdown
// preview already follows (web/MARKDOWN_PREVIEW.md). PDF bytes are cached
// with a long gcTime but invalidated per paper+source; the AbortSignal is
// consumed so a late response can't overwrite a closed viewer.
import { useQuery } from '@tanstack/react-query'
import { useAuth } from './auth'
import { READER_API_MODE, MOCK_PAPER_ID, readerClient, type DiscussionFilters } from './reader-api'
import { usePaperDetail } from './queries'
import { mockPaperDetail } from '@/mocks/reader-mock-api'

// Paper detail for reader pages: in mock mode the fixture paper has no
// backend row, so serve the synthetic detail (lets `npm run dev` preview
// the workbench with zero backend); every other paper hits the real API.
export function usePaperDetailForReader(paperId: string | null) {
  const live = usePaperDetail(paperId)
  const useMock =
    READER_API_MODE === 'mock' && paperId === MOCK_PAPER_ID
  const mocked = useQuery({
    queryKey: ['reader-mock-paper-detail', paperId],
    queryFn: () => mockPaperDetail(),
    enabled: useMock,
    staleTime: Infinity,
    retry: false,
  })
  return useMock ? mocked : live
}

export function usePaperSources(paperId: string | null) {
  return useQuery({
    queryKey: ['reader-sources', paperId],
    queryFn: ({ signal }) => readerClient.listSources(paperId!, signal),
    enabled: Boolean(paperId),
    retry: false,
  })
}

export function usePaperParses(paperId: string | null) {
  return useQuery({
    queryKey: ['reader-parses', paperId],
    queryFn: ({ signal }) => readerClient.listParses(paperId!, signal),
    enabled: Boolean(paperId),
    retry: false,
  })
}

export function useParseBlocks(paperId: string | null, revision: string | null, pageIdx: number) {
  return useQuery({
    queryKey: ['reader-blocks', paperId, revision, pageIdx],
    queryFn: ({ signal }) => readerClient.listBlocks(paperId!, revision!, pageIdx, undefined, signal),
    enabled: Boolean(paperId && revision),
    retry: false,
  })
}

export function useBlockReading(
  paperId: string | null,
  revision: string | null,
  anchor: { page_idx: number; block_index: number } | null,
) {
  return useQuery({
    queryKey: [
      'reader-block-reading',
      paperId,
      revision,
      anchor?.page_idx,
      anchor?.block_index,
    ],
    queryFn: ({ signal }) =>
      readerClient.readBlock(paperId!, revision!, anchor!.page_idx, anchor!.block_index, signal),
    enabled: Boolean(paperId && revision && anchor),
    retry: false,
  })
}

export function useDiscussions(paperId: string | null, filters: DiscussionFilters) {
  return useQuery({
    queryKey: ['reader-discussions', paperId, filters],
    queryFn: ({ signal }) => readerClient.listDiscussions(paperId!, filters, signal),
    enabled: Boolean(paperId),
    retry: false,
  })
}

export function useDiscussionDetail(discussionId: string | null) {
  return useQuery({
    queryKey: ['reader-discussion', discussionId],
    queryFn: ({ signal }) => readerClient.getDiscussion(discussionId!, signal),
    enabled: Boolean(discussionId),
    retry: false,
  })
}

// Source PDF bytes. Keyed by paper+source so account/paper switches fetch
// fresh bytes; the response is an ArrayBuffer held in the query cache (no
// object URL to revoke — pdfjs consumes the buffer directly).
export function usePaperPdfBytes(paperId: string | null, sourceId: string | null) {
  const auth = useAuth()
  return useQuery({
    queryKey: ['reader-pdf', auth.user?.id, paperId, sourceId],
    queryFn: ({ signal }) => readerClient.fetchPdfBytes(paperId!, sourceId!, undefined, signal),
    enabled: Boolean(paperId && sourceId),
    retry: false,
    gcTime: 5 * 60_000,
  })
}
