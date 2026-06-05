import { Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import { Card, CardContent } from '@/components/ui/card'
import type { RagSearchHit } from '@/lib/api'
import { useLang } from '@/hooks/use-lang'

type Props = {
  hit: RagSearchHit
  rank: number
}

// One RAG hit rendered as a card. Optimised for "show what the RAG knows":
// surface every meaningful piece of chunk metadata (arxiv id+version, score,
// chunk index, char range, source object key) instead of hiding it behind
// a tidy summary. The wiki page link is best-effort: when no wiki entry
// exists for this paper the route's own not-found rendering kicks in.
export function RagResultCard({ hit, rank }: Props) {
  const { t } = useTranslation('papers')
  const lang = useLang()
  const title = hit.title || `${hit.canonical}v${hit.version}`
  const sectionPath = hit.section_path.filter((s) => s !== '__preamble__')

  return (
    <Card className="border-border/60">
      <CardContent className="space-y-3 p-4">
        <div className="flex items-baseline justify-between gap-3">
          <div className="flex min-w-0 items-baseline gap-2">
            <span className="shrink-0 font-mono text-xs text-muted-foreground tabular-nums">
              #{rank}
            </span>
            <h3 className="truncate text-base font-medium text-foreground" title={title}>
              {title}
            </h3>
          </div>
          <span className="shrink-0 font-mono text-xs text-muted-foreground">
            {t('ragScore')}: {hit.score.toFixed(3)}
          </span>
        </div>

        <div className="flex flex-wrap items-center gap-1.5 text-xs">
          <Badge variant="default" className="font-mono">
            {hit.canonical}v{hit.version}
          </Badge>
          <Badge variant="outline" className="font-mono">
            {t('chunkLabel')} #{hit.chunk_index}
          </Badge>
          <Badge variant="outline" className="font-mono">
            {t('charRange', { start: hit.char_start, end: hit.char_end })}
          </Badge>
          {(hit.categories ?? []).slice(0, 3).map((cat) => (
            <Badge key={cat} variant="secondary">
              {cat}
            </Badge>
          ))}
        </div>

        {sectionPath.length > 0 && (
          <div className="flex flex-wrap items-center gap-1 text-xs text-muted-foreground">
            {sectionPath.map((seg, i) => (
              <span key={`${i}-${seg}`} className="flex items-center gap-1">
                {i > 0 && <span aria-hidden>›</span>}
                <span>{seg}</span>
              </span>
            ))}
          </div>
        )}

        <p className="whitespace-pre-wrap text-sm leading-relaxed text-foreground/85">
          {hit.snippet}
        </p>

        <div className="flex flex-wrap items-center justify-between gap-2 border-t border-border/40 pt-2 text-xs text-muted-foreground">
          <span className="font-mono" title={t('sourceLabel') ?? ''}>
            {hit.md_object_key}
          </span>
          <Link
            to="/$lang/wiki/page/$"
            params={{ lang, _splat: hit.canonical }}
            className="font-medium text-primary hover:underline"
          >
            {t('viewSource')} →
          </Link>
        </div>
      </CardContent>
    </Card>
  )
}
