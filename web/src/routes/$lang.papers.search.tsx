import { useMemo, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { useLang } from '@/hooks/use-lang'
import {
  AgenticSearchError,
  isSearchRemoteAvailable,
  type AgenticSearchResponse,
  type MultiSearchResponse,
  type PaperSearchResponse,
  type SearchBackendEntry,
} from '@/lib/api'
import {
  useAgenticSearch,
  useMultiSearch,
  usePaperSearch,
  usePlugins,
  useSearchBackends,
} from '@/lib/queries'

type SearchParams = {
  q?: string
}

export const Route = createFileRoute('/$lang/papers/search')({
  validateSearch: (search: Record<string, unknown>): SearchParams => ({
    q: typeof search.q === 'string' ? search.q : undefined,
  }),
  component: PaperSearchPage,
})

// Persisted backend selection (localStorage; the URL stays clean).
const SELECTION_STORAGE_KEY = 'qatlas.search.sources'
// First-visit default: the previous behaviour minus the internal catalog.
const DEFAULT_SOURCES = ['arxiv', 'openalex', 'semantic_scholar']

function loadStoredSelection(): string[] | null {
  try {
    const raw = localStorage.getItem(SELECTION_STORAGE_KEY)
    if (!raw) return null
    const parsed: unknown = JSON.parse(raw)
    if (Array.isArray(parsed) && parsed.every((x) => typeof x === 'string')) {
      return parsed as string[]
    }
  } catch {
    // corrupt entry — fall through to defaults
  }
  return null
}

function storeSelection(sources: string[]) {
  try {
    localStorage.setItem(SELECTION_STORAGE_KEY, JSON.stringify(sources))
  } catch {
    // private mode etc. — selection just won't persist
  }
}

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
  const remoteAvailable = isSearchRemoteAvailable(plugins.data?.plugins)
  const [agenticWanted, setAgenticWanted] = useState(false)
  const agenticOn = agenticWanted && remoteAvailable

  // Backend catalog + selection. The picker only exists when the
  // qatlas-search microservice is reachable (the catalog proxies it).
  const backendsQuery = useSearchBackends(remoteAvailable)
  const backends = useMemo(
    () => backendsQuery.data?.backends ?? [],
    [backendsQuery.data],
  )
  const byName = useMemo(
    () => new Map(backends.map((b) => [b.name, b])),
    [backends],
  )
  const selectableNames = useMemo(
    () => backends.filter((b) => b.selectable).map((b) => b.name),
    [backends],
  )

  const [selection, setSelection] = useState<string[] | null>(() =>
    loadStoredSelection(),
  )
  // Effective selection = stored selection ∩ currently selectable, with
  // a sane default on first visit / when everything stored got disabled.
  const sources = useMemo(() => {
    const stored = selection?.filter((n) => selectableNames.includes(n)) ?? []
    if (stored.length > 0) return stored
    const defaults = DEFAULT_SOURCES.filter((n) => selectableNames.includes(n))
    if (defaults.length > 0) return defaults
    return selectableNames.slice(0, 3)
  }, [selection, selectableNames])

  function toggleSource(name: string) {
    const next = sources.includes(name)
      ? sources.filter((n) => n !== name)
      : [...sources, name]
    setSelection(next)
    storeSelection(next)
  }

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

  // Three mutually exclusive query paths:
  //   agentic on         -> POST /api/search/agentic (fused + conclusion)
  //   agentic off + remote -> POST /api/search/multi (per-backend tabs)
  //   no remote          -> POST /api/search (legacy fused fallback)
  const multiEntry =
    query && remoteAvailable && !agenticOn && sources.length > 0
      ? { text: query, sources }
      : null
  const multiResults = useMultiSearch(multiEntry)

  const classicEntry = query && !remoteAvailable && !agenticOn ? { text: query } : null
  const classicResults = usePaperSearch(classicEntry)
  const agenticEntry = agenticOn && query ? { text: query, sources } : null
  const agenticResults = useAgenticSearch(agenticEntry)

  // 429: the daily quota is exhausted. The body carries today's usage, so
  // render a dedicated quota message instead of a bare error.
  const err =
    agenticOn
      ? agenticResults.error
      : remoteAvailable
        ? multiResults.error
        : classicResults.error
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
            disabled={!remoteAvailable}
            onClick={() => setAgenticWanted((v) => !v)}
            title={remoteAvailable ? t('agentic.toggleHint') : t('agentic.unavailable')}
          >
            <Wand2 className="size-4" />
            {t('agentic.toggle')}
          </Button>
          {!remoteAvailable && (
            <span className="text-xs text-muted-foreground">
              {t('agentic.unavailable')}
            </span>
          )}
        </div>
      </form>

      {remoteAvailable && (
        <BackendsPanel
          backends={backends}
          loading={backendsQuery.isLoading}
          error={backendsQuery.error?.message ?? ''}
          keysEnabled={backendsQuery.data?.keys_enabled ?? true}
          sources={sources}
          onToggle={toggleSource}
          lang={lang}
        />
      )}

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
      ) : query && agenticOn ? (
        <AgenticResults results={agenticResults.data} isFetching={agenticResults.isFetching} />
      ) : query && remoteAvailable ? (
        backendsQuery.isLoading ? (
          <StatusBlock loading error="" empty={false}>
            <span />
          </StatusBlock>
        ) : sources.length === 0 ? (
          <Panel title={t('backends.title')} icon={Search}>
            <p className="text-sm text-muted-foreground">{t('backends.noneSelected')}</p>
          </Panel>
        ) : (
          <StatusBlock
            loading={multiResults.isLoading}
            error={err?.message ?? ''}
            empty={
              !multiResults.isLoading &&
              !err &&
              sources.every(
                (name) => !(multiResults.data?.results?.[name]?.length ?? 0),
              )
            }
            emptyMessage={t('noResults')}
          >
            {multiResults.data && (
              <MultiResults
                key={`${query}|${sources.join(',')}`}
                sources={sources}
                byName={byName}
                data={multiResults.data}
                isFetching={multiResults.isFetching}
              />
            )}
          </StatusBlock>
        )
      ) : query ? (
        <ClassicResults results={classicResults.data} error={err} />
      ) : null}
    </section>
  )
}

