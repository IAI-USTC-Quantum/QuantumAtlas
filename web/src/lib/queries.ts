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
  type SyncStatus,
  type TheoremDetailPayload,
  type TheoremFamiliesPayload,
  type TheoremListPayload,
  type TheoremSourcePayload,
  type TheoremStats,
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

// --- Theorems -------------------------------------------------------------

// List the proved-Theorems catalog. family_id / audit_status are applied
// server-side; pass '' to omit a filter.
export function useTheorems(familyId: string, auditStatus: string) {
  return useQuery({
    queryKey: ['theorems', 'list', familyId, auditStatus],
    queryFn: () => {
      const params = new URLSearchParams()
      if (familyId) params.set('family_id', familyId)
      if (auditStatus) params.set('audit_status', auditStatus)
      const qs = params.toString()
      return getJson<TheoremListPayload>(`/api/theorems/list${qs ? `?${qs}` : ''}`)
    },
  })
}

export function useTheoremFamilies() {
  return useQuery({
    queryKey: ['theorems', 'families'],
    queryFn: () => getJson<TheoremFamiliesPayload>('/api/theorems/families'),
  })
}

export function useTheoremStats() {
  return useQuery({
    queryKey: ['theorems', 'stats'],
    queryFn: () => getJson<TheoremStats>('/api/theorems/stats'),
  })
}

export function useTheoremsSyncStatus() {
  return useQuery({
    queryKey: ['theorems', 'sync-status'],
    queryFn: () => getJson<SyncStatus>('/api/theorems/sync/status'),
  })
}

export function useTheoremDetail(fqn: string | null) {
  return useQuery({
    queryKey: ['theorems', 'detail', fqn],
    queryFn: () =>
      getJson<TheoremDetailPayload>(`/api/theorems/theorem/${encodeURIComponent(fqn!)}`),
    enabled: Boolean(fqn),
  })
}

// Source-on-demand: only fetched when `enabled` (the user clicked "Show
// source"), so the Lean file read is not paid for on every detail view.
export function useTheoremSource(fqn: string | null, enabled: boolean) {
  return useQuery({
    queryKey: ['theorems', 'source', fqn],
    queryFn: () =>
      getJson<TheoremSourcePayload>(`/api/theorems/theorem-source/${encodeURIComponent(fqn!)}`),
    enabled: Boolean(fqn) && enabled,
    retry: false,
  })
}
