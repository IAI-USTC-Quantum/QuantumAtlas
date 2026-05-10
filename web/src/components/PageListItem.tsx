import { Link } from '@tanstack/react-router'
import type { PageSummary } from '@/lib/api'
import { Badge } from './Badge'

export function PageListItem({ page }: { page: PageSummary }) {
  return (
    <Link
      to="/wiki/page/$"
      params={{ _splat: page.id }}
      className="list-item"
    >
      <span>
        <strong>{page.title}</strong>
        <small>{page.id}</small>
      </span>
      <Badge>{page.type}</Badge>
    </Link>
  )
}
