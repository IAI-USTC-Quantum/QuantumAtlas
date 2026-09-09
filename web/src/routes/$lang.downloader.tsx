import { useMemo, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  AlertCircle,
  CheckCircle2,
  Clock3,
  Download,
  ListChecks,
  Loader2,
  Server,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { Textarea } from '@/components/ui/textarea'
import { useLang } from '@/hooks/use-lang'
import { isDownloaderAvailable, type DownloaderJob } from '@/lib/api'
import { parseIdentifier } from '@/lib/identifiers'
import {
  useDownloaderJobs,
  useDownloaderRemoteJobs,
  useDownloaderSubmit,
  usePlugins,
} from '@/lib/queries'

export const Route = createFileRoute('/$lang/downloader')({
  component: DownloaderPage,
})

// Mirrors the backend's maxDownloaderItems.
const MAX_ITEMS = 50
// Keep polling the job snapshot briefly after a submit even before the
// snapshot has picked the new jobs up (they may still be unlisted).
const SUBMIT_POLL_WINDOW_MS = 30_000

type ParsedLine = {
  line: string
  kind: 'doi' | 'arxiv' | 'url' | 'invalid'
  label?: string
}

function DownloaderPage() {
  const { t } = useTranslation('downloader')
  const lang = useLang()

  // Gated on the `downloader` builtin plugin being enabled and
  // connected; when it is off the form is disabled and hinted.
  const plugins = usePlugins()
  const available = isDownloaderAvailable(plugins.data?.plugins)

  const [text, setText] = useState('')
  const [submittedRecently, setSubmittedRecently] = useState(false)

  const parsed = useMemo<ParsedLine[]>(() => {
    return text
      .split('\n')
      .map((line) => line.trim())
      .filter((line) => line !== '')
      .map((line) => ({ line, ...parseIdentifier(line) }))
  }, [text])
  const validLines = useMemo(
    () => parsed.filter((p) => p.kind !== 'invalid').map((p) => p.line),
    [parsed],
  )
  const tooMany = parsed.length > MAX_ITEMS

  const submit = useDownloaderSubmit()

  const jobsQuery = useDownloaderJobs(submittedRecently, available)
  const jobs = jobsQuery.data?.jobs ?? []
  const counters = jobsQuery.data?.counters

  function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!available || validLines.length === 0 || validLines.length > MAX_ITEMS) return
    submit.mutate(validLines, {
      onSuccess: () => {
        setSubmittedRecently(true)
        window.setTimeout(
          () => setSubmittedRecently(false),
          SUBMIT_POLL_WINDOW_MS,
        )
      },
    })
  }

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />

      <Panel title={t('form.title')} icon={Download}>
        <form onSubmit={onSubmit} className="space-y-3">
          {!available && (
            <p className="text-sm text-muted-foreground">{t('unavailable')}</p>
          )}
          <Textarea
            value={text}
            onChange={(event) => setText(event.target.value)}
            rows={6}
            spellCheck={false}
            disabled={!available}
            placeholder={t('form.placeholder')}
            className="font-mono text-[13px] leading-6"
          />
          {parsed.length > 0 && (
            <div className="space-y-2">
              <p className="text-xs font-medium tracking-wide text-muted-foreground uppercase">
                {t('form.preview')}
              </p>
              {tooMany && (
                <p className="text-sm text-destructive">
                  {t('form.tooMany', { count: parsed.length, max: MAX_ITEMS })}
                </p>
              )}
              <div className="flex flex-wrap gap-x-4 gap-y-1.5">
                {parsed.slice(0, MAX_ITEMS).map((p, idx) => (
                  <span
                    key={`${idx}-${p.line}`}
                    className="inline-flex max-w-full items-center gap-1.5"
                  >
                    <Badge
                      variant={p.kind === 'invalid' ? 'destructive' : 'outline'}
                    >
                      {t(`kind.${p.kind}`)}
                    </Badge>
                    <span className="truncate font-mono text-xs text-muted-foreground">
                      {p.label ?? p.line}
                    </span>
                  </span>
                ))}
                {tooMany && (
                  <span className="text-xs text-muted-foreground">
                    {t('form.more', { count: parsed.length - MAX_ITEMS })}
                  </span>
                )}
              </div>
            </div>
          )}
          <div className="flex items-center gap-3">
            <Button
              type="submit"
              disabled={
                !available ||
                submit.isPending ||
                validLines.length === 0 ||
                validLines.length > MAX_ITEMS
              }
            >
              {submit.isPending ? (
                <Loader2 className="size-4 animate-spin" />
              ) : (
                <Download className="size-4" />
              )}
              {submit.isPending ? t('form.submitting') : t('form.submit')}
            </Button>
            <span className="text-xs text-muted-foreground">
              {t('form.validCount', { count: validLines.length })}
            </span>
          </div>
        </form>
      </Panel>

      <RemoteJobsPanel lang={lang} />

      <Panel
        title={t('jobs.title')}
        icon={ListChecks}
        suffix={
          counters
            ? t('jobs.counters', {
                succeeded: counters.succeeded ?? 0,
                failed: counters.failed ?? 0,
                in_flight: counters.in_flight ?? 0,
              })
            : undefined
        }
      >
        <p className="mb-3 text-xs text-muted-foreground">
          {lang === 'zh' ? '本地内存中的任务进度；服务器重启后此列表可能清空。远程任务请查看上方节点进度（如已启用）。' : 'Local in-memory progress; this list may reset after a server restart. Remote jobs appear in the worker progress panel above when enabled.'}
        </p>
        <StatusBlock
          loading={jobsQuery.isLoading}
          error={jobsQuery.error?.message ?? ''}
          empty={!jobsQuery.isLoading && !jobsQuery.error && jobs.length === 0}
          emptyMessage={t('jobs.empty')}
        >
          <div className="flex flex-col gap-3">
            {jobs.map((job) => (
              <JobRow key={job.paper_id} job={job} lang={lang} />
            ))}
          </div>
        </StatusBlock>
      </Panel>
    </section>
  )
}

