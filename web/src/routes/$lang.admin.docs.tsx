import { useMemo } from 'react'
import { createFileRoute } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'

import { PageHeader } from '@/components/page-header'
import { renderMarkdown } from '@/lib/markdown'
import md from '@/docs/architecture.md?raw'

export const Route = createFileRoute('/$lang/admin/docs')({
  component: AdminDocsPage,
})

function AdminDocsPage() {
  const { t } = useTranslation('admin')
  const { lang } = Route.useParams()
  const html = useMemo(() => renderMarkdown(md, lang), [lang])

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('docs.title')}
        copy={t('docs.description')}
      />
      <article
        className="markdown rounded-xl border border-border bg-card p-6"
        // renderMarkdown output is DOMPurify-sanitized.
        dangerouslySetInnerHTML={{ __html: html }}
      />
    </section>
  )
}
