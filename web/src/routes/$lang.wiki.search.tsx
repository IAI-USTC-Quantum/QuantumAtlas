import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Search, Sparkles } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/page-header'
import { PageListItem } from '@/components/page-list-item'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { useSearch } from '@/lib/queries'

type SearchParams = { q?: string }

export const Route = createFileRoute('/$lang/wiki/search')({
  validateSearch: (search: Record<string, unknown>): SearchParams => ({
    q: typeof search.q === 'string' ? search.q : undefined,
  }),
  component: SearchPage,
})

const POPULAR_TOPICS = [
  'quantum Fourier transform',
  'phase estimation',
  'amplitude amplification',
  'variational',
  'optimization',
] as const

function SearchPage() {
  const { t } = useTranslation('wiki')
  const lang = useLang()
  const { q } = Route.useSearch()
  const query = q ?? ''
  const navigate = useNavigate()
  const results = useSearch(query)

  function go(next: string) {
    navigate({
      to: '/$lang/wiki/search',
      params: { lang },
      search: next ? { q: next } : {},
    })
  }

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('searchEyebrow')}
        title={t('searchTitle')}
        copy={t('searchSubtitle')}
      />
      <form
        className="flex items-center gap-2"
        onSubmit={(event) => {
          event.preventDefault()
          const form = new FormData(event.currentTarget)
          go(String(form.get('q') ?? '').trim())
        }}
      >
        <div className="relative flex-1">
          <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
          <Input
            name="q"
            defaultValue={query}
            placeholder={t('searchInputPlaceholder')}
            className="pl-9"
          />
        </div>
        <Button type="submit">{t('searchEyebrow')}</Button>
      </form>

      {!query ? (
        <Panel title={t('popularTopics')} icon={Sparkles}>
          <div className="flex flex-wrap gap-2">
            {POPULAR_TOPICS.map((topic) => (
              <Button
                key={topic}
                type="button"
                variant="outline"
                size="sm"
                onClick={() => go(topic)}
              >
                {topic}
              </Button>
            ))}
          </div>
        </Panel>
      ) : (
        <Panel
          title={t('resultsFor', { query })}
          icon={Search}
          suffix={`${results.data?.total ?? 0}`}
        >
          <StatusBlock
            loading={results.isLoading}
            error={results.error?.message ?? ''}
            empty={!results.data?.results?.length}
          >
            <div className="flex flex-col">
              {(results.data?.results ?? []).map((page) => (
                <PageListItem key={page.id} page={page} />
              ))}
            </div>
          </StatusBlock>
        </Panel>
      )}
    </section>
  )
}
