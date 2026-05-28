import { createFileRoute } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Network } from 'lucide-react'

import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'

export const Route = createFileRoute('/$lang/graph/node/$')({
  component: GraphNodePage,
})

function GraphNodePage() {
  const { t } = useTranslation('graph')
  const { _splat } = Route.useParams()
  const raw = _splat ?? ''
  const [nodeType = 'node', ...nodeIdParts] = raw.split('/')
  const nodeId = nodeIdParts.join('/') || 'unknown'

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('nodeEyebrow')}
        title={decodeURIComponent(nodeId)}
        copy={t('nodeType', { type: decodeURIComponent(nodeType) })}
      />
      <Panel title={t('nodeDetail')} icon={Network}>
        <p className="text-sm text-muted-foreground">{t('nodeDetailCopy')}</p>
      </Panel>
    </section>
  )
}
