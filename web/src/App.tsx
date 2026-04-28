import { useEffect, useMemo, useState } from 'react'
import DOMPurify from 'dompurify'
import { marked } from 'marked'
import {
  Activity,
  BookOpen,
  Boxes,
  ChevronRight,
  CircleDot,
  Clipboard,
  Code2,
  Database,
  FileText,
  GitBranch,
  Home,
  KeyRound,
  Layers3,
  Network,
  RefreshCw,
  Search,
  ShieldCheck,
  Sparkles,
} from 'lucide-react'
import './App.css'

type Stats = {
  total_pages?: number
  by_type?: Record<string, number>
  by_status?: Record<string, number>
  by_category?: Record<string, number>
}

type PageSummary = {
  id: string
  title: string
  type: string
  category?: string | null
  status?: string
  tags?: string[]
}

type PageDetail = PageSummary & {
  content?: string | null
  created_at?: string | null
  updated_at?: string | null
}

type SearchPayload = {
  query: string
  total: number
  results: PageSummary[]
}

type GraphStats = {
  nodes?: number
  relationships?: number
  labels?: string[]
  error?: string
}

function readToken() {
  return document
    .querySelector<HTMLMetaElement>('meta[name="qatlas-token"]')
    ?.content.trim() ?? ''
}

function shortToken(token: string) {
  if (token.length <= 28) return token || 'No token found'
  return `${token.slice(0, 18)}...${token.slice(-12)}`
}

function maskToken(token: string) {
  if (!token) return ''
  return `${token.slice(0, 12)}${'*'.repeat(24)}${token.slice(-10)}`
}

function currentPath() {
  return `${window.location.pathname}${window.location.search}`
}

async function getJson<T>(url: string): Promise<T> {
  const response = await fetch(url)
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

function useJson<T>(url: string | null) {
  const [data, setData] = useState<T | null>(null)
  const [error, setError] = useState<string>('')
  const [loading, setLoading] = useState(Boolean(url))

  useEffect(() => {
    if (!url) {
      setData(null)
      setError('')
      setLoading(false)
      return
    }

    let cancelled = false
    setLoading(true)
    setError('')
    getJson<T>(url)
      .then((value) => {
        if (!cancelled) setData(value)
      })
      .catch((err: unknown) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err))
      })
      .finally(() => {
        if (!cancelled) setLoading(false)
      })

    return () => {
      cancelled = true
    }
  }, [url])

  return { data, error, loading }
}

function statValue(stats: Stats | null, group: keyof Stats, key: string) {
  const value = stats?.[group]
  if (!value || typeof value !== 'object') return 0
  return (value as Record<string, number>)[key] ?? 0
}

function navigate(path: string) {
  window.history.pushState({}, '', path)
  window.dispatchEvent(new PopStateEvent('popstate'))
}

function App() {
  const [path, setPath] = useState(currentPath())

  useEffect(() => {
    const update = () => setPath(currentPath())
    window.addEventListener('popstate', update)
    return () => window.removeEventListener('popstate', update)
  }, [])

  return (
    <div className="app-shell">
      <Sidebar path={path} />
      <main className="app-main">
        <Topbar />
        <div className="page-frame">
          <RouteView path={path} />
        </div>
      </main>
    </div>
  )
}

function Sidebar({ path }: { path: string }) {
  const links = [
    { href: '/', label: 'Home', icon: Home },
    { href: '/wiki', label: 'Wiki', icon: BookOpen },
    { href: '/wiki/search', label: 'Search', icon: Search },
    { href: '/graph', label: 'Graph', icon: Network },
    { href: '/token', label: 'Token', icon: KeyRound },
  ]

  return (
    <aside className="sidebar">
      <a className="brand" href="/" onClick={(event) => { event.preventDefault(); navigate('/') }}>
        <span className="brand-mark"><Sparkles size={20} /></span>
        <span>
          <strong>QuantumAtlas</strong>
          <small>Knowledge workbench</small>
        </span>
      </a>
      <nav>
        {links.map((link) => {
          const Icon = link.icon
          const active = link.href === '/' ? path === '/' : path.startsWith(link.href)
          return (
            <a
              key={link.href}
              className={active ? 'active' : ''}
              href={link.href}
              onClick={(event) => { event.preventDefault(); navigate(link.href) }}
            >
              <Icon size={18} />
              {link.label}
            </a>
          )
        })}
      </nav>
    </aside>
  )
}

function Topbar() {
  return (
    <header className="topbar">
      <form
        className="global-search"
        onSubmit={(event) => {
          event.preventDefault()
          const form = new FormData(event.currentTarget)
          const q = String(form.get('q') ?? '').trim()
          navigate(`/wiki/search${q ? `?q=${encodeURIComponent(q)}` : ''}`)
        }}
      >
        <Search size={17} />
        <input name="q" placeholder="Search pages, primitives, algorithms" />
      </form>
      <a className="docs-link" href="/api/docs">
        API docs
        <ChevronRight size={16} />
      </a>
    </header>
  )
}

