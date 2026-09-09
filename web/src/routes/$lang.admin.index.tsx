import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  Activity,
  AlertCircle,
  BookOpenText,
  ChevronRight,
  Database,
  FileWarning,
  Upload,
  Gauge,
  KeyRound,
  Puzzle,
  Settings2,
  Server,
  UsersRound,
} from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import {
  adminPutPlan,
  adminPutQuota,
  type AdminAcquisitionFailure,
  type AdminPlan,
  type AdminSchemaTable,
  type AdminUsageRow,
} from '@/lib/api'
import {
  useAdminAcquisitionFailures,
  useUploadFailurePDF,
  useAdminPlans,
  useAdminSchema,
  useAdminUsage,
  useAdminWhoami,
} from '@/lib/queries'

export const Route = createFileRoute('/$lang/admin/')({
  component: AdminPage,
})

// Usage days are counted in UTC server-side, so the picker defaults to
// today's UTC date.
function todayUTC(): string {
  return new Date().toISOString().slice(0, 10)
}

function AdminPage() {
  const { t } = useTranslation('admin')
  const { lang } = Route.useParams()
  const whoami = useAdminWhoami()
  const isAdmin = whoami.data?.is_admin ?? false
  // DB-flag user managers (is_admin / is_superadmin on the users
  // record) reach the user-management surface without the env
  // allowlist; see internal/routes/admin_users.go.
  const canManageUsers = whoami.data?.is_user_admin ?? false
  const schema = useAdminSchema(isAdmin)
  const acquisitionFailures = useAdminAcquisitionFailures(isAdmin)
  const [day, setDay] = useState(todayUTC)
  const usage = useAdminUsage(day || undefined, isAdmin)
  const plans = useAdminPlans(isAdmin)

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />

      {/* User management entry: role-gated on is_user_admin (env admin,
          is_admin or is_superadmin); the target page applies the same
          gate. */}
      {canManageUsers && (
        <Panel title={t('users.title')} icon={UsersRound}>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">
              {t('users.subtitle')}
            </p>
            <Button asChild size="sm" variant="outline">
              <Link to="/$lang/admin/users" params={{ lang }}>
                {t('users.open')}
              </Link>
            </Button>
          </div>
        </Panel>
      )}

      {/* Dev docs entry: sphinx-hosted at /devdoc behind a server-side
          admin ticket gate; the target page mints the ticket. */}
      <Panel title={t('docs.title')} icon={BookOpenText}>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-sm text-muted-foreground">
            {t('docs.description')}
          </p>
          <Button asChild size="sm" variant="outline">
            <Link to="/$lang/admin/docs" params={{ lang }}>
              {t('docs.open')}
            </Link>
          </Button>
        </div>
      </Panel>

      {/* Plugins entry: the target page applies the same admin gate. */}
      <Panel title={t('plugins.title')} icon={Puzzle}>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-sm text-muted-foreground">
            {t('plugins.subtitle')}
          </p>
          <Button asChild size="sm" variant="outline">
            <Link to="/$lang/admin/plugins" params={{ lang }}>
              {t('plugins.open')}
            </Link>
          </Button>
        </div>
      </Panel>

      {isAdmin && (
        <Panel title={lang === 'zh' ? '下载工作节点' : 'Downloader workers'} icon={Server}>
          <div className="flex flex-wrap items-center justify-between gap-3">
            <p className="text-sm text-muted-foreground">
              {lang === 'zh' ? '审批节点注册，管理任务分配并监控容量、浏览器、磁盘及错误。' : 'Approve registrations, control job leasing and monitor capacity, browser health, disk and errors.'}
            </p>
            <Button asChild size="sm" variant="outline">
              <Link to="/$lang/admin/downloader-workers" params={{ lang }}>
                {lang === 'zh' ? '管理工作节点' : 'Manage workers'}
              </Link>
            </Button>
          </div>
        </Panel>
      )}

      <StatusBlock loading={whoami.isLoading} error="" empty={false}>
        {/* whoami is session-only: a 401/403 means no browser session, so
            offer the sign-in path instead of a raw error. */}
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
        ) : whoami.data && !isAdmin && !canManageUsers ? (
          <Alert>
            <AlertCircle className="size-4" />
            <AlertTitle>{t('adminOnly')}</AlertTitle>
            {whoami.data.login && (
              <AlertDescription>
                {t('signedInAs', { login: whoami.data.login })}
              </AlertDescription>
            )}
          </Alert>
        ) : isAdmin ? (
          <div className="space-y-8">
            <AcquisitionFailuresSection
              items={acquisitionFailures.data?.items ?? []}
              loading={acquisitionFailures.isLoading}
              error={acquisitionFailures.error?.message ?? ''}
              lang={lang}
            />

            <StatusBlock
              loading={schema.isLoading}
              error={schema.error?.message ?? ''}
              empty={!schema.isLoading && !schema.data?.tables.length}
            >
              {schema.data && (
                <div className="space-y-4">
                  <div className="flex flex-wrap items-center gap-2 text-sm">
                    <Badge variant="outline" className="gap-1.5 font-mono">
                      <Database className="size-3" />
                      {t('database')}: {schema.data.database}
                    </Badge>
                    <span className="text-muted-foreground">
                      {t('tables', { count: schema.data.tables.length })}
                    </span>
                  </div>

                  {schema.data.tables.map((table) => (
                    <TableCard key={table.name} table={table} />
                  ))}
                </div>
              )}
            </StatusBlock>

            <UsageSection
              day={day}
              onDayChange={setDay}
              usage={usage.data?.rows ?? []}
              loading={usage.isLoading}
              error={usage.error?.message ?? ''}
            />

            <PlansSection
              plans={plans.data?.plans ?? []}
              loading={plans.isLoading}
              error={plans.error?.message ?? ''}
              usageRows={usage.data?.rows ?? []}
            />
          </div>
        ) : (
          // User-manager without the env allowlist: no ops dashboard —
          // the users entry panel above is the whole surface.
          null
        )}
      </StatusBlock>
    </section>
  )
}

