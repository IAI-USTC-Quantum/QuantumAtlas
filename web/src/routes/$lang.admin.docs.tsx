import { createFileRoute } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { BookOpenText, ExternalLink } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { postJson } from '@/lib/api'
import { useState } from 'react'

export const Route = createFileRoute('/$lang/admin/docs')({
  component: AdminDocsPage,
})

// The dev docs are a sphinx-built static site hosted at /devdoc behind a
// server-side admin gate (ticket → HttpOnly cookie). This page is just
// the entry point: mint a ticket and navigate to the signed URL.
function AdminDocsPage() {
  const { t } = useTranslation('admin')
  const [opening, setOpening] = useState(false)
  const [error, setError] = useState('')

  async function openDocs() {
    setOpening(true)
    setError('')
    try {
      const { url } = await postJson<{ url: string }>(
        '/api/admin/devdoc/ticket',
        {},
      )
      window.location.href = url
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err))
      setOpening(false)
    }
  }

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('docs.title')}
        copy={t('docs.description')}
      />
      <Panel title={t('docs.title')} icon={BookOpenText}>
        <div className="flex flex-wrap items-center gap-3">
          <Button type="button" disabled={opening} onClick={openDocs}>
            <ExternalLink className="size-4" />
            {opening ? t('docs.opening') : t('docs.open')}
          </Button>
          {error && <span className="text-sm text-destructive">{error}</span>}
        </div>
      </Panel>
    </section>
  )
}
