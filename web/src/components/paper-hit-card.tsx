import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import type { SearchHit } from '@/lib/api'
import { useLang } from '@/hooks/use-lang'
import { usePaperDetail } from '@/lib/queries'
import { PaperPipelineChip } from '@/components/paper-pipeline-chip'

type Props = {
  hit: SearchHit
  rank: number
  /** Registry paper this hit resolved to. When set, the title links to the paper detail page. */
  paperId?: string
  /** True when this search minted the registry paper (picked up by lazy ingestion). */
  created?: boolean
  /** Hide the fused score (multi-mode raw results carry no meaningful score). */
  hideScore?: boolean
}

// One search hit rendered as a card: title/authors/year, outbound
// buttons for the hit's identities (arXiv abs page, DOI resolver), the
// provider that produced it, and the merge score.
// Hits with a registry paper_id link through to the paper detail route;
// hits with only an external url render an outbound link; title-only
// candidates (neither) render without a link.
export function PaperHitCard({ hit, rank, paperId, created, hideScore }: Props) {
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
            ) : hit.url ? (
              <a
                href={hit.url}
                target="_blank"
                rel="noreferrer"
                className="truncate text-base font-medium text-foreground hover:text-primary hover:underline"
                title={title}
              >
                {title}
              </a>
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
          {!hideScore && (
            <span className="shrink-0 font-mono text-xs text-muted-foreground">
              {t('scoreLabel')}: {hit.score.toFixed(3)}
            </span>
          )}
        </div>

        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          {hit.arxiv_id && (
            <Button asChild variant="outline" size="xs" className="font-mono">
              <a
                href={`https://arxiv.org/abs/${hit.arxiv_id}`}
                target="_blank"
                rel="noreferrer"
                title={t('openArxiv')}
                aria-label={t('openArxiv')}
              >
                arXiv:{hit.arxiv_id}
              </a>
            </Button>
          )}
          {hit.doi && (
            <Button asChild variant="outline" size="xs" className="font-mono">
              <a
                href={`https://doi.org/${hit.doi}`}
                target="_blank"
                rel="noreferrer"
                title={t('openDoi')}
                aria-label={t('openDoi')}
              >
                doi:{hit.doi}
              </a>
            </Button>
          )}
          {hit.year ? <Badge variant="outline">{hit.year}</Badge> : null}
          <Badge variant="outline">{hit.source}</Badge>
          {hit.venue && <Badge variant="outline">{hit.venue}</Badge>}
          {typeof hit.citations === 'number' && hit.citations > 0 && (
            <Badge variant="outline" className="tabular-nums">
              {t('citationsLabel', { count: hit.citations })}
            </Badge>
          )}
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

        {paperId && <PaperPipelineChip acquisition={detail.data?.acquisition} />}
      </CardContent>
    </Card>
  )
}
