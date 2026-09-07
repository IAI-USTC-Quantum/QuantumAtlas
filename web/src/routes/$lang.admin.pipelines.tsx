import { Fragment } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  AlertCircle,
  ArrowLeft,
  CheckCircle2,
  Clock3,
  Cog,
  Download,
  Loader2,
} from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { type DownloaderJob, type MineruStatus } from '@/lib/api'
import {
  useAdminMineruStatus,
  useAdminPipelineJobs,
  useAdminWhoami,
} from '@/lib/queries'

export const Route = createFileRoute('/$lang/admin/pipelines')({
  component: AdminPipelinesPage,
})

function AdminPipelinesPage() {
  const { t } = useTranslation('admin')
  const { lang } = Route.useParams()
  const whoami = useAdminWhoami()
  const isAdmin = whoami.data?.is_admin ?? false

  const mineru = useAdminMineruStatus(isAdmin)
  const jobs = useAdminPipelineJobs(isAdmin)
  const anyActive = jobs.data?.jobs.some((job) => job.active) ?? false

  return (
    <section className="space-y-5">
      <div className="space-y-2">
        <Button asChild variant="ghost" size="sm" className="-ml-2">
          <Link to="/$lang/admin" params={{ lang }}>
            <ArrowLeft className="size-4" /> {t('pipelines.backToAdmin')}
          </Link>
        </Button>
        <PageHeader
          eyebrow={t('eyebrow')}
          title={t('pipelines.title')}
          copy={t('pipelines.subtitle')}
        />
      </div>

      <StatusBlock loading={whoami.isLoading} error="" empty={false}>
        {/* Same session gate as the other admin pages. */}
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
          <div className="space-y-5">
            <MineruPanel
              loading={mineru.isLoading}
              error={mineru.error?.message ?? ''}
              data={mineru.data}
            />

            <DownloaderPanel
              loading={jobs.isLoading}
              error={jobs.error?.message ?? ''}
              jobs={jobs.data?.jobs ?? []}
              counters={jobs.data?.counters}
              anyActive={anyActive}
              lang={lang}
            />
          </div>
        )}
      </StatusBlock>
    </section>
  )
}

// --- MinerU conversion pipeline ------------------------------------------------
//
// Scheduler snapshot (GET /api/admin/mineru/status): run state, today's
// daily-cap consumption, the last run's counters, and the next scheduled
// run. Every field but `running` may be absent on a fresh server.

