import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  Check,
  ChevronLeft,
  ChevronRight,
  Filter,
  Loader2,
  Search,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { PageHeader } from '@/components/page-header'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { usePapersList } from '@/lib/queries'
import type { PapersListParams } from '@/lib/api'

type ListSearchParams = {
  converted?: boolean
  status?: 'pending' | 'ready' | 'failed'
  q?: string
  page?: number
}

export const Route = createFileRoute('/$lang/papers/')({
  validateSearch: (search: Record<string, unknown>): ListSearchParams => ({
    converted:
      typeof search.converted === 'boolean' ? search.converted : undefined,
    status:
      search.status === 'pending' ||
      search.status === 'ready' ||
      search.status === 'failed'
        ? search.status
        : undefined,
    q: typeof search.q === 'string' && search.q ? search.q : undefined,
    page:
      typeof search.page === 'number' && search.page > 1
        ? Math.floor(search.page)
        : undefined,
  }),
  component: PapersListPage,
})

function PapersListPage() {
  const { t } = useTranslation('papers')
  const lang = useLang()
  const navigate = useNavigate()
  const search = Route.useSearch()

  // The page's purpose is showing converted papers, so the has_md filter
  // defaults ON; only an explicit converted=false turns it off.
  const converted = search.converted ?? true
  const page = search.page ?? 1
  const params: PapersListParams = {
    has_md: converted || undefined,
    status: search.status,
    q: search.q,
    page,
  }
  const list = usePapersList(params)

  function go(next: Partial<ListSearchParams>) {
    navigate({
      to: '/$lang/papers',
      params: { lang },
      // Changing any filter resets pagination to page 1.
      search: { ...search, ...next },
    })
  }

  const items = list.data?.items ?? []
  const total = list.data?.total ?? 0
  const perPage = list.data?.per_page ?? 20
  const totalPages = Math.max(1, Math.ceil(total / perPage))

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('list.eyebrow')}
        title={t('list.title')}
        copy={t('list.subtitle')}
      />

      <div className="flex flex-wrap items-center gap-2">
        <Button
          type="button"
          variant={converted ? 'default' : 'outline'}
          onClick={() => go({ converted: converted ? false : undefined, page: undefined })}
        >
          {converted ? <Check className="size-4" /> : <Filter className="size-4" />}
          {t('list.convertedOnly')}
        </Button>

        <Select
          value={search.status ?? 'all'}
          onValueChange={(value) =>
            go({
              status:
                value === 'all'
                  ? undefined
                  : (value as ListSearchParams['status']),
              page: undefined,
            })
          }
        >
          <SelectTrigger className="w-40">
            <SelectValue placeholder={t('list.statusFilter.placeholder')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t('list.statusFilter.all')}</SelectItem>
            <SelectItem value="pending">{t('detail.status.pending')}</SelectItem>
            <SelectItem value="ready">{t('detail.status.ready')}</SelectItem>
            <SelectItem value="failed">{t('detail.status.failed')}</SelectItem>
          </SelectContent>
        </Select>

        <form
          className="flex min-w-60 flex-1 items-center gap-2"
          onSubmit={(event) => {
            event.preventDefault()
            const form = new FormData(event.currentTarget)
            const q = String(form.get('q') ?? '').trim()
            go({ q: q || undefined, page: undefined })
          }}
        >
          <div className="relative flex-1">
            <Search className="pointer-events-none absolute left-3 top-1/2 size-4 -translate-y-1/2 text-muted-foreground" />
            <Input
              name="q"
              defaultValue={search.q ?? ''}
              placeholder={t('list.searchPlaceholder')}
              className="pl-9"
            />
          </div>
          <Button type="submit" variant="secondary">
            {t('list.apply')}
          </Button>
        </form>
      </div>

      <StatusBlock
        loading={list.isLoading}
        error={list.error?.message ?? ''}
        empty={!list.isLoading && items.length === 0}
        emptyMessage={t('list.empty')}
      >
        <div className="space-y-3">
          <div className="flex items-center gap-2 text-sm text-muted-foreground">
            {list.isFetching && <Loader2 className="size-3 animate-spin" />}
            <span>{t('list.total', { count: total })}</span>
          </div>

          <div className="overflow-x-auto rounded-xl border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th className="px-4 py-2.5 font-medium">{t('list.cols.title')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('list.cols.arxiv')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('list.cols.status')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('list.cols.converted')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('list.cols.images')}</th>
                  <th className="px-4 py-2.5 font-medium">{t('list.cols.createdAt')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {items.map((paper) => (
                  <tr key={paper.paper_id}>
                    <td className="max-w-md px-4 py-2.5">
                      <Link
                        to="/$lang/papers/$paperId"
                        params={{ lang, paperId: paper.paper_id }}
                        className="block truncate font-medium text-foreground hover:text-primary hover:underline"
                        title={paper.title || paper.paper_id}
                      >
                        {paper.title || t('untitled')}
                      </Link>
                    </td>
                    <td className="px-4 py-2.5 font-mono text-xs text-muted-foreground">
                      {paper.arxiv_id ?? '—'}
                    </td>
                    <td className="px-4 py-2.5">
                      <Badge
                        variant={
                          paper.status === 'ready' ? 'default' : 'secondary'
                        }
                      >
                        {t(`detail.status.${paper.status}`, {
                          defaultValue: paper.status,
                        })}
                      </Badge>
                    </td>
                    <td className="px-4 py-2.5">
                      {paper.has_md ? (
                        <Check className="size-4 text-primary" />
                      ) : (
                        <span className="text-muted-foreground">—</span>
                      )}
                    </td>
                    <td className="px-4 py-2.5 tabular-nums">
                      {paper.image_count}
                    </td>
                    <td className="px-4 py-2.5 text-muted-foreground">
                      {formatDate(paper.created_at)}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="flex items-center justify-between gap-3">
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={page <= 1}
              onClick={() => go({ page: page - 1 > 1 ? page - 1 : undefined })}
            >
              <ChevronLeft className="size-4" /> {t('list.prev')}
            </Button>
            <span className="text-sm text-muted-foreground tabular-nums">
              {t('list.pageOf', { page, pages: totalPages })}
            </span>
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={page >= totalPages}
              onClick={() => go({ page: page + 1 })}
            >
              {t('list.next')} <ChevronRight className="size-4" />
            </Button>
          </div>
        </div>
      </StatusBlock>
    </section>
  )
}

function formatDate(value?: string): string {
  if (!value) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString()
}
