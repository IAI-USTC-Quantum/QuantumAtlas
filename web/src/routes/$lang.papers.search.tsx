import { useState } from 'react'
import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { AlertCircle, Loader2, Search, Sparkles, Wand2 } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { PaperHitCard } from '@/components/paper-hit-card'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { AgenticSearchError, isSearchRemoteAvailable } from '@/lib/api'
import { useAgenticSearch, usePaperSearch, usePlugins } from '@/lib/queries'

type SearchParams = {
  q?: string
}

export const Route = createFileRoute('/$lang/papers/search')({
  validateSearch: (search: Record<string, unknown>): SearchParams => ({
    q: typeof search.q === 'string' ? search.q : undefined,
  }),
  component: PaperSearchPage,
})

function PaperSearchPage() {
  const { t } = useTranslation('papers')
  const lang = useLang()
  const navigate = useNavigate()
  const { q } = Route.useSearch()
  const query = q ?? ''

  // Agentic mode toggle. The switch is gated on the `search-remote` plugin
  // being enabled and connected; when the microservice is not there we
  // force classic mode and disable the toggle.
  const plugins = usePlugins()
  const agenticAvailable = isSearchRemoteAvailable(plugins.data?.plugins)
  const [agenticWanted, setAgenticWanted] = useState(false)
  const agenticOn = agenticWanted && agenticAvailable

  const entry = query ? { text: query } : null
  const classicResults = usePaperSearch(agenticOn ? null : entry)
  const agenticResults = useAgenticSearch(agenticOn ? entry : null)
  const results = agenticOn ? agenticResults : classicResults

  function go(nextQuery: string) {
    navigate({
      to: '/$lang/papers/search',
      params: { lang },
      search: nextQuery ? { q: nextQuery } : {},
    })
  }

  // Examples array comes from i18n. returnObjects: true makes i18next return
  // the underlying array instead of joining it. Type is asserted because
  // the default useTranslation signature returns string.
  const examples = t('queryExamples', { returnObjects: true }) as string[]

  const candidates = results.data?.candidates ?? []

  // Agentic-only extras: the LLM conclusion card and the daily usage badge.
  const agenticData = agenticOn ? agenticResults.data : undefined
  const conclusion = agenticData?.conclusion ?? null
  const usage = agenticData?.usage

  // 429: the daily quota is exhausted. The body carries today's usage, so
  // render a dedicated quota message instead of a bare error.
  const err = results.error
  const rateLimitError =
    err instanceof AgenticSearchError && err.status === 429 ? err : undefined

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />

      <form
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
            />
          </div>
          <Button type="submit">{t('searchButton')}</Button>
        </div>
        <div className="mt-2 flex items-center gap-2">
          <Button
            type="button"
            variant={agenticOn ? 'default' : 'outline'}
            size="sm"
            disabled={!agenticAvailable}
            onClick={() => setAgenticWanted((v) => !v)}
            title={
              agenticAvailable
                ? t('agentic.toggleHint')
                : t('agentic.unavailable')
            }
          >
            <Wand2 className="size-4" />
            {t('agentic.toggle')}
          </Button>
          {!agenticAvailable && (
            <span className="text-xs text-muted-foreground">
              {t('agentic.unavailable')}
            </span>
          )}
        </div>
      </form>

      {!query && (
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

      {query && rateLimitError ? (
        <Alert>
          <AlertCircle className="size-4" />
          <AlertTitle>{t('agentic.rateLimitedTitle')}</AlertTitle>
          <AlertDescription>
            {rateLimitError.usage
              ? t('agentic.rateLimited', {
                  today: rateLimitError.usage.today,
                  limit: rateLimitError.usage.limit,
                })
              : rateLimitError.message}
          </AlertDescription>
        </Alert>
      ) : (
        query && (
          <StatusBlock
            loading={results.isLoading}
            error={err?.message ?? ''}
            empty={!results.isLoading && !results.data?.results.length && !candidates.length}
            emptyMessage={t('noResults')}
          >
            {results.data && (
              <>
                <div className="flex items-center gap-2 text-sm text-muted-foreground">
                  {results.isFetching && <Loader2 className="size-3 animate-spin" />}
                  <span>
                    {t('resultsHeader', { count: results.data.results.length })}
                  </span>
                  {usage && (
                    <Badge variant="outline" className="ml-auto tabular-nums">
                      {t('agentic.usage', {
                        today: usage.today,
                        limit: usage.limit,
                      })}
                    </Badge>
                  )}
                </div>

                {conclusion && (
                  <Panel title={t('agentic.conclusionTitle')} icon={Wand2}>
                    <p className="whitespace-pre-line text-sm leading-6">
                      {conclusion}
                    </p>
                  </Panel>
                )}

                <div className="space-y-3">
                  {results.data.results.map((result, idx) => (
                    <PaperHitCard
                      key={result.paper_id}
                      hit={result.hit}
                      rank={idx + 1}
                      paperId={result.paper_id}
                      created={result.created}
                    />
                  ))}
                </div>

                {candidates.length > 0 && (
                  <Panel
                    title={t('candidatesTitle')}
                    icon={Sparkles}
                    suffix={`${candidates.length}`}
                  >
                    <p className="mb-3 text-sm text-muted-foreground">
                      {t('candidatesHint')}
                    </p>
                    <div className="space-y-3">
                      {candidates.map((hit, idx) => (
                        <PaperHitCard
                          key={`${hit.title}-${idx}`}
                          hit={hit}
                          rank={idx + 1}
                        />
                      ))}
                    </div>
                  </Panel>
                )}
              </>
            )}
          </StatusBlock>
        )
      )}
    </section>
  )
}
