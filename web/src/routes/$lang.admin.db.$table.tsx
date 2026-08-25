import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  AlertCircle,
  ArrowLeft,
  ChevronLeft,
  ChevronRight,
  Loader2,
} from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { PageHeader } from '@/components/page-header'
import { StatusBlock } from '@/components/status-block'
import { useAdminDBRows, useAdminWhoami } from '@/lib/queries'

const PER_PAGE_OPTIONS = [20, 50, 100] as const

type DBRowsSearchParams = {
  page?: number
  per_page?: number
}

export const Route = createFileRoute('/$lang/admin/db/$table')({
  validateSearch: (search: Record<string, unknown>): DBRowsSearchParams => ({
    page:
      typeof search.page === 'number' && search.page > 1
        ? Math.floor(search.page)
        : undefined,
    per_page: (PER_PAGE_OPTIONS as readonly number[]).includes(
      Number(search.per_page),
    )
      ? Number(search.per_page)
      : undefined,
  }),
  component: AdminDBRowsPage,
})

function AdminDBRowsPage() {
  const { t } = useTranslation('admin')
  const { lang, table } = Route.useParams()
  const navigate = useNavigate()
  const search = Route.useSearch()
  const whoami = useAdminWhoami()
  const isAdmin = whoami.data?.is_admin ?? false

  const page = search.page ?? 1
  const perPage = search.per_page ?? 20
  const rows = useAdminDBRows(table, page, perPage, isAdmin)

  function go(next: Partial<DBRowsSearchParams>) {
    navigate({
      to: '/$lang/admin/db/$table',
      params: { lang, table },
      search: { ...search, ...next },
    })
  }

  const total = rows.data?.total ?? 0
  const totalPages = Math.max(1, Math.ceil(total / perPage))

  return (
    <section className="space-y-5">
      <div className="space-y-2">
        <Button asChild variant="ghost" size="sm" className="-ml-2">
          <Link to="/$lang/admin" params={{ lang }}>
            <ArrowLeft className="size-4" /> {t('dbrows.backToAdmin')}
          </Link>
        </Button>
        <PageHeader
          eyebrow={t('eyebrow')}
          title={table}
          copy={t('dbrows.subtitle', { table })}
        />
      </div>

      <StatusBlock loading={whoami.isLoading} error="" empty={false}>
        {/* Same session gate as the main admin page. */}
        {whoami.error ? (
          <Alert>
            <AlertCircle className="size-4" />
            <AlertTitle>{t('loginPrompt')}</AlertTitle>
            <AlertDescription className="mt-2">
              <Button asChild size="sm">
                <Link to="/login">{t('loginButton')}</Link>
              </Button>
            </AlertDescription>
          </Alert>
        ) : whoami.data && !isAdmin ? (
          <Alert>
            <AlertCircle className="size-4" />
            <AlertTitle>{t('adminOnly')}</AlertTitle>
            {whoami.data.login && (
              <AlertDescription>
                {t('signedInAs', { login: whoami.data.login })}
              </AlertDescription>
            )}
          </Alert>
        ) : (
          <StatusBlock
            loading={rows.isLoading}
            error={rows.error?.message ?? ''}
            empty={!rows.isLoading && rows.data?.rows.length === 0}
            emptyMessage={t('dbrows.empty')}
          >
            <div className="space-y-3">
              <div className="flex items-center gap-2 text-sm text-muted-foreground">
                {rows.isFetching && (
                  <Loader2 className="size-3 animate-spin" />
                )}
                <span>{t('dbrows.total', { count: total })}</span>
              </div>

              <div className="overflow-x-auto rounded-lg border border-border">
                <table className="w-full text-sm">
                  <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                    <tr>
                      {(rows.data?.columns ?? []).map((col) => (
                        <th key={col} className="px-4 py-2 font-medium">
                          {col}
                        </th>
                      ))}
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {(rows.data?.rows ?? []).map((row, rowIdx) => (
                      <tr key={rowIdx}>
                        {row.map((cell, cellIdx) => (
                          <td
                            key={cellIdx}
                            className="max-w-96 truncate px-4 py-2 font-mono text-xs"
                            title={cellTitle(cell)}
                          >
                            <CellValue value={cell} />
                          </td>
                        ))}
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>

              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="flex items-center gap-2">
                  <span className="text-sm text-muted-foreground">
                    {t('dbrows.perPage')}
                  </span>
                  <Select
                    value={String(perPage)}
                    onValueChange={(value) =>
                      // Changing the page size resets to page 1.
                      go({
                        per_page:
                          Number(value) === 20 ? undefined : Number(value),
                        page: undefined,
                      })
                    }
                  >
                    <SelectTrigger size="sm" className="w-20">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {PER_PAGE_OPTIONS.map((n) => (
                        <SelectItem key={n} value={String(n)}>
                          {n}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>

                <div className="flex items-center gap-3">
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={page <= 1}
                    onClick={() =>
                      go({ page: page - 1 > 1 ? page - 1 : undefined })
                    }
                  >
                    <ChevronLeft className="size-4" /> {t('dbrows.prev')}
                  </Button>
                  <span className="text-sm text-muted-foreground tabular-nums">
                    {t('dbrows.pageOf', { page, pages: totalPages })}
                  </span>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    disabled={page >= totalPages}
                    onClick={() => go({ page: page + 1 })}
                  >
                    {t('dbrows.next')} <ChevronRight className="size-4" />
                  </Button>
                </div>
              </div>
            </div>
          </StatusBlock>
        )}
      </StatusBlock>
    </section>
  )
}

function CellValue({ value }: { value: unknown }) {
  if (value === null || value === undefined) {
    return <span className="text-muted-foreground">NULL</span>
  }
  if (typeof value === 'object') {
    return <>{JSON.stringify(value)}</>
  }
  return <>{String(value as string | number | boolean)}</>
}

function cellTitle(value: unknown): string | undefined {
  if (value === null || value === undefined) return undefined
  return typeof value === 'object' ? JSON.stringify(value) : String(value)
}
