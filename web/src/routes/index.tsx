import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { Activity, BookOpen, FileText, Network, RefreshCw, Search, ShieldCheck } from 'lucide-react'
import { MetricGrid } from '@/components/MetricGrid'
import { PageListItem } from '@/components/PageListItem'
import { Panel } from '@/components/Panel'
import { StatusBlock } from '@/components/StatusBlock'
import { usePages, useStats } from '@/lib/queries'

export const Route = createFileRoute('/')({
  component: HomePage,
})

function HomePage() {
  const navigate = useNavigate()
  const stats = useStats()
  const pages = usePages()
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
          <button className="primary" onClick={() => navigate({ to: '/wiki' })}>
            <BookOpen size={18} /> Browse wiki
          </button>
          <button className="secondary" onClick={() => navigate({ to: '/graph' })}>
            <Network size={18} /> Explore graph
          </button>
        </div>
      </div>

      <MetricGrid stats={stats.data} loading={stats.isLoading} />

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
                navigate({ to: '/wiki' })
              }}
            >
              <input name="arxiv_id" placeholder="arxiv-id" />
              <button type="submit"><RefreshCw size={16} /> Ingest</button>
            </form>
            <button className="wide" onClick={() => navigate({ to: '/wiki/search' })}>
              <Search size={16} /> Search knowledge
            </button>
            <a className="button-like wide" href="/api/lint">
              <ShieldCheck size={16} /> Run lint check
            </a>
          </div>
        </Panel>

        <Panel title="Recent pages" icon={FileText}>
          <StatusBlock loading={pages.isLoading} error={pages.error?.message ?? ''} empty={!recent.length}>
            <div className="list">
              {recent.map((page) => <PageListItem key={page.id} page={page} />)}
            </div>
          </StatusBlock>
        </Panel>
      </div>
    </section>
  )
}
