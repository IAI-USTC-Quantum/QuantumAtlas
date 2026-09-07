import {
  AlertCircle,
  ArrowRight,
  CheckCircle2,
  CloudDownload,
  FileText,
  Hourglass,
  RefreshCw,
} from 'lucide-react'
import type { LucideIcon } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import type { AcquisitionStatus } from '@/lib/api'
import { cn } from '@/lib/utils'

type Props = {
  acquisition?: AcquisitionStatus
}

// Backend phase vocabulary (internal/routes/acquisition.go) collapsed into
// the four-stage pipeline this chip surfaces: download (waiting / running)
// then conversion (waiting / running), plus the two terminal states.
const DOWNLOAD_PHASES = new Set([
  'resolving_source',
  'resolving_version',
  'downloading_pdf',
  'fetching_pdf',
  'storing_pdf',
  'registering_asset',
])
const WAIT_CONVERT_PHASES = new Set(['pdf_ready', 'waiting_mineru', 'mineru_queued'])
const CONVERT_PHASES = new Set(['mineru_started', 'mineru_running', 'converting_md'])
const DOWNLOAD_FAIL_PHASES = new Set(['error_fetching'])

type Stage =
  | 'waitingDownload'
  | 'downloading'
  | 'waitingConvert'
  | 'converting'
  | 'done'
  | 'failed'
  | 'unknown'

type SegmentState = 'pending' | 'waiting' | 'active' | 'done' | 'error'

function classifyStage(acquisition: AcquisitionStatus): Stage {
  if (acquisition.state === 'failed') return 'failed'
  if (acquisition.state === 'done' || acquisition.phase === 'ready') return 'done'
  if (CONVERT_PHASES.has(acquisition.phase)) return 'converting'
  if (WAIT_CONVERT_PHASES.has(acquisition.phase)) return 'waitingConvert'
  if (DOWNLOAD_PHASES.has(acquisition.phase)) return 'downloading'
  if (acquisition.state === 'queued' && acquisition.phase === 'queued') return 'waitingDownload'
  return 'unknown'
}

function segmentStates(stage: Stage, phase: string): [SegmentState, SegmentState] {
  switch (stage) {
    case 'waitingDownload':
      return ['waiting', 'pending']
    case 'downloading':
      return ['active', 'pending']
    case 'waitingConvert':
      return ['done', 'waiting']
    case 'converting':
      return ['done', 'active']
    case 'done':
      return ['done', 'done']
    case 'failed':
      // Pin the error to whichever leg of the pipeline failed.
      return DOWNLOAD_PHASES.has(phase) || DOWNLOAD_FAIL_PHASES.has(phase)
        ? ['error', 'pending']
        : ['done', 'error']
    default:
      return ['pending', 'pending']
  }
}

const SEGMENT_CLASS: Record<SegmentState, string> = {
  pending: 'border-border/60 bg-muted/30 text-muted-foreground',
  waiting: 'border-amber-500/40 bg-amber-500/10 text-amber-900 dark:text-amber-200',
  active: 'border-primary/40 bg-primary/10 text-primary',
  done: 'border-emerald-500/50 bg-emerald-500/10 text-emerald-600 dark:border-emerald-400/40 dark:text-emerald-400',
  error: 'border-destructive/50 bg-destructive/10 text-destructive',
}

function SegmentIcon({ segment, state }: { segment: 'download' | 'convert'; state: SegmentState }) {
  let Icon: LucideIcon
  let animation = ''
  switch (state) {
    case 'active':
      Icon = segment === 'download' ? CloudDownload : RefreshCw
      animation = segment === 'download' ? 'animate-pulse' : 'animate-spin'
      break
    case 'waiting':
      Icon = Hourglass
      animation = 'animate-pulse'
      break
    case 'done':
      Icon = CheckCircle2
      break
    case 'error':
      Icon = AlertCircle
      break
    default:
      Icon = segment === 'download' ? CloudDownload : FileText
  }
  return <Icon className={cn('size-3 shrink-0', animation)} />
}

function PipelineSegment({
  segment,
  state,
  label,
}: {
  segment: 'download' | 'convert'
  state: SegmentState
  label: string
}) {
  return (
    <span
      className={cn(
        'inline-flex items-center gap-1 rounded-full border px-2 py-0.5 font-medium whitespace-nowrap transition-colors',
        SEGMENT_CLASS[state],
      )}
    >
      <SegmentIcon segment={segment} state={state} />
      {label}
    </span>
  )
}

function formatEta(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds <= 0) return ''
  if (seconds < 60) return `${Math.round(seconds)}s`
  if (seconds < 3600) {
    const minutes = Math.floor(seconds / 60)
    const rest = Math.round(seconds % 60)
    return rest > 0 ? `${minutes}m${rest}s` : `${minutes}m`
  }
  const hours = Math.floor(seconds / 3600)
  const minutes = Math.round((seconds % 3600) / 60)
  return minutes > 0 ? `${hours}h${minutes}m` : `${hours}h`
}

// One-line pipeline progress for search result cards: a two-segment
// download → convert stepper, the current status label, and — while waiting
// on the global MinerU concurrency slot — the queue badge.
export function PaperPipelineChip({ acquisition }: Props) {
  const { t } = useTranslation('papers')
  if (!acquisition) return null

  const stage = classifyStage(acquisition)
  const [downloadState, convertState] = segmentStates(stage, acquisition.phase)
  const queue = acquisition.queue
  const eta = queue?.eta_seconds ? formatEta(queue.eta_seconds) : ''

  const statusText =
    stage === 'unknown'
      ? t(`acquisition.phase.${acquisition.phase}`, { defaultValue: acquisition.phase })
      : t(`pipeline.${stage}`)

  return (
    <div
      className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs"
      title={stage === 'failed' ? acquisition.error || undefined : undefined}
    >
      <span className="inline-flex items-center gap-1">
        <PipelineSegment
          segment="download"
          state={downloadState}
          label={t('pipeline.step.download')}
        />
        <ArrowRight className="size-3 shrink-0 text-muted-foreground/60" />
        <PipelineSegment
          segment="convert"
          state={convertState}
          label={t('pipeline.step.convert')}
        />
      </span>
      <span
        className={cn(
          'font-medium',
          stage === 'done' && 'text-emerald-600 dark:text-emerald-400',
          stage === 'failed' && 'text-destructive',
        )}
      >
        {statusText}
      </span>
      {queue?.position ? (
        <Badge variant="outline" className="tabular-nums">
          {t('pipeline.queueHint', {
            position: queue.position,
            ahead: queue.ahead_of_me,
            max: queue.max_concurrent,
          })}
          {eta ? ` · ${t('pipeline.queueEta', { eta })}` : ''}
        </Badge>
      ) : null}
    </div>
  )
}