function RouteView({ path }: { path: string }) {
  const pathname = path.split('?', 1)[0]
  if (pathname === '/') return <HomePage />
  if (pathname === '/wiki' || pathname === '/wiki/') return <WikiPage />
  if (pathname === '/wiki/search') return <SearchPage path={path} />
  if (pathname.startsWith('/wiki/page/')) {
    return <WikiDetailPage pageId={decodeURIComponent(pathname.replace('/wiki/page/', ''))} />
  }
  if (pathname === '/graph' || pathname === '/graph/') return <GraphPage />
  if (pathname.startsWith('/graph/node/')) return <GraphNodePage path={pathname} />
  if (pathname === '/token') return <TokenPage />
  return <NotFoundPage />
}

function HomePage() {
  const stats = useJson<Stats>('/api/stats')
  const pages = useJson<{ total: number; pages: PageSummary[] }>('/api/pages')
  const recent = (pages.data?.pages ?? []).slice(0, 5)

  return (
    <section className="stack">
      <div className="hero-band">
        <div>
          <p className="eyebrow">Quantum algorithm workspace</p>
          <h1>QuantumAtlas</h1>
          <p className="hero-copy">
            Browse extracted papers, inspect primitives, and hand API-ready tokens to trusted terminals.
          </p>
        </div>
        <div className="hero-actions">
          <button className="primary" onClick={() => navigate('/wiki')}>
            <BookOpen size={18} /> Browse wiki
          </button>
          <button className="secondary" onClick={() => navigate('/graph')}>
            <Network size={18} /> Explore graph
          </button>
        </div>
      </div>

      <MetricGrid stats={stats.data} loading={stats.loading} />

      <div className="two-column">
        <Panel title="Quick actions" icon={Activity}>
          <div className="action-list">
            <form
              className="inline-form"
              onSubmit={async (event) => {
                event.preventDefault()
                const form = new FormData(event.currentTarget)
                const arxivId = String(form.get('arxiv_id') ?? '').trim()
                if (!arxivId) return
                await fetch('/api/ingest/paper', {
                  method: 'POST',
                  headers: { 'Content-Type': 'application/json' },
                  body: JSON.stringify({ arxiv_id: arxivId }),
                })
                navigate('/wiki')
              }}
            >
              <input name="arxiv_id" placeholder="arxiv-id" />
              <button type="submit"><RefreshCw size={16} /> Ingest</button>
            </form>
            <button className="wide" onClick={() => navigate('/wiki/search')}>
              <Search size={16} /> Search knowledge
            </button>
            <a className="button-like wide" href="/api/lint">
              <ShieldCheck size={16} /> Run lint check
            </a>
          </div>
        </Panel>

        <Panel title="Recent pages" icon={FileText}>
          <StatusBlock loading={pages.loading} error={pages.error} empty={!recent.length}>
            <div className="list">
              {recent.map((page) => <PageListItem key={page.id} page={page} />)}
            </div>
          </StatusBlock>
        </Panel>
      </div>
    </section>
  )
}

function MetricGrid({ stats, loading }: { stats: Stats | null; loading: boolean }) {
  const metrics = [
    { label: 'Total pages', value: stats?.total_pages ?? 0, icon: FileText },
    { label: 'Primitives', value: statValue(stats, 'by_category', 'primitive'), icon: Boxes },
    { label: 'Algorithms', value: statValue(stats, 'by_category', 'algorithm'), icon: GitBranch },
    { label: 'Published', value: statValue(stats, 'by_status', 'published'), icon: CircleDot },
  ]

  return (
    <div className="metrics">
      {metrics.map((metric) => {
        const Icon = metric.icon
        return (
          <div className="metric" key={metric.label}>
            <Icon size={18} />
            <span>{metric.label}</span>
            <strong>{loading ? '...' : metric.value}</strong>
          </div>
        )
      })}
    </div>
  )
}

function WikiPage() {
  const stats = useJson<Stats>('/api/stats')
  const pages = useJson<{ total: number; pages: PageSummary[] }>('/api/pages')
  const grouped = useMemo(() => {
    const map = new Map<string, PageSummary[]>()
    for (const page of pages.data?.pages ?? []) {
      const key = page.type || 'page'
      map.set(key, [...(map.get(key) ?? []), page])
    }
    return [...map.entries()]
  }, [pages.data])

  return (
    <section className="stack">
      <PageHeader
        eyebrow="Wiki"
        title="Structured knowledge base"
        copy="Pages are read from the Wiki Git checkout and rendered by the web app."
      />
      <MetricGrid stats={stats.data} loading={stats.loading} />
      <StatusBlock loading={pages.loading} error={pages.error} empty={!grouped.length}>
        {grouped.map(([type, items]) => (
          <Panel key={type} title={`${titleCase(type)}s`} icon={Layers3} suffix={`${items.length}`}>
            <div className="page-grid">
              {items.map((page) => <PageCard key={page.id} page={page} />)}
            </div>
          </Panel>
        ))}
      </StatusBlock>
    </section>
  )
}

