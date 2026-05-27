import { useQuery } from '@tanstack/react-query'
import {
  getJson,
  type GraphStats,
  type PageDetail,
  type PageListPayload,
  type SearchPayload,
  type Stats,
} from './api'

export function useStats() {
  return useQuery({
    queryKey: ['stats'],
    queryFn: () => getJson<Stats>('/api/stats'),
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

export function useGraphStats() {
  return useQuery({
    queryKey: ['graph', 'stats'],
    queryFn: () => getJson<GraphStats>('/api/graph/stats'),
  })
}
