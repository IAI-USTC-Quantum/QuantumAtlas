// Mock papers-list dataset (plan §12.1 “原型先行”).
//
// In mock mode (VITE_READER_API unset/mock — the same switch the reader
// workbench uses) the registry list page has no backend to call: `vite dev`
// would answer /api/papers with the SPA fallback index.html and the JSON
// parse explodes with `Unexpected token '<'`. Instead of crashing, the list
// degrades gracefully to synthetic entries.
//
// The dataset deliberately contains ONLY the real-paper mock entry
// (arXiv 1605.01488, the production registry id): every row must stay
// clickable into a fully working reader (mock detail + real parse data +
// the committed source PDF). Adding a row without fixture data would turn
// the list into a launcher of "reader data unavailable" dead ends.
import type { PapersListItem, PapersListParams, PapersListResponse } from '@/lib/api'
import { MOCK_PAPER_ID } from '@/lib/reader-shared'

// Mirrors mockPaperDetail() in reader-mock-seed.ts (title/status/timestamps)
// so the list row and the detail page tell the same story.
const MOCK_PAPERS: PapersListItem[] = [
  {
    paper_id: MOCK_PAPER_ID,
    title: 'Fully dynamic data structure for LCE queries in compressed space',
    status: 'ready',
    has_pdf: true,
    has_md: true,
    image_count: 7,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-23T12:00:00Z',
  },
]

const PER_PAGE = 20

export function mockPapersList(params: PapersListParams): PapersListResponse {
  const filtered = MOCK_PAPERS.filter((paper) => {
    if (params.has_md !== undefined && paper.has_md !== params.has_md) return false
    if (params.status && paper.status !== params.status) return false
    if (params.q && !paper.title?.toLowerCase().includes(params.q.toLowerCase())) return false
    return true
  })
  const perPage = params.per_page ?? PER_PAGE
  const page = Math.max(1, params.page ?? 1)
  const start = (page - 1) * perPage
  return {
    items: filtered.slice(start, start + perPage),
    total: filtered.length,
    page,
    per_page: perPage,
  }
}
