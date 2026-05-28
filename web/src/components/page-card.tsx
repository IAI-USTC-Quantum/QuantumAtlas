import { Link } from '@tanstack/react-router'

import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import { useLang } from '@/hooks/use-lang'
import type { PageSummary } from '@/lib/api'

export function PageCard({ page }: { page: PageSummary }) {
  const lang = useLang()
  return (
    <Link
      to="/$lang/wiki/page/$"
      params={{ lang, _splat: page.id }}
      className="group block focus-visible:outline-none"
    >
      <Card className="h-full gap-2 py-4 transition-colors group-hover:border-primary/40 group-hover:bg-accent/30 group-focus-visible:ring-[3px] group-focus-visible:ring-ring/50">
        <CardContent className="space-y-2 px-4">
          <strong className="block text-sm font-semibold text-foreground">
            {page.title}
          </strong>
          <span className="block truncate text-xs text-muted-foreground">
            {page.id}
          </span>
          <div className="flex flex-wrap gap-1.5 pt-1">
            {page.category && (
              <Badge variant="secondary">{page.category}</Badge>
            )}
            {(page.tags ?? []).slice(0, 3).map((tag) => (
              <Badge key={tag} variant="outline">
                {tag}
              </Badge>
            ))}
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}
