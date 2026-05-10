import { createFileRoute } from '@tanstack/react-router'
import { Network } from 'lucide-react'
import { PageHeader } from '@/components/PageHeader'
import { Panel } from '@/components/Panel'

export const Route = createFileRoute('/graph/node/$')({
  component: GraphNodePage,
})

function GraphNodePage() {
  const { _splat } = Route.useParams()
  const raw = _splat ?? ''
  const [nodeType = 'node', ...nodeIdParts] = raw.split('/')
  const nodeId = nodeIdParts.join('/') || 'unknown'

  return (
    <section className="stack">
      <PageHeader
        eyebrow="Graph node"
        title={decodeURIComponent(nodeId)}
        copy={`Type: ${decodeURIComponent(nodeType)}`}
      />
      <Panel title="Node detail" icon={Network}>
        <p className="muted">
          Graph node detail pages now live in the web shell. Add a dedicated JSON endpoint when richer node payloads are needed.
        </p>
      </Panel>
    </section>
  )
}
