import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Search, Sparkles } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { PageListItem } from '@/components/PageListItem'
import { Panel } from '@/components/Panel'
import { StatusBlock } from '@/components/StatusBlock'
import { useSearch } from '@/lib/queries'

type SearchParams = { q?: string }

export const Route = createFileRoute('/wiki/search')({
  validateSearch: (search: Record<string, unknown>): SearchParams => ({
    q: typeof search.q === 'string' ? search.q : undefined,
  }),
  component: SearchPage,
})

function SearchPage() {
  const { q } = Route.useSearch()
  const query = q ?? ''
  const navigate = useNavigate()
  const results = useSearch(query)

  return (
    <section className="stack">
      <PageHeader eyebrow="Search" title="Find wiki pages" copy="Search titles, tags, page ids, and extracted content." />
      <form
        className="search-panel"
        onSubmit={(event) => {
          event.preventDefault()
          const form = new FormData(event.currentTarget)
          const next = String(form.get('q') ?? '').trim()
          navigate({ to: '/wiki/search', search: next ? { q: next } : {} })
        }}
      >
        <Search size={18} />
        <input name="q" defaultValue={query} placeholder="quantum Fourier transform" />
        <button type="submit">Search</button>
      </form>

      {!query ? (
        <Panel title="Popular topics" icon={Sparkles}>
          <div className="chips">
            {['quantum Fourier transform', 'phase estimation', 'amplitude amplification', 'variational', 'optimization'].map((topic) => (
              <button
                key={topic}
                onClick={() => navigate({ to: '/wiki/search', search: { q: topic } })}
              >
                {topic}
              </button>
            ))}
          </div>
        </Panel>
      ) : (
        <Panel title={`Results for "${query}"`} icon={Search} suffix={`${results.data?.total ?? 0}`}>
          <StatusBlock
            loading={results.isLoading}
            error={results.error?.message ?? ''}
            empty={!results.data?.results?.length}
          >
            <div className="list">
              {(results.data?.results ?? []).map((page) => <PageListItem key={page.id} page={page} />)}
            </div>
          </StatusBlock>
        </Panel>
      )}
    </section>
  )
}