function MineruPanel({
  loading,
  error,
  data,
}: {
  loading: boolean
  error: string
  data?: MineruStatus
}) {
  const { t } = useTranslation('admin')
  const running = data?.running ?? false
  const converted = data?.converted_today ?? 0
  const cap = data?.daily_cap ?? 0
  const pct = cap > 0 ? Math.min(100, (converted / cap) * 100) : 0
  const lastRun = data?.last_run

  return (
    <Panel
      title={t('pipelines.mineru.title')}
      icon={Cog}
      suffix={
        running ? (
          <Badge
            variant="outline"
            className="border-emerald-500/50 text-emerald-600 dark:border-emerald-400/40 dark:text-emerald-400"
          >
            <Loader2 className="animate-spin" />
            {t('pipelines.mineru.running')}
          </Badge>
        ) : (
          <Badge variant="secondary">{t('pipelines.mineru.idle')}</Badge>
        )
      }
    >
      <StatusBlock loading={loading} error={error} empty={false}>
        {data && (
          <div className="space-y-5">
            <div>
              <div className="mb-1.5 flex flex-wrap items-baseline justify-between gap-2 text-sm">
                <span className="text-muted-foreground">
                  {t('pipelines.mineru.todayConverted')}
                </span>
                <span className="font-medium tabular-nums">
                  {converted.toLocaleString()} / {cap.toLocaleString()}
                  <span className="ml-1.5 text-xs font-normal text-muted-foreground">
                    {t('pipelines.mineru.dailyCap')}
                  </span>
                </span>
              </div>
              <div
                role="progressbar"
                aria-valuemin={0}
                aria-valuemax={cap}
                aria-valuenow={converted}
                aria-label={t('pipelines.mineru.todayConverted')}
                className="h-2 overflow-hidden rounded-full bg-muted"
              >
                <div
                  className="h-full rounded-full bg-primary transition-[width] duration-500"
                  style={{ width: `${pct}%` }}
                />
              </div>
              {data.cap_day && (
                <p className="mt-1 text-xs text-muted-foreground">
                  {t('pipelines.mineru.capDay')}: {data.cap_day}
                </p>
              )}
            </div>

            <div>
              <h4 className="mb-2 text-xs font-medium uppercase tracking-wide text-muted-foreground">
                {t('pipelines.mineru.lastRun')}
              </h4>
              {lastRun ? (
                <div className="grid grid-cols-2 gap-3 sm:grid-cols-4">
                  <LastRunStat
                    label={t('pipelines.mineru.processed')}
                    value={lastRun.processed}
                  />
                  <LastRunStat
                    label={t('pipelines.mineru.succeeded')}
                    value={lastRun.succeeded}
                    className="text-emerald-600 dark:text-emerald-400"
                  />
                  <LastRunStat
                    label={t('pipelines.mineru.failed')}
                    value={lastRun.failed}
                    className={
                      (lastRun.failed ?? 0) > 0 ? 'text-destructive' : ''
                    }
                  />
                  <LastRunStat
                    label={t('pipelines.mineru.skipped')}
                    value={lastRun.skipped}
                  />
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">
                  {t('pipelines.mineru.noLastRun')}
                </p>
              )}
            </div>

            <div className="flex flex-col gap-1.5 text-sm sm:flex-row sm:flex-wrap sm:gap-x-6">
              <span className="text-muted-foreground">
                {t('pipelines.mineru.nextRun')}:{' '}
                <span className="text-foreground">
                  {formatPipelineTime(data.next_run_at)}
                </span>
              </span>
              {data.last_stop_reason && (
                <span className="text-muted-foreground">
                  {t('pipelines.mineru.lastStopReason')}:{' '}
                  <code className="rounded bg-muted px-1.5 py-0.5 font-mono text-xs text-foreground">
                    {data.last_stop_reason}
                  </code>
                </span>
              )}
            </div>
          </div>
        )}
      </StatusBlock>
    </Panel>
  )
}

function LastRunStat({
  label,
  value,
  className = '',
}: {
  label: string
  value?: number
  className?: string
}) {
  return (
    <div className="rounded-lg border border-border/60 bg-muted/20 px-3 py-2">
      <div className="text-xs text-muted-foreground">{label}</div>
      <div className={`text-xl font-semibold tabular-nums ${className}`}>
        {(value ?? 0).toLocaleString()}
      </div>
    </div>
  )
}

// --- Downloader pipeline -------------------------------------------------------
//
// Same job snapshot as the downloader page (GET /api/downloader/jobs),
// rendered as a table with an expandable per-job strategy trace.

function DownloaderPanel({
  loading,
  error,
  jobs,
  counters,
  anyActive,
  lang,
}: {
  loading: boolean
  error: string
  jobs: DownloaderJob[]
  counters?: {
    queued?: number
    in_flight?: number
    succeeded?: number
    failed?: number
    skipped?: number
  }
  anyActive: boolean
  lang: string
}) {
  const { t } = useTranslation('admin')

  return (
    <Panel
      title={t('pipelines.downloader.title')}
      icon={Download}
      suffix={
        <span className="inline-flex items-center gap-1.5">
          <Loader2 className="size-3 animate-spin text-muted-foreground" />
          {t('pipelines.downloader.autoRefresh')}
          <span className="text-muted-foreground/70">
            · {anyActive ? '2s' : '10s'}
          </span>
        </span>
      }
    >
      <div className="mb-4 flex flex-wrap items-center gap-2">
        <Badge variant="secondary" className="tabular-nums">
          {t('pipelines.downloader.counters.queued')}{' '}
          {counters?.queued ?? 0}
        </Badge>
        <Badge className="tabular-nums">
          {t('pipelines.downloader.counters.inFlight')}{' '}
          {counters?.in_flight ?? 0}
        </Badge>
        <Badge
          variant="outline"
          className="border-emerald-500/50 text-emerald-600 tabular-nums dark:border-emerald-400/40 dark:text-emerald-400"
        >
          {t('pipelines.downloader.counters.succeeded')}{' '}
          {counters?.succeeded ?? 0}
        </Badge>
        <Badge variant="destructive" className="tabular-nums">
          {t('pipelines.downloader.counters.failed')}{' '}
          {counters?.failed ?? 0}
        </Badge>
        <Badge variant="outline" className="tabular-nums">
          {t('pipelines.downloader.counters.skipped')}{' '}
          {counters?.skipped ?? 0}
        </Badge>
      </div>

      <StatusBlock
        loading={loading}
        error={error}
        empty={!loading && !error && jobs.length === 0}
        emptyMessage={t('pipelines.downloader.empty')}
      >
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-4 py-2 font-medium">
                  {t('pipelines.downloader.cols.status')}
                </th>
                <th className="px-4 py-2 font-medium">
                  {t('pipelines.downloader.cols.input')}
                </th>
                <th className="px-4 py-2 font-medium">
                  {t('pipelines.downloader.cols.strategy')}
                </th>
                <th className="px-4 py-2 font-medium">
                  {t('pipelines.downloader.cols.phase')}
                </th>
                <th className="px-4 py-2 font-medium">
                  {t('pipelines.downloader.cols.error')}
                </th>
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {jobs.map((job) => (
                <JobRow key={job.paper_id} job={job} lang={lang} />
              ))}
            </tbody>
          </table>
        </div>
      </StatusBlock>
    </Panel>
  )
}