// --- Multi (per-backend tabs) -------------------------------------------------

function MultiResults({
  sources,
  byName,
  data,
  isFetching,
}: {
  sources: string[]
  byName: Map<string, SearchBackendEntry>
  data: MultiSearchResponse
  isFetching: boolean
}) {
  const { t } = useTranslation('papers')
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        {isFetching && <Loader2 className="size-3 animate-spin" />}
        <span>{t('backends.selectedCount', { count: sources.length })}</span>
      </div>
      <Tabs defaultValue={sources[0]}>
        <TabsList className="h-auto flex-wrap justify-start">
          {sources.map((name) => {
            const error = data.errors?.[name]
            const count = data.results?.[name]?.length ?? 0
            return (
              <TabsTrigger key={name} value={name} className="whitespace-nowrap">
                {byName.get(name)?.label ?? name}
                {error ? (
                  <span className="text-destructive" title={error}>
                    ⚠
                  </span>
                ) : (
                  <span className="text-muted-foreground tabular-nums">{count}</span>
                )}
              </TabsTrigger>
            )
          })}
        </TabsList>
        {sources.map((name) => {
          const error = data.errors?.[name]
          const hits = data.results?.[name] ?? []
          return (
            <TabsContent key={name} value={name} className="mt-3">
              {error ? (
                <Alert variant="destructive">
                  <AlertCircle className="size-4" />
                  <AlertTitle>{t('tabs.errorTitle', { backend: byName.get(name)?.label ?? name })}</AlertTitle>
                  <AlertDescription className="break-all">{error}</AlertDescription>
                </Alert>
              ) : hits.length === 0 ? (
                <p className="py-4 text-sm text-muted-foreground">
                  {t('tabs.noResults')}
                </p>
              ) : (
                <div className="space-y-3">
                  {hits.map((hit, idx) => (
                    <PaperHitCard
                      key={`${hit.title}-${idx}`}
                      hit={hit}
                      rank={idx + 1}
                      hideScore
                    />
                  ))}
                </div>
              )}
            </TabsContent>
          )
        })}
      </Tabs>
    </div>
  )
}

// --- Backend picker -------------------------------------------------------------

function BackendsPanel({
  backends,
  loading,
  error,
  keysEnabled,
  sources,
  onToggle,
  lang,
}: {
  backends: SearchBackendEntry[]
  loading: boolean
  error: string
  keysEnabled: boolean
  sources: string[]
  onToggle: (name: string) => void
  lang: string
}) {
  const { t } = useTranslation('papers')
  const academic = backends.filter((b) => b.category === 'academic')
  const web = backends.filter((b) => b.category === 'web')

  return (
    <Panel
      title={t('backends.title')}
      icon={Search}
      suffix={t('backends.selectedCount', { count: sources.length })}
    >
      {loading ? (
        <p className="text-sm text-muted-foreground">{t('backends.loading')}</p>
      ) : error ? (
        <p className="text-sm text-muted-foreground">{t('backends.unavailable')}</p>
      ) : (
        <div className="space-y-4">
          {!keysEnabled && (
            <p className="text-xs text-muted-foreground">{t('backends.keysDisabled')}</p>
          )}
          <BackendGroup
            label={t('backends.academic')}
            backends={academic}
            sources={sources}
            onToggle={onToggle}
            lang={lang}
          />
          {web.length > 0 && (
            <BackendGroup
              label={t('backends.web')}
              backends={web}
              sources={sources}
              onToggle={onToggle}
              lang={lang}
            />
          )}
        </div>
      )}
    </Panel>
  )
}

