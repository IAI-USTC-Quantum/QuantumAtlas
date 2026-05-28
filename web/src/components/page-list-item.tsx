import { Link } from '@tanstack/react-router'

import { Badge } from '@/components/ui/badge'
import { useLang } from '@/hooks/use-lang'
import type { PageSummary } from '@/lib/api'

export function PageListItem({ page }: { page: PageSummary }) {
  const lang = useLang()
  return (
    <Link
      to="/$lang/wiki/page/$"
      params={{ lang, _splat: page.id }}
      className="group flex items-center justify-between gap-4 rounded-md border border-transparent px-3 py-2 transition-colors hover:border-border hover:bg-accent/30 focus-visible:border-ring focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-ring/50"
    >
      <span className="min-w-0 flex-1">
        <strong className="block truncate text-sm font-medium text-foreground">
          {page.title}
        </strong>
        <small className="block truncate text-xs text-muted-foreground">
          {page.id}
        </small>
      </span>
      <Badge variant="secondary" className="shrink-0">
        {page.type}
      </Badge>
    </Link>
  )
}
