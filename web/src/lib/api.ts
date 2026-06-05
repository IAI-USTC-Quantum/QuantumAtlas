export type Stats = {
  total_pages?: number
  entries?: number
  sources?: number
  by_type?: Record<string, number>
  by_status?: Record<string, number>
  by_category?: Record<string, number>
}

export type PaperStats = {
  available: boolean
  total?: number
  has_pdf?: number
  has_md?: number
  has_json?: number
  needs_mineru?: number
  total_images?: number
  loaded_at?: string
}

export type PageSummary = {
  id: string
  title: string
  type: string
  category?: string | null
  status?: string
  tags?: string[]
}

export type PageDetail = PageSummary & {
  content?: string | null
  created_at?: string | null
  updated_at?: string | null
}

export type PageListPayload = {
  total: number
  pages: PageSummary[]
}

export type SearchPayload = {
  query: string
  total: number
  results: PageSummary[]
}

export type GraphStats = {
  nodes?: number
  relationships?: number
  labels?: string[]
  label_counts?: Record<string, number>
  error?: string
  [key: string]: number | string | string[] | Record<string, number> | undefined
}

import { pb } from './pb'

// Attach the current PocketBase auth token (if any) to outbound fetches so
// that protected /api/* endpoints accept us. Reads via the SDK so token
// rotation (authRefresh) is picked up automatically.
function authHeaders(): Record<string, string> {
  const token = pb.authStore.token
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export async function getJson<T>(url: string): Promise<T> {
  const response = await fetch(url, { headers: { ...authHeaders() } })
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

export async function postJson<T>(url: string, body: unknown): Promise<T> {
  const response = await fetch(url, {
    method: 'POST',
    headers: { ...authHeaders(), 'content-type': 'application/json' },
    body: JSON.stringify(body),
  })
  if (!response.ok) {
    // Try to surface the server-side detail (qatlasd returns
    // {"detail": "..."} on 4xx/5xx). Fall back to bare status text.
    let detail = ''
    try {
      const j = (await response.json()) as { detail?: string }
      detail = j.detail ?? ''
    } catch {
      // not JSON; ignore
    }
    throw new Error(detail ? `${response.status}: ${detail}` : `${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

// --- RAG semantic search -------------------------------------------------
//
// Posts to /api/rag/search; qatlasd reverse-proxies to the configured
// sidecar. Returns chunk-level hits with section path + snippet.
//
// The route is only registered when the operator sets both
// QATLAS_PAPER_ACCESS_ENABLED=true and QATLAS_RAG_SIDECAR_URL; otherwise
// the endpoint 404s. The SPA discovers availability by polling
// /api/rag/healthz (see useRagHealth) and only renders the semantic
// toggle when the probe returns 200.

export type RagSearchRequest = {
  query: string
  top_k?: number
  rerank?: boolean
  rerank_pool?: number
  use_sparse?: boolean
  filters?: Record<string, string>
}

export type RagSearchHit = {
  arxiv_id: string
  canonical: string
  yymm: string
  version: number
  title?: string | null
  authors?: string[] | null
  categories?: string[] | null
  section_path: string[]
  chunk_index: number
  snippet: string
  score: number
  md_object_key: string
  char_start: number
  char_end: number
  image_refs: string[]
}

export type RagSearchPayload = {
  query: string
  took_s: number
  reranked: boolean
  results: RagSearchHit[]
}

export type RagHealthPayload = {
  status: 'ok' | 'degraded' | 'down'
}

export async function ragSearch(body: RagSearchRequest): Promise<RagSearchPayload> {
  return postJson<RagSearchPayload>('/api/rag/search', body)
}

// Anonymous probe — does not include auth headers so it works regardless
// of session state. Returns null when the endpoint is not registered (404
// because either QATLAS_PAPER_ACCESS_ENABLED is off or
// QATLAS_RAG_SIDECAR_URL is unset on the server).
export async function ragHealth(): Promise<RagHealthPayload | null> {
  try {
    const r = await fetch('/api/rag/healthz')
    if (!r.ok) return null
    return (await r.json()) as RagHealthPayload
  } catch {
    return null
  }
}

export function statValue(stats: Stats | null | undefined, group: keyof Stats, key: string) {
  const value = stats?.[group]
  if (!value || typeof value !== 'object') return 0
  return (value as Record<string, number>)[key] ?? 0
}

export function graphLabelCounts(stats: GraphStats | null | undefined) {
  if (!stats) return {}
  if (stats.label_counts) return stats.label_counts

  const counts: Record<string, number> = {}
  for (const [key, value] of Object.entries(stats)) {
    if (['nodes', 'relationships', 'labels', 'error'].includes(key)) continue
    if (typeof value === 'number') counts[key] = value
  }
  return counts
}
