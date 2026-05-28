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
import { usePages, useStats } from '@/lib/queries'
import { titleCase } from '@/lib/utils'

export const Route = createFileRoute('/$lang/wiki/')({
  component: WikiPage,
})

function WikiPage() {
  const { t } = useTranslation('wiki')
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
    <section className="space-y-6">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />
      <MetricGrid stats={stats.data} loading={stats.isLoading} />
      <StatusBlock
        loading={pages.isLoading}
        error={pages.error?.message ?? ''}
        empty={!grouped.length}
      >
        <div className="space-y-4">
          {grouped.map(([type, items]) => (
            <Panel
              key={type}
              title={`${titleCase(type)}s`}
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