function BackendGroup({
  label,
  backends,
  sources,
  onToggle,
  lang,
}: {
  label: string
  backends: SearchBackendEntry[]
  sources: string[]
  onToggle: (name: string) => void
  lang: string
}) {
  const { t } = useTranslation('papers')
  return (
    <div>
      <p className="mb-2 text-xs font-medium tracking-wide text-muted-foreground uppercase">
        {label}
      </p>
      <div className="grid gap-x-4 gap-y-2 sm:grid-cols-2 lg:grid-cols-3">
        {backends.map((b) => {
          const checked = sources.includes(b.name)
          const locked = !b.selectable
          return (
            <label
              key={b.name}
              className={`flex items-center gap-2 rounded-md border border-border/60 px-2.5 py-2 text-sm ${
                locked ? 'opacity-60' : 'cursor-pointer hover:border-primary/40'
              }`}
              title={locked ? t('backends.keyRequired') : b.label}
            >
              <input
                type="checkbox"
                className="size-4 shrink-0 accent-primary"
                checked={checked}
                disabled={locked}
                onChange={() => onToggle(b.name)}
              />
              <span className="min-w-0 truncate">{b.label}</span>
              {locked && (
                <Link
                  to="/$lang/dashboard"
                  params={{ lang }}
                  onClick={(e) => e.stopPropagation()}
                  className="ml-auto shrink-0 text-xs text-primary hover:underline"
                >
                  {t('backends.configure')}
                </Link>
              )}
              {!locked && b.requires_key && (
                <Badge variant="secondary" className="ml-auto shrink-0 text-[10px]">
                  {b.key_configured
                    ? t('backends.userKey')
                    : t('backends.serverKey')}
                </Badge>
              )}
            </label>
          )
        })}
      </div>
    </div>
  )
}

// --- Agentic / classic fused renderings (unchanged behaviour) -------------------

function AgenticResults({
  results,
  isFetching,
}: {
  results: AgenticSearchResponse | undefined
  isFetching: boolean
}) {
  const { t } = useTranslation('papers')
  if (!results) return null
  const candidates = results.candidates ?? []
  return (
    <div className="space-y-3">
      <div className="flex items-center gap-2 text-sm text-muted-foreground">
        {isFetching && <Loader2 className="size-3 animate-spin" />}
        <span>{t('resultsHeader', { count: results.results.length })}</span>
        {results.usage && (
          <Badge variant="outline" className="ml-auto tabular-nums">
            {t('agentic.usage', {
              today: results.usage.today,
              limit: results.usage.limit,
            })}
          </Badge>
        )}
      </div>

      {results.conclusion && (
        <Panel title={t('agentic.conclusionTitle')} icon={Wand2}>
          <p className="whitespace-pre-line text-sm leading-6">{results.conclusion}</p>
        </Panel>
      )}

      <div className="space-y-3">
        {results.results.map((result, idx) => (
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
        <Panel title={t('candidatesTitle')} icon={Sparkles} suffix={`${candidates.length}`}>
          <p className="mb-3 text-sm text-muted-foreground">{t('candidatesHint')}</p>
          <div className="space-y-3">
            {candidates.map((hit, idx) => (
              <PaperHitCard key={`${hit.title}-${idx}`} hit={hit} rank={idx + 1} />
            ))}
          </div>
        </Panel>
      )}
    </div>
  )
}

function ClassicResults({
  results,
  error,
}: {
  results: PaperSearchResponse | undefined
  error: Error | null
}) {
  const { t } = useTranslation('papers')
  const candidates = results?.candidates ?? []
  return (
    <StatusBlock
      loading={!results && !error}
      error={error?.message ?? ''}
      empty={
        results !== undefined && !results.results.length && !candidates.length
      }
      emptyMessage={t('noResults')}
    >
      {results && (
        <>
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            <span>{t('resultsHeader', { count: results.results.length })}</span>
          </div>
          <div className="space-y-3">
            {results.results.map((result, idx) => (
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
            <Panel title={t('candidatesTitle')} icon={Sparkles} suffix={`${candidates.length}`}>
              <p className="mb-3 text-sm text-muted-foreground">{t('candidatesHint')}</p>
              <div className="space-y-3">
                {candidates.map((hit, idx) => (
                  <PaperHitCard key={`${hit.title}-${idx}`} hit={hit} rank={idx + 1} />
                ))}
              </div>
            </Panel>
          )}
        </>
      )}
    </StatusBlock>
  )
}
