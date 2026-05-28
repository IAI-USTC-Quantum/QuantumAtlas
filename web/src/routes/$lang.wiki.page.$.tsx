import { createFileRoute } from '@tanstack/react-router'
import { useMemo } from 'react'
import { useTranslation } from 'react-i18next'
import DOMPurify from 'dompurify'
import { marked } from 'marked'

import { Badge } from '@/components/ui/badge'
import { PageHeader } from '@/components/page-header'
import { StatusBlock } from '@/components/status-block'
import { usePage } from '@/lib/queries'

export const Route = createFileRoute('/$lang/wiki/page/$')({
  component: WikiDetailPage,
})

function WikiDetailPage() {
  const { t } = useTranslation('wiki')
  const { _splat } = Route.useParams()
  const pageId = decodeURIComponent(_splat ?? '')
  const page = usePage(pageId || null)
  const html = useMemo(() => {
    const raw = marked.parse(page.data?.content ?? '') as string
    return DOMPurify.sanitize(raw)
  }, [page.data?.content])

  return (
    <section className="space-y-5">
      <StatusBlock
        loading={page.isLoading}
        error={page.error?.message ?? ''}
        empty={!page.data}
      >
        {page.data && (
          <>
            <PageHeader
              eyebrow={page.data.type}
              title={page.data.title}
              copy={page.data.id}
            />
            <div className="flex flex-wrap gap-2">
              <Badge variant="secondary">
                {page.data.status ?? t('detailMeta.draft')}
              </Badge>
              {page.data.category && (
                <Badge variant="outline">{page.data.category}</Badge>
              )}
              {(page.data.tags ?? []).slice(0, 8).map((tag) => (
                <Badge key={tag} variant="outline">
                  {tag}
                </Badge>
              ))}
            </div>
            <article
              className="markdown rounded-xl border border-border bg-card p-6"
              dangerouslySetInnerHTML={{ __html: html }}
            />
          </>
        )}
      </StatusBlock>
    </section>
  )
}