function AcquisitionFailuresSection({
  items,
  loading,
  error,
  lang,
}: {
  items: AdminAcquisitionFailure[]
  loading: boolean
  error: string
  lang: string
}) {
  const { t } = useTranslation('admin')
  const uploadPDF = useUploadFailurePDF()

  return (
    <Panel
      title={t('acquisitionFailures.title')}
      icon={FileWarning}
      suffix={`${items.length}`}
    >
      <p className="mb-3 text-sm text-muted-foreground">
        {t('acquisitionFailures.description')}
      </p>
      <StatusBlock
        loading={loading}
        error={error}
        empty={!loading && !error && items.length === 0}
        emptyMessage={t('acquisitionFailures.empty')}
      >
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">{t('acquisitionFailures.cols.paper')}</th>
                <th className="px-4 py-2 font-medium">{t('acquisitionFailures.cols.stage')}</th>
                <th className="px-4 py-2 font-medium">{t('acquisitionFailures.cols.attempts')}</th>
                <th className="px-4 py-2 font-medium">{t('acquisitionFailures.cols.time')}</th>
                <th className="px-4 py-2 font-medium">{t('acquisitionFailures.cols.reason')}</th>
                <th className="px-4 py-2 font-medium">{t('acquisitionFailures.cols.actions')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {items.map((item) => (
                <tr key={item.paper_id} className="align-top">
                  <td className="min-w-72 px-4 py-2">
                    <Link
                      to="/$lang/papers/$paperId"
                      params={{ lang, paperId: item.paper_id }}
                      className="line-clamp-2 font-medium text-primary hover:underline"
                    >
                      {item.title || item.arxiv_id || item.doi || item.paper_id}
                    </Link>
                    {item.arxiv_id ? (
                      <a
                        href={`https://arxiv.org/abs/${item.arxiv_id}`}
                        target="_blank"
                        rel="noreferrer"
                        className="mt-1 block text-xs text-muted-foreground hover:text-primary hover:underline"
                      >
                        arXiv:{item.arxiv_id} ↗
                      </a>
                    ) : item.doi ? (
                      <a
                        href={`https://doi.org/${item.doi}`}
                        target="_blank"
                        rel="noreferrer"
                        className="mt-1 block text-xs text-muted-foreground hover:text-primary hover:underline"
                      >
                        doi:{item.doi} ↗
                      </a>
                    ) : (
                      <code className="mt-1 block text-xs text-muted-foreground">
                        {item.paper_id}
                      </code>
                    )}
                  </td>
                  <td className="px-4 py-2">
                    <Badge variant="outline">
                      {item.stage || t('acquisitionFailures.legacy')}
                    </Badge>
                  </td>
                  <td className="px-4 py-2 tabular-nums">{item.attempts}</td>
                  <td className="whitespace-nowrap px-4 py-2 text-muted-foreground">
                    {formatAdminTime(item.failed_at)}
                  </td>
                  <td className="min-w-96 px-4 py-2">
                    {item.reason ? (
                      <code className="block whitespace-pre-wrap break-words rounded bg-muted px-2 py-1 text-xs text-destructive">
                        {item.reason}
                      </code>
                    ) : (
                      <span className="text-muted-foreground">
                        {t('acquisitionFailures.noLog')}
                      </span>
                    )}
                  </td>
                  <td className="whitespace-nowrap px-4 py-2">
                    {item.doi ? (
                      <label className="inline-flex cursor-pointer items-center gap-1.5 rounded-md border border-border px-2.5 py-1.5 text-xs font-medium hover:border-primary/50 hover:text-primary">
                        <Upload className="size-3.5" />
                        {uploadPDF.isPending &&
                        uploadPDF.variables?.doi === item.doi
                          ? t('acquisitionFailures.uploading')
                          : t('acquisitionFailures.uploadPDF')}
                        <input
                          type="file"
                          accept="application/pdf"
                          className="hidden"
                          onChange={(e) => {
                            const file = e.target.files?.[0]
                            e.target.value = ''
                            if (!file) return
                            uploadPDF.mutate(
                              { doi: item.doi!, file },
                              {
                                onSuccess: () =>
                                  toast.success(
                                    t('acquisitionFailures.uploaded', {
                                      doi: item.doi,
                                    }),
                                  ),
                                onError: (err: Error) => toast.error(err.message),
                              },
                            )
                          }}
                        />
                      </label>
                    ) : (
                      <span className="text-xs text-muted-foreground">—</span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </StatusBlock>
    </Panel>
  )
}

function formatAdminTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function TableCard({ table }: { table: AdminSchemaTable }) {
  const { t } = useTranslation('admin')
  const lang = useLang()

  return (
    <Card className="py-0">
      {/* Native <details> keeps this collapsible without an accordion dep.
          group/open variants rotate the chevron. */}
      <details className="group">
        <summary className="flex cursor-pointer list-none items-center gap-2 px-5 py-4 [&::-webkit-details-marker]:hidden">
          <ChevronRight className="size-4 shrink-0 text-muted-foreground transition-transform group-open:rotate-90" />
          <code className="text-sm font-semibold">{table.name}</code>
          <Link
            to="/$lang/admin/db/$table"
            params={{ lang, table: table.name }}
            className="text-xs text-primary hover:underline"
            // Keep the row browser link from toggling the <details>.
            onClick={(e) => e.stopPropagation()}
          >
            {t('dbrows.browse')}
          </Link>
          <span className="ml-auto flex items-center gap-2 text-xs text-muted-foreground">
            <Badge variant="secondary" className="tabular-nums">
              {t('rows', { count: table.row_estimate })}
            </Badge>
            <Badge variant="outline" className="tabular-nums">
              {table.total_size}
            </Badge>
          </span>
        </summary>

        <CardContent className="space-y-4 border-t border-border px-5 py-4">
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 font-medium">{t('cols.name')}</th>
                  <th className="px-4 py-2 font-medium">{t('cols.type')}</th>
                  <th className="px-4 py-2 font-medium">{t('cols.nullable')}</th>
                  <th className="px-4 py-2 font-medium">{t('cols.default')}</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {table.columns.map((col) => (
                  <tr key={col.name}>
                    <td className="px-4 py-2">
                      <code className="font-medium">{col.name}</code>
                      {col.is_pk && (
                        <Badge variant="default" className="ml-2">
                          <KeyRound className="size-3" /> {t('pk')}
                        </Badge>
                      )}
                    </td>
                    <td className="px-4 py-2 font-mono text-xs text-muted-foreground">
                      {col.data_type}
                    </td>
                    <td className="px-4 py-2 text-muted-foreground">
                      {col.nullable ? '✓' : '—'}
                    </td>
                    <td className="px-4 py-2 font-mono text-xs text-muted-foreground">
                      {col.default ?? '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>

          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-2">
              <h4 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                {t('indexes')}
              </h4>
              {table.indexes.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t('noIndexes')}</p>
              ) : (
                <ul className="space-y-1.5">
                  {table.indexes.map((idx) => (
                    <li key={idx.name}>
                      <code className="block break-all rounded bg-muted px-2 py-1 text-xs">
                        {idx.definition}
                      </code>
                    </li>
                  ))}
                </ul>
              )}
            </div>
            <div className="space-y-2">
              <h4 className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
                {t('constraints')}
              </h4>
              {table.constraints.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  {t('noConstraints')}
                </p>
              ) : (
                <ul className="space-y-1.5">
                  {table.constraints.map((con) => (
                    <li key={con.name}>
                      <Badge variant="outline" className="mb-1">
                        {con.kind}
                      </Badge>
                      <code className="block break-all rounded bg-muted px-2 py-1 text-xs">
                        {con.definition}
                      </code>
                    </li>
                  ))}
                </ul>
              )}
            </div>
          </div>
        </CardContent>
      </details>
    </Card>
  )
}

// --- Usage section -----------------------------------------------------------
//
// Per-user agentic-search metering for one UTC day (GET /api/admin/usage).
// The date picker re-queries; `cost` is USD and rendered with 4 decimals.

function UsageSection({
  day,
  onDayChange,
  usage,
  loading,
  error,
}: {
  day: string
  onDayChange: (day: string) => void
  usage: AdminUsageRow[]
  loading: boolean
  error: string
}) {
  const { t } = useTranslation('admin')

  return (
    <Panel title={t('usage.title')} icon={Activity}>
      <div className="mb-3 flex items-center gap-2">
        <label
          htmlFor="usage-day"
          className="text-sm text-muted-foreground"
        >
          {t('usage.dateLabel')}
        </label>
        <Input
          id="usage-day"
          type="date"
          value={day}
          max={todayUTC()}
          onChange={(e) => onDayChange(e.target.value)}
          className="w-40"
        />
      </div>

      <StatusBlock
        loading={loading}
        error={error}
        empty={!loading && !error && usage.length === 0}
        emptyMessage={t('usage.empty')}
      >
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">{t('usage.cols.user')}</th>
                <th className="px-4 py-2 font-medium">{t('usage.cols.plan')}</th>
                <th className="px-4 py-2 font-medium">{t('usage.cols.count')}</th>
                <th className="px-4 py-2 font-medium">{t('usage.cols.limit')}</th>
                <th className="px-4 py-2 font-medium">{t('usage.cols.tokens')}</th>
                <th className="px-4 py-2 font-medium">{t('usage.cols.cost')}</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {usage.map((row) => (
                <tr key={row.user_id}>
                  <td className="px-4 py-2 font-medium">{row.login}</td>
                  <td className="px-4 py-2">
                    <Badge variant="secondary">{row.plan}</Badge>
                  </td>
                  <td className="px-4 py-2 tabular-nums">{row.count}</td>
                  <td className="px-4 py-2 tabular-nums">
                    {row.effective_limit}
                  </td>
                  <td className="px-4 py-2 tabular-nums">{row.llm_tokens}</td>
                  <td className="px-4 py-2 tabular-nums">
                    ${row.cost.toFixed(4)}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </StatusBlock>
    </Panel>
  )
}

// --- Plans & per-user quotas -------------------------------------------------
//
// Plans table (GET /api/admin/plans) with inline daily-limit editing
// (PUT /api/admin/plans/{name}); per-user rows let an admin switch a user's
// plan and set/clear a daily-limit override (PUT /api/admin/quotas/{user_id}).
// The user list is derived from the usage rows of the selected day.

function PlansSection({
  plans,
  loading,
  error,
  usageRows,
}: {
  plans: AdminPlan[]
  loading: boolean
  error: string
  usageRows: AdminUsageRow[]
}) {
  const { t } = useTranslation('admin')

  return (
    <div className="space-y-4">
      <Panel title={t('plans.title')} icon={Settings2}>
        <StatusBlock
          loading={loading}
          error={error}
          empty={!loading && !error && plans.length === 0}
        >
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 font-medium">{t('plans.cols.name')}</th>
                  <th className="px-4 py-2 font-medium">
                    {t('plans.cols.dailyLimit')}
                  </th>
                  <th className="px-4 py-2 font-medium">
                    {t('plans.cols.description')}
                  </th>
                  <th className="px-4 py-2 font-medium">
                    {t('plans.cols.actions')}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {plans.map((plan) => (
                  <PlanRow key={plan.name} plan={plan} />
                ))}
              </tbody>
            </table>
          </div>
        </StatusBlock>
      </Panel>

      <Panel title={t('quotas.title')} icon={Gauge}>
        <p className="mb-3 text-sm text-muted-foreground">
          {t('quotas.hint')}
        </p>
        {usageRows.length === 0 ? (
          <p className="text-sm text-muted-foreground">{t('quotas.empty')}</p>
        ) : (
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  <th className="px-4 py-2 font-medium">{t('quotas.cols.user')}</th>
                  <th className="px-4 py-2 font-medium">{t('quotas.cols.plan')}</th>
                  <th className="px-4 py-2 font-medium">
                    {t('quotas.cols.override')}
                  </th>
                  <th className="px-4 py-2 font-medium">
                    {t('quotas.cols.actions')}
                  </th>
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {usageRows.map((row) => (
                  <QuotaRow
                    key={row.user_id}
                    row={row}
                    planNames={plans.map((p) => p.name)}
                  />
                ))}
              </tbody>
            </table>
          </div>
        )}
      </Panel>
    </div>
  )
}

function PlanRow({ plan }: { plan: AdminPlan }) {
  const { t } = useTranslation('admin')
  const qc = useQueryClient()
  const [limit, setLimit] = useState(String(plan.daily_agentic_search_limit))

  const saveMutation = useMutation({
    mutationFn: () =>
      adminPutPlan(plan.name, {
        daily_agentic_search_limit: Number(limit),
        description: plan.description,
      }),
    onSuccess: () => {
      toast.success(t('plans.saved'))
      void qc.invalidateQueries({ queryKey: ['admin-plans'] })
      void qc.invalidateQueries({ queryKey: ['admin-usage'] })
    },
  })

  const parsed = Number(limit)
  const valid =
    limit.trim() !== '' && Number.isInteger(parsed) && parsed >= 0

  return (
    <tr>
      <td className="px-4 py-2">
        <code className="font-medium">{plan.name}</code>
      </td>
      <td className="px-4 py-2">
        <Input
          type="number"
          min={0}
          value={limit}
          onChange={(e) => setLimit(e.target.value)}
          className="w-28"
        />
      </td>
      <td className="px-4 py-2 text-muted-foreground">
        {plan.description || '—'}
      </td>
      <td className="px-4 py-2">
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={!valid || saveMutation.isPending}
            onClick={() => saveMutation.mutate()}
          >
            {t('plans.save')}
          </Button>
          {saveMutation.error && (
            <span className="text-xs text-destructive">
              {saveMutation.error.message}
            </span>
          )}
        </div>
      </td>
    </tr>
  )
}

function QuotaRow({
  row,
  planNames,
}: {
  row: AdminUsageRow
  planNames: string[]
}) {
  const { t } = useTranslation('admin')
  const qc = useQueryClient()
  const [plan, setPlan] = useState(row.plan)
  const [override, setOverride] = useState('')

  const saveMutation = useMutation({
    mutationFn: () =>
      adminPutQuota(row.user_id, {
        plan,
        // Empty input means "clear the override" (back to the plan limit).
        daily_limit_override: override.trim() === '' ? null : Number(override),
      }),
    onSuccess: () => {
      toast.success(t('quotas.saved'))
      void qc.invalidateQueries({ queryKey: ['admin-usage'] })
    },
  })

  const parsed = Number(override)
  const valid =
    override.trim() === '' || (Number.isInteger(parsed) && parsed >= 0)
  // Make sure the user's current plan is selectable even if the plans
  // listing hasn't loaded (or doesn't include it).
  const options = planNames.includes(plan) ? planNames : [plan, ...planNames]

  return (
    <tr>
      <td className="px-4 py-2 font-medium">{row.login}</td>
      <td className="px-4 py-2">
        <Select value={plan} onValueChange={setPlan}>
          <SelectTrigger size="sm" className="w-28">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {options.map((name) => (
              <SelectItem key={name} value={name}>
                {name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </td>
      <td className="px-4 py-2">
        <Input
          type="number"
          min={0}
          value={override}
          placeholder={t('quotas.overridePlaceholder')}
          onChange={(e) => setOverride(e.target.value)}
          className="w-32"
        />
      </td>
      <td className="px-4 py-2">
        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={!valid || saveMutation.isPending}
            onClick={() => saveMutation.mutate()}
          >
            {t('quotas.save')}
          </Button>
          {saveMutation.error && (
            <span className="text-xs text-destructive">
              {saveMutation.error.message}
            </span>
          )}
        </div>
      </td>
    </tr>
  )
}
