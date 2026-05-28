import { useTranslation } from 'react-i18next'
import {
  Boxes,
  CircleDot,
  Database,
  FileText,
  GitBranch,
  Layers3,
  type LucideIcon,
} from 'lucide-react'

import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import { statValue, type Stats } from '@/lib/api'

export type Metric = {
  key: string
  label: string
  value: number
  icon: LucideIcon
}

/**
 * MetricGrid renders a 2–4 column row of stat tiles. Pass `items` for
 * fully custom rows, or pass one of the convenience prop sets
 * (`stats=` for the wiki stats payload, `nodes/relationships/labels`
 * for the graph stats payload) and it will build the items itself.
 */
export function MetricGrid(
  props:
    | {
        items: Metric[]
        loading?: boolean
      }
    | {
        stats: Stats | null | undefined
        loading: boolean
      }
    | {
        nodes: number
        relationships: number
        labels: number
        loading: boolean
      },
) {
  const { t } = useTranslation('common')

  let items: Metric[]
  if ('items' in props) {
    items = props.items
  } else if ('stats' in props) {
    items = [
      {
        key: 'total_pages',
        label: t('metrics.totalPages'),
        value: props.stats?.total_pages ?? 0,
        icon: FileText,
      },
      {
        key: 'primitives',
        label: t('metrics.primitives'),
        value: statValue(props.stats, 'by_category', 'primitive'),
        icon: Boxes,
      },
      {
        key: 'algorithms',
        label: t('metrics.algorithms'),
        value: statValue(props.stats, 'by_category', 'algorithm'),
        icon: GitBranch,
      },
      {
        key: 'published',
        label: t('metrics.published'),
        value: statValue(props.stats, 'by_status', 'published'),
        icon: CircleDot,
      },
    ]
  } else {
    items = [
      {
        key: 'nodes',
        label: t('metrics.nodes'),
        value: props.nodes,
        icon: Database,
      },
      {
        key: 'relationships',
        label: t('metrics.relationships'),
        value: props.relationships,
        icon: GitBranch,
      },
      {
        key: 'labels',
        label: t('metrics.labels'),
        value: props.labels,
        icon: Layers3,
      },
    ]
  }
  const loading = props.loading ?? false

  return (
    <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
      {items.map((metric) => {
        const Icon = metric.icon
        return (
          <Card key={metric.key} className="gap-2 py-4">
            <CardContent className="flex flex-col gap-1 px-4">
              <div className="flex items-center gap-2 text-xs font-medium text-muted-foreground">
                <Icon className="size-4 text-primary" /> {metric.label}
              </div>
              {loading ? (
                <Skeleton className="h-7 w-16" />
              ) : (
                <strong className="text-2xl font-semibold tabular-nums text-foreground">
                  {metric.value.toLocaleString()}
                </strong>
              )}
            </CardContent>
          </Card>
        )
      })}
    </div>
  )
}

