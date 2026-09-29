import { useTranslation } from 'react-i18next'
import { cn } from '@/lib/utils'
import type { DiscussionScope, DiscussionStatus } from '@/lib/reader-api'

// Discussion status machine (plan §12.1.4): every discussion carries an
// OPTIONAL status — pending / confirmed / retracted, or null for a plain
// note. Confirmed is not a platform endorsement; retracted keeps content
// visible. Colors deliberately do not hide disputes (plan §7.3).
export function StatusBadge({ status }: { status: DiscussionStatus | null }) {
  const { t } = useTranslation('reader')
  if (status === null) {
    return (
      <span
        className="inline-flex w-fit items-center rounded-full border border-border px-2 py-0.5 text-xs font-medium text-muted-foreground"
        data-testid="status-badge"
        data-status="none"
      >
        {t('status.none')}
      </span>
    )
  }
  const styles: Record<DiscussionStatus, string> = {
    pending: 'bg-amber-500/15 text-amber-700 dark:text-amber-400',
    confirmed: 'bg-emerald-500/15 text-emerald-700 dark:text-emerald-400',
    retracted: 'bg-muted text-muted-foreground line-through decoration-muted-foreground/60',
  }
  return (
    <span
      className={cn('inline-flex w-fit items-center rounded-full px-2 py-0.5 text-xs font-medium', styles[status])}
      data-testid="status-badge"
      data-status={status}
    >
      {t(`status.${status}`)}
    </span>
  )
}

export function ScopeBadge({ scope }: { scope: DiscussionScope }) {
  const { t } = useTranslation('reader')
  return (
    <span
      className="inline-flex w-fit items-center rounded-full border border-border px-2 py-0.5 text-xs font-medium text-muted-foreground"
      data-testid="scope-badge"
      data-scope={scope}
    >
      {t(`scope.${scope}`)}
    </span>
  )
}

// Type is just a bounded string tag (plan §12.1.4): presets plus custom
// slugs. Custom types render as-is; no type implies permissions.
export const PRESET_TYPES = ['normal', 'transcription_error', 'typo_in_original'] as const

export function TypeBadge({ type }: { type: string }) {
  const { t } = useTranslation('reader')
  const preset = (PRESET_TYPES as readonly string[]).includes(type)
  return (
    <span
      className="inline-flex w-fit items-center rounded-md bg-secondary px-1.5 py-0.5 font-mono text-[11px] text-secondary-foreground"
      data-testid="type-badge"
      data-type={type}
    >
      {preset ? t(`type.${type}`) : type}
    </span>
  )
}
