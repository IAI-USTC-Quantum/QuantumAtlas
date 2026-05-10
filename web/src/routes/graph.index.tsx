import { createFileRoute } from '@tanstack/react-router'
import { CircleDot, Database, GitBranch, Layers3, Network } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { Panel } from '@/components/Panel'
import { useGraphStats } from '@/lib/queries'
import { graphLabelCounts } from '@/lib/api'
import { sumCounts } from '@/lib/utils'

export const Route = createFileRoute('/graph/')({
  component: GraphPage,
})

function GraphPage() {
  const stats = useGraphStats()
  const labelCounts = graphLabelCounts(stats.data)
  const labels = stats.data?.labels ?? Object.keys(labelCounts)
  const nodes = stats.data?.nodes ?? sumCounts(labelCounts)
  const relationships = stats.data?.relationships ?? 0

  return (
    <section className="stack">
      <PageHeader
        eyebrow="Graph"
        title="Knowledge graph overview"
        copy="Neo4j-backed topology and schema health for the quantum knowledge graph."
      />
      <div className="metrics">
        <div className="metric"><Database size={18} /><span>Nodes</span><strong>{nodes}</strong></div>
        <div className="metric"><GitBranch size={18} /><span>Relationships</span><strong>{relationships}</strong></div>
        <div className="metric"><Layers3 size={18} /><span>Labels</span><strong>{labels.length}</strong></div>
      </div>
      {stats.data?.error && <div className="notice danger">{stats.data.error}</div>}
      <Panel title="Node labels" icon={Network}>
        <div className="chips">
          {labels.length ? labels.map((label) => {
            const count = labelCounts[label]
            return <span key={label}>{typeof count === 'number' ? `${label} ${count}` : label}</span>
          }) : <span>No labels reported</span>}
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
