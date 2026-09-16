import { useTranslation } from 'react-i18next'
import { PaperHitCard } from '@/components/paper-hit-card'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import type { RankedSearchResponse } from '@/lib/scoring-api'

export function ScorerResults({ results }: { results: RankedSearchResponse }) {
  const { t } = useTranslation('papers')
  return (
    <section className="space-y-3" aria-label={t('scoring.results')}>
      <p className="text-sm text-muted-foreground">{t('resultsHeader', { count: results.hits.length })}</p>
      {Object.entries(results.errors ?? {}).map(([source, error]) => (
        <Alert key={source} variant="destructive">
          <AlertTitle>{t('tabs.errorTitle', { backend: source })}</AlertTitle>
          <AlertDescription className="break-all">{error}</AlertDescription>
        </Alert>
      ))}
      {results.hits.length === 0 && <p>{t('noResults')}</p>}
      {/* Keep the exact server order across registry-backed and title-only hits. */}
      <ol className="space-y-3" data-testid="scorer-hits">
        {results.hits.map((hit, index) => (
          <li key={`${hit.paper_id ?? hit.title}-${index}`} className="space-y-2">
            <PaperHitCard hit={hit} rank={index + 1} paperId={hit.paper_id} created={hit.created} />
            {(hit.score_explanation != null || hit.score_detail != null) && (
              <details className="rounded-md border p-3 text-sm">
                <summary className="cursor-pointer">{t('scoring.explanation')}</summary>
                <pre className="mt-2 overflow-auto whitespace-pre-wrap break-all" data-testid="score-explanation">
                  {JSON.stringify({ score_detail: hit.score_detail, score_explanation: hit.score_explanation }, null, 2)}
                </pre>
              </details>
            )}
          </li>
        ))}
      </ol>
      <details className="text-sm">
        <summary className="cursor-pointer">{t('scoring.ranking')}</summary>
        <pre className="mt-2 overflow-auto whitespace-pre-wrap break-all">{JSON.stringify(results.ranking, null, 2)}</pre>
      </details>
    </section>
  )
}
