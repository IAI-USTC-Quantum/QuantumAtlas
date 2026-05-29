import { createFileRoute } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import { Layers3 } from 'lucide-react'

import { MetricGrid } from '@/components/metric-grid'
import { PageCard } from '@/components/page-card'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import type { PageSummary } from '@/lib/api'
import { usePages, usePaperStats, useStats } from '@/lib/queries'
import { titleCase } from '@/lib/utils'

export const Route = createFileRoute('/$lang/wiki/')({
  component: WikiPage,
})

function WikiPage() {
  const { t } = useTranslation('wiki')
  const stats = useStats()
  const paperStats = usePaperStats()
  const pages = usePages()
  // Wikipedia-style: every page is a concept (词条). Group by category
  // (algorithm / primitive / zoo-section / comparison / …) so the browse
  // surfaces meaningful sections instead of one flat list. Sources are
  // already excluded server-side.
  const grouped = useMemo(() => {
    const map = new Map<string, PageSummary[]>()
    for (const page of pages.data?.pages ?? []) {
      const key = page.category || 'other'
      map.set(key, [...(map.get(key) ?? []), page])
    }
    return [...map.entries()].sort((a, b) => b[1].length - a[1].length)
  }, [pages.data])

  return (
    <section className="space-y-6">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />
      <MetricGrid stats={stats.data} loading={stats.isLoading} />
      {paperStats.data?.available && (
        <MetricGrid
          paperStats={paperStats.data}
          loading={paperStats.isLoading}
        />
      )}
      <StatusBlock
        loading={pages.isLoading}
        error={pages.error?.message ?? ''}
        empty={!grouped.length}
      >
        <div className="space-y-4">
          {grouped.map(([category, items]) => (
            <Panel
              key={category}
              title={titleCase(category)}
              icon={Layers3}
              suffix={`${items.length}`}
            >
              <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
                {items.map((page) => (
                  <PageCard key={page.id} page={page} />
                ))}
              </div>
            </Panel>
          ))}
        </div>
      </StatusBlock>
    </section>
  )
}
