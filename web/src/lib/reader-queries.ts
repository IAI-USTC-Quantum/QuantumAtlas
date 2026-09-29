// TanStack Query layer for the block-comments reader (plan §12.2 shapes).
//
// Query keys always include the paper id (and, where relevant, the parse
// revision) so switching papers or revisions can never serve another
// paper's data, mirroring the account/paper isolation rules the markdown
// preview already follows (web/MARKDOWN_PREVIEW.md). PDF bytes are cached
// with a long gcTime but invalidated per paper+source; the AbortSignal is
// consumed so a late response can't overwrite a closed viewer.
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from './auth'
import {
  READER_API_MODE,
  MOCK_PAPER_ID,
  readerClient,
  type CreateDiscussionInput,
  type DiscussionFilters,
  type DiscussionStatus,
  type ReaderBlock,
  type ReplyInput,
} from './reader-api'
import { useAdminWhoami, usePaperDetail } from './queries'
import { mockPaperDetail } from '@/mocks/reader-mock-seed'

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

// Blocks of one page, following the cursor chain until the terminal null.
// Real 17-page parses put up to ~21 top-level blocks on a page — more than
// one mock/live page of the blocks endpoint — and the overlay + list must
// show EVERY positioned block or boxes silently go missing (the exact
// "invisible blocks" symptom the real-paper preview exposed).
export function useParseBlocks(paperId: string | null, revision: string | null, pageIdx: number) {
  return useQuery({
    queryKey: ['reader-blocks', paperId, revision, pageIdx],
    queryFn: async ({ signal }) => {
      const blocks: ReaderBlock[] = []
      let cursor: string | undefined
      // Defensive cap: 100 pages × per-page size is far beyond any real
      // document; a server looping on the same cursor must not hang the tab.
      for (let hop = 0; hop < 100; hop += 1) {
        const page = await readerClient.listBlocks(paperId!, revision!, pageIdx, cursor, signal)
        blocks.push(...page.blocks)
        if (!page.next_cursor) break
        cursor = page.next_cursor
      }
      return { blocks }
    },
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

export function useDiscussionRevisions(discussionId: string | null, enabled: boolean) {
  return useQuery({
    queryKey: ['reader-discussion-revisions', discussionId],
    queryFn: ({ signal }) => readerClient.listRevisions(discussionId!, signal),
    enabled: Boolean(discussionId) && enabled,
    retry: false,
  })
}

// Who is acting? In mock mode the fixture actor decides (root author /
// admin semantics come from the mock store); live mode combines the
// session user id with the session-only admin whoami. Used ONLY to decide
// which controls to render — the server remains the authority.
export function useReaderActor(): { id: string; is_admin: boolean; ready: boolean } {
  const auth = useAuth()
  const whoami = useAdminWhoami()
  if (READER_API_MODE === 'mock') {
    return { id: 'user_alma', is_admin: false, ready: true }
  }
  return {
    id: auth.user?.id ?? '',
    is_admin: whoami.data?.is_admin ?? false,
    ready: !auth.isChecking,
  }
}

// Fresh idempotency key per submission attempt (§12.2: retries with the
// SAME key replay the original result — the key is minted when the user
// initiates a submission, not on every keystroke).
export function newIdempotencyKey(): string {
  if (typeof crypto !== 'undefined' && 'randomUUID' in crypto) return crypto.randomUUID()
  return `idem-${Date.now()}-${Math.random().toString(36).slice(2)}`
}

function useInvalidateReader() {
  const qc = useQueryClient()
  return () => {
    void qc.invalidateQueries({ queryKey: ['reader-discussions'] })
    void qc.invalidateQueries({ queryKey: ['reader-discussion'] })
    void qc.invalidateQueries({ queryKey: ['reader-block-reading'] })
  }
}

export function useCreateDiscussion(paperId: string) {
  const invalidate = useInvalidateReader()
  return useMutation({
    mutationFn: ({
      input,
      idempotencyKey,
    }: {
      input: CreateDiscussionInput
      idempotencyKey: string
    }) => readerClient.createDiscussion(paperId, input, idempotencyKey),
    onSuccess: invalidate,
    retry: false,
  })
}

export function useAddReply() {
  const invalidate = useInvalidateReader()
  return useMutation({
    mutationFn: ({
      discussionId,
      input,
      idempotencyKey,
    }: {
      discussionId: string
      input: ReplyInput
      idempotencyKey: string
    }) => readerClient.addReply(discussionId, input, idempotencyKey),
    onSuccess: invalidate,
    retry: false,
  })
}

export function useSetDiscussionStatus() {
  const invalidate = useInvalidateReader()
  return useMutation({
    mutationFn: ({
      discussionId,
      status,
      reason,
    }: {
      discussionId: string
      status: DiscussionStatus | null
      reason: string
    }) => readerClient.setStatus(discussionId, { status, reason }),
    onSuccess: invalidate,
    retry: false,
  })
}

export function useEditDiscussionBody() {
  const invalidate = useInvalidateReader()
  return useMutation({
    mutationFn: ({
      discussionId,
      body,
      ifMatchRevision,
    }: {
      discussionId: string
      body: string
      ifMatchRevision: number
    }) => readerClient.editDiscussionBody(discussionId, body, ifMatchRevision),
    onSuccess: invalidate,
    retry: false,
  })
}

export function useEditReplyBody() {
  const invalidate = useInvalidateReader()
  return useMutation({
    mutationFn: ({
      discussionId,
      replyId,
      body,
      ifMatchRevision,
    }: {
      discussionId: string
      replyId: string
      body: string
      ifMatchRevision: number
    }) => readerClient.editReplyBody(discussionId, replyId, body, ifMatchRevision),
    onSuccess: invalidate,
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
