import { createFileRoute } from '@tanstack/react-router'
import { useMemo } from 'react'
import { Layers3 } from 'lucide-react'
import { MetricGrid } from '@/components/MetricGrid'
import { PageCard } from '@/components/PageCard'
import { PageHeader } from '@/components/PageHeader'
import { Panel } from '@/components/Panel'
import { StatusBlock } from '@/components/StatusBlock'
import type { PageSummary } from '@/lib/api'
import { usePages, useStats } from '@/lib/queries'
import { titleCase } from '@/lib/utils'

export const Route = createFileRoute('/wiki/')({
  component: WikiPage,
})

function WikiPage() {
  const stats = useStats()
  const pages = usePages()
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
      <MetricGrid stats={stats.data} loading={stats.isLoading} />
      <StatusBlock loading={pages.isLoading} error={pages.error?.message ?? ''} empty={!grouped.length}>
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
