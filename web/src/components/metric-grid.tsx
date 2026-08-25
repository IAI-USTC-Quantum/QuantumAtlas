import { useTranslation } from 'react-i18next'
import {
  CheckCircle2,
  CircleX,
  Database,
  Loader2,
  type LucideIcon,
} from 'lucide-react'

import { Card, CardContent } from '@/components/ui/card'
import { Skeleton } from '@/components/ui/skeleton'
import type { PaperStats } from '@/lib/api'

export type Metric = {
  key: string
  label: string
  value: number
  icon: LucideIcon
}

/**
 * MetricGrid renders a 2–4 column row of stat tiles. Pass `items` for
 * fully custom rows, or pass `paperStats=` for the registry lifecycle
 * counters (GET /api/papers/stats) and it will build the items itself.
 */
export function MetricGrid(
  props:
    | {
        items: Metric[]
        loading?: boolean
      }
    | {
        paperStats: PaperStats | null | undefined
        loading: boolean
      },
) {
  const { t } = useTranslation('common')

  let items: Metric[]
  if ('items' in props) {
    items = props.items
  } else {
    items = [
      {
        key: 'total',
        label: t('metrics.papersTotal'),
        value: props.paperStats?.total ?? 0,
        icon: Database,
      },
      {
        key: 'pending',
        label: t('metrics.papersPending'),
        value: props.paperStats?.pending ?? 0,
        icon: Loader2,
      },
      {
        key: 'ready',
        label: t('metrics.papersReady'),
        value: props.paperStats?.ready ?? 0,
        icon: CheckCircle2,
      },
      {
        key: 'failed',
        label: t('metrics.papersFailed'),
        value: props.paperStats?.failed ?? 0,
        icon: CircleX,
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
