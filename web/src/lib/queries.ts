import { useQuery } from '@tanstack/react-query'
import {
  getJson,
  ragHealth,
  ragSearch,
  type GraphStats,
  type PageDetail,
  type PageListPayload,
  type PaperStats,
  type RagSearchRequest,
  type SearchPayload,
  type Stats,
} from './api'

export function useStats() {
  return useQuery({
    queryKey: ['stats'],
    queryFn: () => getJson<Stats>('/api/stats'),
  })
}

export function usePaperStats() {
  return useQuery({
    queryKey: ['paper-stats'],
    queryFn: () => getJson<PaperStats>('/api/papers/stats'),
  })
}

export function usePages() {
  return useQuery({
    queryKey: ['pages'],
    queryFn: () => getJson<PageListPayload>('/api/pages'),
  })
}

export function usePage(pageId: string | null) {
  return useQuery({
    queryKey: ['page', pageId],
    queryFn: () => getJson<PageDetail>(`/api/pages/${encodeURIComponent(pageId!)}`),
    enabled: Boolean(pageId),
  })
}

export function useSearch(query: string) {
  return useQuery({
    queryKey: ['search', query],
    queryFn: () =>
      getJson<SearchPayload>(`/api/search?q=${encodeURIComponent(query)}&limit=20`),
    enabled: Boolean(query),
  })
}

// Semantic search against /api/rag/search; only meaningful when
// useRagSearch runs a vector search against the operator-deployed RAG
// sidecar via the qatlasd reverse-proxy. Pass `null` to keep the hook
// idle (e.g. while the user is still typing or RAG isn't available).
// Caller fully controls top_k / rerank / use_sparse / filters so the
// page UI can expose them without growing the hook signature each time.
export function useRagSearch(req: RagSearchRequest | null) {
  return useQuery({
    queryKey: [
      'rag-search',
      req?.query ?? '',
      req?.top_k ?? 8,
      req?.rerank ?? true,
      req?.use_sparse ?? true,
      req?.rerank_pool ?? null,
      JSON.stringify(req?.filters ?? null),
    ],
    queryFn: () => ragSearch(req as RagSearchRequest),
    enabled: req !== null && Boolean(req.query),
    retry: false,
  })
}

// Probe whether the server advertises a RAG sidecar. Cached for 5
// minutes — operators who flip the switch will see the toggle within
// that window. Returns `true` only when the probe returned a
// {"status":"ok"} body; `degraded` / `down` / 404 / network error all
// hide the toggle.
export function useRagHealth() {
  return useQuery({
    queryKey: ['rag-health'],
    queryFn: () => ragHealth(),
    staleTime: 5 * 60 * 1000,
    retry: false,
  })
}

export function useGraphStats() {
  return useQuery({
    queryKey: ['graph', 'stats'],
    queryFn: () => getJson<GraphStats>('/api/graph/stats'),
  })
}
