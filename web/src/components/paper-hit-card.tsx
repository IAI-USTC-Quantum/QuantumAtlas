import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import type { SearchHit } from '@/lib/api'
import { useLang } from '@/hooks/use-lang'
import { usePaperDetail } from '@/lib/queries'
import { PaperAcquisition } from '@/components/paper-acquisition'

type Props = {
  hit: SearchHit
  rank: number
  /** Registry paper this hit resolved to. When set, the title links to the paper detail page. */
  paperId?: string
  /** True when this search minted the registry paper (picked up by lazy ingestion). */
  created?: boolean
}

// One search hit rendered as a card: title/authors/year, identity badges
// (arxiv id, DOI), the provider that produced it, and the merge score.
// Hits with a registry paper_id link through to the paper detail route;
// title-only candidates (no paper_id) render without the link.
export function PaperHitCard({ hit, rank, paperId, created }: Props) {
  const { t } = useTranslation('papers')
  const lang = useLang()
  const detail = usePaperDetail(paperId ?? null)
  const title = hit.title || hit.arxiv_id || hit.doi || t('untitled')

  return (
    <Card className="border-border/60">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-baseline justify-between gap-3">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="shrink-0 font-mono text-xs text-muted-foreground tabular-nums">
              #{rank}
            </span>
            {paperId ? (
              <Link
                to="/$lang/papers/$paperId"
                params={{ lang, paperId }}
                className="truncate text-base font-medium text-foreground hover:text-primary hover:underline"
                title={title}
              >
                {title}
              </Link>
            ) : (
              <h3 className="truncate text-base font-medium text-foreground" title={title}>
                {title}
              </h3>
            )}
            {created && (
              <Badge variant="default" className="shrink-0">
                {t('newBadge')}
              </Badge>
            )}
          </div>
          <span className="shrink-0 font-mono text-xs text-muted-foreground">
            {t('scoreLabel')}: {hit.score.toFixed(3)}
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          {hit.arxiv_id && (
            <Badge variant="default" className="font-mono">
              arXiv:{hit.arxiv_id}
            </Badge>
          )}
          {hit.doi && (
            <Badge variant="secondary" className="font-mono">
              doi:{hit.doi}
            </Badge>
          )}
          {hit.year ? <Badge variant="outline">{hit.year}</Badge> : null}
          <Badge variant="outline">{hit.source}</Badge>
        </div>

        {hit.authors && hit.authors.length > 0 && (
          <p className="truncate text-sm text-muted-foreground" title={hit.authors.join(', ')}>
            {hit.authors.join(', ')}
          </p>
        )}

        {hit.abstract && (
          <p className="line-clamp-3 text-sm leading-6 text-muted-foreground">
            {hit.abstract}
          </p>
        )}

        {paperId && (
          <PaperAcquisition acquisition={detail.data?.acquisition} compact />
        )}
      </CardContent>
    </Card>
  )
}