function SearchPage({ path }: { path: string }) {
  const params = new URLSearchParams(path.split('?', 2)[1] ?? '')
  const query = params.get('q') ?? ''
  const results = useJson<SearchPayload>(query ? `/api/search?q=${encodeURIComponent(query)}&limit=20` : null)

  return (
    <section className="stack">
      <PageHeader eyebrow="Search" title="Find wiki pages" copy="Search titles, tags, page ids, and extracted content." />
      <form
        className="search-panel"
        onSubmit={(event) => {
          event.preventDefault()
          const form = new FormData(event.currentTarget)
          const q = String(form.get('q') ?? '').trim()
          navigate(`/wiki/search${q ? `?q=${encodeURIComponent(q)}` : ''}`)
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
              <button key={topic} onClick={() => navigate(`/wiki/search?q=${encodeURIComponent(topic)}`)}>{topic}</button>
            ))}
          </div>
        </Panel>
      ) : (
        <Panel title={`Results for "${query}"`} icon={Search} suffix={`${results.data?.total ?? 0}`}>
          <StatusBlock loading={results.loading} error={results.error} empty={!results.data?.results?.length}>
            <div className="list">
              {(results.data?.results ?? []).map((page) => <PageListItem key={page.id} page={page} />)}
            </div>
          </StatusBlock>
        </Panel>
      )}
    </section>
  )
}

function WikiDetailPage({ pageId }: { pageId: string }) {
  const page = useJson<PageDetail>(`/api/pages/${encodeURIComponent(pageId)}`)
  const html = useMemo(() => {
    const raw = marked.parse(page.data?.content ?? '') as string
    return DOMPurify.sanitize(raw)
  }, [page.data?.content])

  return (
    <section className="stack">
      <StatusBlock loading={page.loading} error={page.error} empty={!page.data}>
        {page.data && (
          <>
            <PageHeader
              eyebrow={page.data.type}
              title={page.data.title}
              copy={page.data.id}
            />
            <div className="detail-meta">
              <Badge>{page.data.status ?? 'draft'}</Badge>
              {page.data.category && <Badge>{page.data.category}</Badge>}
              {(page.data.tags ?? []).slice(0, 8).map((tag) => <Badge key={tag}>{tag}</Badge>)}
            </div>
            <article className="markdown" dangerouslySetInnerHTML={{ __html: html }} />
          </>
        )}
      </StatusBlock>
    </section>
  )
}

function GraphPage() {
  const stats = useJson<GraphStats>('/api/graph/stats')
  const labels = stats.data?.labels ?? []

  return (
    <section className="stack">
      <PageHeader
        eyebrow="Graph"
        title="Knowledge graph overview"
        copy="Neo4j-backed topology and schema health for the quantum knowledge graph."
      />
      <div className="metrics">
        <div className="metric"><Database size={18} /><span>Nodes</span><strong>{stats.data?.nodes ?? 0}</strong></div>
        <div className="metric"><GitBranch size={18} /><span>Relationships</span><strong>{stats.data?.relationships ?? 0}</strong></div>
        <div className="metric"><Layers3 size={18} /><span>Labels</span><strong>{labels.length}</strong></div>
      </div>
      {stats.data?.error && <div className="notice danger">{stats.data.error}</div>}
      <Panel title="Node labels" icon={Network}>
        <div className="chips">
          {labels.length ? labels.map((label) => <span key={label}>{label}</span>) : <span>No labels reported</span>}
        </div>
      </Panel>
      <Panel title="Explorer" icon={CircleDot}>
        <div className="graph-surface">
          <div className="node large">Algorithm</div>
          <div className="edge" />
          <div className="node">Primitive</div>
          <div className="edge second" />
          <div className="node small">Paper</div>
        </div>
      </Panel>
    </section>
  )
}

function GraphNodePage({ path }: { path: string }) {
  const [, nodeType = 'node', nodeId = 'unknown'] = path.replace('/graph/node/', '').split('/')
  return (
    <section className="stack">
      <PageHeader eyebrow="Graph node" title={decodeURIComponent(nodeId)} copy={`Type: ${decodeURIComponent(nodeType)}`} />
      <Panel title="Node detail" icon={Network}>
        <p className="muted">Graph node detail pages now live in the web shell. Add a dedicated JSON endpoint when richer node payloads are needed.</p>
      </Panel>
    </section>
  )
}

