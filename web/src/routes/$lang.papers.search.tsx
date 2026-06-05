import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Loader2, Search, Sparkles } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { RagResultCard } from '@/components/rag-result-card'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { useRagHealth, useRagSearch } from '@/lib/queries'

type SearchParams = {
  q?: string
  k?: number
  rerank?: boolean
  sparse?: boolean
}

export const Route = createFileRoute('/$lang/papers/search')({
  validateSearch: (search: Record<string, unknown>): SearchParams => ({
    q: typeof search.q === 'string' ? search.q : undefined,
    k: typeof search.k === 'number' && search.k > 0 && search.k <= 50 ? search.k : undefined,
    // accept "1"/"0"/true/false (URL strings come in coerced by upstream)
    rerank: search.rerank === false ? false : undefined,
    sparse: search.sparse === false ? false : undefined,
  }),
  component: PaperSearchPage,
})

function PaperSearchPage() {
  const { t } = useTranslation('papers')
  const lang = useLang()
  const navigate = useNavigate()
  const { q, k, rerank, sparse } = Route.useSearch()
  const query = q ?? ''
  const topK = k ?? 8
  const useRerank = rerank !== false
  const useSparse = sparse !== false

  const [pendingTopK, setPendingTopK] = useState(topK)
  const [pendingRerank, setPendingRerank] = useState(useRerank)
  const [pendingSparse, setPendingSparse] = useState(useSparse)

  const ragHealth = useRagHealth()
  const available = ragHealth.data?.status === 'ok'
  const degraded = ragHealth.data?.status === 'degraded'

  const results = useRagSearch(
    available && query
      ? { query, top_k: topK, rerank: useRerank, use_sparse: useSparse }
      : null,
  )

  function go(nextQuery: string) {
    navigate({
      to: '/$lang/papers/search',
      params: { lang },
      search: nextQuery
        ? {
            q: nextQuery,
            k: pendingTopK !== 8 ? pendingTopK : undefined,
            rerank: pendingRerank ? undefined : false,
            sparse: pendingSparse ? undefined : false,
          }
        : {},
    })
  }

  // Examples array comes from i18n. returnObjects: true makes i18next return
  // the underlying array instead of joining it. Type is asserted because
  // the default useTranslation signature returns string.
  const examples = t('queryExamples', { returnObjects: true }) as string[]

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />

      {!ragHealth.isLoading && !available && (
        <Panel
          title={degraded ? t('degradedTitle') : t('unavailableTitle')}
          icon={Sparkles}
        >
          <p className="text-sm text-muted-foreground">
            {degraded ? t('degradedBody') : t('unavailableBody')}
          </p>
        </Panel>
      )}

      <form
        className="space-y-3"
        onSubmit={(event) => {
          event.preventDefault()
          const form = new FormData(event.currentTarget)
          go(String(form.get('q') ?? '').trim())
        }}
      >
        <div className="flex items-center gap-2">
          <div className="relative flex-1">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              name="q"
              defaultValue={query}
              placeholder={t('inputPlaceholder')}
              className="pl-9"
              disabled={!available}
            />
          </div>
          <Button type="submit" disabled={!available}>
            {t('searchButton')}
          </Button>
        </div>

        <div className="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm">
          <label className="flex items-center gap-2">
            <span className="text-muted-foreground">{t('topKLabel')}:</span>
            <Input
              type="number"
              min={1}
              max={50}
              value={pendingTopK}
              onChange={(e) => setPendingTopK(Math.max(1, Math.min(50, Number(e.target.value) || 8)))}
              className="h-8 w-20"
              disabled={!available}
            />
          </label>
          <label className="flex items-center gap-2" title={t('rerankHint') ?? ''}>
            <input
              type="checkbox"
              checked={pendingRerank}
              onChange={(e) => setPendingRerank(e.target.checked)}
              disabled={!available}
              className="size-4 accent-primary"
            />
            <span className="text-muted-foreground">{t('rerankLabel')}</span>
          </label>
          <label className="flex items-center gap-2" title={t('useSparseHint') ?? ''}>
            <input
              type="checkbox"
              checked={pendingSparse}
              onChange={(e) => setPendingSparse(e.target.checked)}
              disabled={!available}
              className="size-4 accent-primary"
            />
            <span className="text-muted-foreground">{t('useSparseLabel')}</span>
          </label>
        </div>
      </form>

      {!query && available && (
        <Panel title={t('popularQueries')} icon={Sparkles}>
          <div className="flex flex-wrap gap-2">
            {examples.map((example) => (
              <Button
                key={example}
                type="button"
                variant="outline"
                size="sm"
                onClick={() => go(example)}
              >
                {example}
              </Button>
            ))}
          </div>
        </Panel>
      )}

      {query && available && (
        <StatusBlock
          loading={results.isLoading}
          error={results.error?.message ?? ''}
          empty={!results.isLoading && !results.data?.results.length}
          emptyMessage={t('noResults')}
        >
          {results.data && (
            <>
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                {results.isFetching && <Loader2 className="size-3 animate-spin" />}
                <span>
                  {t('resultsHeader', {
                    count: results.data.results.length,
                    took: results.data.took_s.toFixed(2),
                    reranked: results.data.reranked,
                  })}
                </span>
              </div>
              <div className="space-y-3">
                {results.data.results.map((hit, idx) => (
                  <RagResultCard
                    key={`${hit.canonical}-${hit.chunk_index}`}
                    hit={hit}
                    rank={idx + 1}
                  />
                ))}
              </div>
            </>
          )}
        </StatusBlock>
      )}
    </section>
  )
}
