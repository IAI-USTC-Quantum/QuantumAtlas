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