function TokenPage() {
  const token = useMemo(readToken, [])
  const [copied, setCopied] = useState<string>('')
  const [revealed, setRevealed] = useState(false)
  const origin = window.location.origin
  const curlCommand = `curl -k -H 'Authorization: Bearer ${token}' ${origin}/api/server/info`

  async function copy(text: string, label: string) {
    await navigator.clipboard.writeText(text)
    setCopied(`${label} copied`)
    window.clearTimeout(window.__tokenHintTimer)
    window.__tokenHintTimer = window.setTimeout(() => setCopied(''), 2200)
  }

  return (
    <section className="token-layout">
      <div className="token-intro">
        <KeyRound size={34} />
        <p className="eyebrow">Caddy-authenticated access</p>
        <h1>QuantumAtlas Token</h1>
        <p>Copy your current Caddy-issued bearer token for API calls from trusted terminals.</p>
        <dl>
          <div><dt>Scope</dt><dd>QuantumAtlas API</dd></div>
          <div><dt>Lifetime</dt><dd>7 days</dd></div>
          <div><dt>Source</dt><dd>Caddy auth session</dd></div>
        </dl>
      </div>
      <div className="token-workspace">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">Access token</p>
            <h2>{token ? 'Ready for CLI use' : 'Sign in required'}</h2>
          </div>
          <span className={token ? 'status good' : 'status'}>{token ? 'Active' : 'Missing'}</span>
        </div>
        <div className="token-box">{shortToken(token)}</div>
        <div className="field-row">
          <label htmlFor="token-value">Token value</label>
          <button className="ghost small" type="button" disabled={!token} onClick={() => setRevealed((value) => !value)}>
            {revealed ? 'Hide' : 'Reveal'}
          </button>
        </div>
        <textarea id="token-value" readOnly spellCheck={false} value={revealed ? token : maskToken(token)} />
        <div className="actions">
          <button className="primary" type="button" disabled={!token} onClick={() => copy(token, 'Token')}>
            <Clipboard size={17} /> Copy token
          </button>
          <button className="secondary" type="button" disabled={!token} onClick={() => copy(curlCommand, 'Command')}>
            <Code2 size={17} /> Copy curl
          </button>
          <span className="copy-state" aria-live="polite">{copied}</span>
        </div>
        <pre className="command-block"><code>{curlCommand}</code></pre>
        <p className="muted">Treat this value like a password. It is signed by Caddy and expires with the auth policy.</p>
      </div>
    </section>
  )
}

function NotFoundPage() {
  return <PageHeader eyebrow="404" title="Page not found" copy="This route is not part of the web workspace." />
}

function PageHeader({ eyebrow, title, copy }: { eyebrow: string; title: string; copy: string }) {
  return (
    <header className="page-header">
      <p className="eyebrow">{eyebrow}</p>
      <h1>{title}</h1>
      <p>{copy}</p>
    </header>
  )
}

function Panel({ title, icon: Icon, suffix, children }: {
  title: string
  icon: typeof Activity
  suffix?: string
  children: React.ReactNode
}) {
  return (
    <section className="panel">
      <div className="panel-heading">
        <h2><Icon size={18} /> {title}</h2>
        {suffix && <span className="panel-suffix">{suffix}</span>}
      </div>
      {children}
    </section>
  )
}

function StatusBlock({ loading, error, empty, children }: {
  loading: boolean
  error: string
  empty: boolean
  children: React.ReactNode
}) {
  if (loading) return <div className="notice">Loading...</div>
  if (error) return <div className="notice danger">{error}</div>
  if (empty) return <div className="notice">No records yet.</div>
  return <>{children}</>
}

function PageCard({ page }: { page: PageSummary }) {
  return (
    <a className="page-card" href={`/wiki/page/${page.id}`} onClick={(event) => { event.preventDefault(); navigate(`/wiki/page/${page.id}`) }}>
      <strong>{page.title}</strong>
      <span>{page.id}</span>
      <div className="tags">
        {page.category && <Badge>{page.category}</Badge>}
        {(page.tags ?? []).slice(0, 3).map((tag) => <Badge key={tag}>{tag}</Badge>)}
      </div>
    </a>
  )
}

function PageListItem({ page }: { page: PageSummary }) {
  return (
    <a className="list-item" href={`/wiki/page/${page.id}`} onClick={(event) => { event.preventDefault(); navigate(`/wiki/page/${page.id}`) }}>
      <span>
        <strong>{page.title}</strong>
        <small>{page.id}</small>
      </span>
      <Badge>{page.type}</Badge>
    </a>
  )
}

function Badge({ children }: { children: React.ReactNode }) {
  return <span className="badge">{children}</span>
}

function titleCase(value: string) {
  return value.charAt(0).toUpperCase() + value.slice(1)
}

declare global {
  interface Window {
    __tokenHintTimer?: number
  }
}

export default App
