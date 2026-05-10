import { Link } from '@tanstack/react-router'
import type { PageSummary } from '@/lib/api'
import { Badge } from './Badge'

export function PageCard({ page }: { page: PageSummary }) {
  return (
    <Link
      to="/wiki/page/$"
      params={{ _splat: page.id }}
      className="page-card"
    >
      <strong>{page.title}</strong>
      <span>{page.id}</span>
      <div className="tags">
        {page.category && <Badge>{page.category}</Badge>}
        {(page.tags ?? []).slice(0, 3).map((tag) => <Badge key={tag}>{tag}</Badge>)}
      </div>
    </Link>
  )
}
