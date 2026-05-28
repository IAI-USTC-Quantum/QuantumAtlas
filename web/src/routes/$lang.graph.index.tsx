import { createFileRoute } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { CircleDot, Network } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { MetricGrid } from '@/components/metric-grid'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { graphLabelCounts } from '@/lib/api'
import { useGraphStats } from '@/lib/queries'
import { sumCounts } from '@/lib/utils'

export const Route = createFileRoute('/$lang/graph/')({
  component: GraphPage,
})

function GraphPage() {
  const { t } = useTranslation('graph')
  const stats = useGraphStats()
  const labelCounts = graphLabelCounts(stats.data)
  const labels = stats.data?.labels ?? Object.keys(labelCounts)

  return (
    <section className="space-y-6">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />
      <MetricGrid
        nodes={stats.data?.nodes ?? sumCounts(labelCounts)}
        relationships={stats.data?.relationships ?? 0}
        labels={labels.length}
        loading={stats.isLoading}
      />
      {stats.data?.error && (
        <Alert variant="destructive">
          <AlertTitle>Neo4j</AlertTitle>
          <AlertDescription>{stats.data.error}</AlertDescription>
        </Alert>
      )}
      <Panel title={t('labelsPanel')} icon={Network} suffix={`${labels.length}`}>
        <div className="flex flex-wrap gap-2">
          {labels.length ? (
            labels.map((label) => {
              const count = labelCounts[label]
              return (
                <Badge key={label} variant="outline" className="font-normal">
                  {label}
                  {typeof count === 'number' && (
                    <span className="ml-1 text-muted-foreground">{count}</span>
                  )}
                </Badge>
              )
            })
          ) : (
            <span className="text-sm text-muted-foreground">
              {t('labelsEmpty')}
            </span>
          )}
        </div>
      </Panel>
      <Panel title={t('explorerPanel')} icon={CircleDot}>
        <div className="flex flex-wrap items-center justify-center gap-4 rounded-lg border border-dashed border-border bg-muted/40 p-8 text-sm">
          <span className="rounded-full bg-primary/15 px-4 py-2 font-medium text-primary">
            Algorithm
          </span>
          <span className="h-px w-8 bg-border" />
          <span className="rounded-full bg-secondary px-3 py-1.5 text-secondary-foreground">
            Primitive
          </span>
          <span className="h-px w-8 bg-border" />
          <span className="rounded-full bg-muted px-2.5 py-1 text-xs text-muted-foreground">
            Paper
          </span>
        </div>
        <p className="mt-3 text-xs text-muted-foreground">{t('explorerHint')}</p>
      </Panel>
    </section>
  )
}