function RemoteJobsPanel({ lang }: { lang: string }) {
  // Independent of the local plugin gate: durable remote jobs remain useful
  // after restarts, even when the local in-memory snapshot is unavailable.
  const query = useDownloaderRemoteJobs()
  const text = (en: string, zh: string) => lang === 'zh' ? zh : en
  // This is an optional feature; do not interrupt the local downloader when
  // remote mode is disabled or has not been discovered yet.
  if (!query.data?.enabled) return null

  const jobs = query.data.jobs ?? []
  const states: Record<string, string> = {
    queued: text('Queued', '排队中'),
    running: text('Running', '运行中'),
    staged: text('Archiving', '归档中'),
    done: text('Done', '已完成'),
    failed: text('Failed', '失败'),
  }

  return (
    <Panel
      title={text('Remote worker progress', '远程节点任务进度')}
      icon={Server}
      suffix={text('Refreshes every 5s', '每 5 秒刷新')}
    >
      <p className="mb-3 text-sm text-muted-foreground">
        {text('Persisted remote jobs survive server restarts. Running includes downloading and uploading; archiving means the uploaded result is being stored.', '远程任务进度持久化保存，服务器重启后仍可查看。运行中包含下载与上传；归档中表示正在保存已上传的结果。')}
      </p>
      <div className="mb-3 flex flex-wrap gap-2">
        {Object.entries(states).map(([state, label]) => (
          <Badge key={state} variant={state === 'failed' ? 'destructive' : 'outline'} className="tabular-nums">
            {label} {jobs.filter((job) => job.state === state).length}
          </Badge>
        ))}
      </div>
      <StatusBlock
        loading={query.isLoading}
        error={query.error?.message ?? ''}
        empty={!jobs.length}
        emptyMessage={text('No remote jobs yet.', '暂无远程任务。')}
      >
        <div className="overflow-x-auto rounded-lg border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                {[text('Identifier / job', '标识 / 任务'), text('State', '状态'), text('Worker ID', '节点 ID'), text('Updated', '更新时间'), text('Error', '错误')].map((label) => (
                  <th key={label} scope="col" className="px-4 py-2 font-medium">{label}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-border">
              {jobs.map((job) => (
                <tr key={job.id} className="align-top">
                  <td className="min-w-56 px-4 py-3">
                    <span className="break-all font-mono">{job.identifier || '—'}</span>
                    <code className="mt-1 block break-all text-xs text-muted-foreground">{job.id}</code>
                  </td>
                  <td className="px-4 py-3">
                    <Badge variant={job.state === 'failed' ? 'destructive' : job.state === 'done' ? 'default' : 'secondary'}>
                      {(job.state === 'running' || job.state === 'staged') && <Loader2 className="size-3 animate-spin" />}
                      {states[job.state] ?? job.state}
                    </Badge>
                  </td>
                  <td className="min-w-40 px-4 py-3"><code className="break-all text-xs">{job.worker_id || text('Unassigned', '未分配')}</code></td>
                  <td className="whitespace-nowrap px-4 py-3 text-muted-foreground">{formatRemoteJobTime(job.updated_at)}</td>
                  <td className="min-w-56 max-w-lg px-4 py-3"><span className="whitespace-pre-wrap break-words text-xs text-destructive">{job.error || '—'}</span></td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      </StatusBlock>
    </Panel>
  )
}

function formatRemoteJobTime(value?: string): string {
  if (!value || value.startsWith('0001-')) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function JobRow({ job, lang }: { job: DownloaderJob; lang: string }) {
  const { t } = useTranslation('downloader')
  const failed = job.state === 'failed'
  const done = job.state === 'done'
  const trace = job.trace ?? []

  return (
    <div className="space-y-2 rounded-lg border border-border/60 bg-muted/20 p-3">
      <div className="flex flex-wrap items-center gap-2">
        <span
          className="shrink-0"
          title={t(`state.${job.state}`, { defaultValue: job.state })}
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
        </span>
        {job.paper_id ? (
          <Link
            to="/$lang/papers/$paperId"
            params={{ lang, paperId: job.paper_id }}
            className="min-w-0 truncate font-mono text-sm text-primary hover:underline"
          >
            {job.input || job.paper_id}
          </Link>
        ) : (
          <span className="min-w-0 truncate font-mono text-sm">
            {job.input || job.paper_id}
          </span>
        )}
        {job.kind && (
          <Badge variant="outline">
            {t(`kind.${job.kind}`, { defaultValue: job.kind })}
          </Badge>
        )}
        {job.strategy && <Badge variant="secondary">{job.strategy}</Badge>}
        <span className="ml-auto text-xs text-muted-foreground">
          {t(`phase.${job.phase}`, { defaultValue: job.phase })}
        </span>
      </div>
      {job.error && (
        <p className="break-words text-xs text-destructive">{job.error}</p>
      )}
      {trace.length > 0 && (
        <details className="text-xs">
          <summary className="cursor-pointer select-none text-muted-foreground">
            {t('trace.title', { count: trace.length })}
          </summary>
          <ul className="mt-1.5 space-y-1 border-l border-border pl-3">
            {trace.map((attempt, idx) => (
              <li key={`${attempt.strategy}-${idx}`} className="break-all">
                <span className="font-medium">{attempt.strategy}</span>
                {attempt.ms !== undefined && <> · {attempt.ms} ms</>}
                {attempt.url && <> · {attempt.url}</>}
                {attempt.error && (
                  <span className="block text-destructive">{attempt.error}</span>
                )}
              </li>
            ))}
          </ul>
        </details>
      )}
    </div>
  )
}
