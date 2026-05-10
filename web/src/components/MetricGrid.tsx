import { Boxes, CircleDot, FileText, GitBranch } from 'lucide-react'
import { statValue, type Stats } from '@/lib/api'

export function MetricGrid({ stats, loading }: { stats: Stats | null | undefined; loading: boolean }) {
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
