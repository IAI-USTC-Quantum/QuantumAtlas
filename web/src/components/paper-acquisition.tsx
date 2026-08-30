import { AlertCircle, CheckCircle2, Clock3, Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { Badge } from '@/components/ui/badge'
import type { AcquisitionStatus } from '@/lib/api'

type Props = {
  acquisition?: AcquisitionStatus
  compact?: boolean
}

export function PaperAcquisition({ acquisition, compact = false }: Props) {
  const { t } = useTranslation('papers')
  if (!acquisition) return null

  const failed = acquisition.state === 'failed'
  const done = acquisition.state === 'done'
  const events = compact
    ? acquisition.events.slice(Math.max(0, acquisition.events.length - 3))
    : acquisition.events

  return (
    <div className="space-y-2 rounded-lg border border-border/60 bg-muted/20 p-3">
      <div className="flex flex-wrap items-center gap-2">
        {acquisition.active ? (
          <Loader2 className="size-4 animate-spin text-primary" />
        ) : failed ? (
          <AlertCircle className="size-4 text-destructive" />
        ) : done ? (
          <CheckCircle2 className="size-4 text-emerald-500" />
        ) : (
          <Clock3 className="size-4 text-muted-foreground" />
        )}
        <span className="text-sm font-medium">
          {t(`acquisition.phase.${acquisition.phase}`, {
            defaultValue: acquisition.phase,
          })}
        </span>
        {acquisition.queue?.position ? (
          <Badge variant="outline" className="tabular-nums">
            {t('acquisition.queue', {
              position: acquisition.queue.position,
              ahead: acquisition.queue.ahead_of_me,
            })}
          </Badge>
        ) : null}
      </div>

      {acquisition.active && (
        <div className="h-1 overflow-hidden rounded-full bg-muted">
          <div className="h-full w-2/3 animate-pulse rounded-full bg-primary" />
        </div>
      )}

      {events.length > 0 && (
        <ol className="space-y-1 border-l border-border pl-3 text-xs text-muted-foreground">
          {events.map((event, index) => (
            <li key={`${event.at}-${event.phase}-${index}`} className="relative">
              <span className="absolute -left-[0.94rem] top-1.5 size-1.5 rounded-full bg-current" />
              <span className="text-foreground">
                {t(`acquisition.phase.${event.phase}`, {
                  defaultValue: event.phase,
                })}
              </span>
              <span className="ml-2">{formatTime(event.at)}</span>
              {event.detail && !compact ? (
                <p
                  className={`mt-0.5 break-words ${
                    event.state === 'failed' ? 'text-destructive' : 'text-muted-foreground'
                  }`}
                >
                  {event.detail}
                </p>
              ) : null}
            </li>
          ))}
        </ol>
      )}
    </div>
  )
}

function formatTime(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleTimeString()
}
