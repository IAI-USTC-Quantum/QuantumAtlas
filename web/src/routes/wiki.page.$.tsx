import { createFileRoute } from '@tanstack/react-router'
import { useMemo } from 'react'
import DOMPurify from 'dompurify'
import { marked } from 'marked'
import { Badge } from '@/components/Badge'
import { PageHeader } from '@/components/PageHeader'
import { StatusBlock } from '@/components/StatusBlock'
import { usePage } from '@/lib/queries'

export const Route = createFileRoute('/wiki/page/$')({
  component: WikiDetailPage,
})

function WikiDetailPage() {
  const { _splat } = Route.useParams()
  const pageId = decodeURIComponent(_splat ?? '')
  const page = usePage(pageId || null)
  const html = useMemo(() => {
    const raw = marked.parse(page.data?.content ?? '') as string
    return DOMPurify.sanitize(raw)
  }, [page.data?.content])

  return (
    <section className="stack">
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
            <div className="detail-meta">
              <Badge>{page.data.status ?? 'draft'}</Badge>
              {page.data.category && <Badge>{page.data.category}</Badge>}
              {(page.data.tags ?? []).slice(0, 8).map((tag) => <Badge key={tag}>{tag}</Badge>)}
            </div>
            <article className="markdown" dangerouslySetInnerHTML={{ __html: html }} />
          </>
        )}
      </StatusBlock>
    </section>
  )
}
