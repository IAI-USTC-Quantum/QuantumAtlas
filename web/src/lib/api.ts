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