function JobRow({ job, lang }: { job: DownloaderJob; lang: string }) {
  const { t } = useTranslation('admin')
  const { t: td } = useTranslation('downloader')
  const failed = job.state === 'failed'
  const done = job.state === 'done'
  const trace = job.trace ?? []

  return (
    <Fragment>
      <tr className="align-top">
        <td className="whitespace-nowrap px-4 py-2">
          <span
            className="inline-flex items-center gap-1.5"
            title={td(`state.${job.state}`, { defaultValue: job.state })}
          >
            {job.active ? (
              <Loader2 className="size-4 animate-spin text-primary" />
            ) : failed ? (
              <AlertCircle className="size-4 text-destructive" />
            ) : done ? (
              <CheckCircle2 className="size-4 text-emerald-500" />
            ) : (
              <Clock3 className="size-4 text-muted-foreground" />
            )}
            {td(`state.${job.state}`, { defaultValue: job.state })}
          </span>
        </td>
        <td className="min-w-48 px-4 py-2">
          {job.paper_id ? (
            <Link
              to="/$lang/papers/$paperId"
              params={{ lang, paperId: job.paper_id }}
              className="font-mono text-primary hover:underline"
            >
              {job.input || job.paper_id}
            </Link>
          ) : (
            <span className="font-mono">{job.input || job.paper_id}</span>
          )}
        </td>
        <td className="whitespace-nowrap px-4 py-2">
          {job.strategy ? (
            <Badge variant="secondary">{job.strategy}</Badge>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </td>
        <td className="whitespace-nowrap px-4 py-2 text-muted-foreground">
          {td(`phase.${job.phase}`, { defaultValue: job.phase })}
        </td>
        <td className="min-w-64 px-4 py-2">
          {job.error ? (
            <span className="block text-xs text-destructive">
              {job.error}
            </span>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </td>
      </tr>
      {trace.length > 0 && (
        <tr>
          <td colSpan={5} className="px-4 pb-3">
            <details className="text-xs">
              <summary className="cursor-pointer select-none text-muted-foreground">
                {t('pipelines.downloader.trace', { count: trace.length })}
              </summary>
              <ul className="mt-1.5 space-y-1 border-l border-border pl-3">
                {trace.map((attempt, idx) => (
                  <li
                    key={`${attempt.strategy}-${idx}`}
                    className="break-all"
                  >
                    <span className="font-medium">{attempt.strategy}</span>
                    {attempt.ms !== undefined && <> · {attempt.ms} ms</>}
                    {attempt.url && <> · {attempt.url}</>}
                    {attempt.error && (
                      <span className="block text-destructive">
                        {attempt.error}
                      </span>
                    )}
                  </li>
                ))}
              </ul>
            </details>
          </td>
        </tr>
      )}
    </Fragment>
  )
}

function formatPipelineTime(value?: string): string {
  if (!value) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
