import { useTranslation } from 'react-i18next'
import { Link } from '@tanstack/react-router'
import { Clock, Cpu, MessageSquare, User } from 'lucide-react'

import type { BlockReading, DiscussionSummary } from '@/lib/reader-api'
import { ScopeBadge, StatusBadge, TypeBadge } from './badges'

function formatWhen(value: string): string {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

// One discussion card: author, optional model DECLARATION (purely
// informational, plan §6.3), timestamps, status/type/scope badges. Bodies
// render as plain text — comment content is untrusted data, never markup.
function DiscussionCard({ discussion, lang, paperId }: {
  discussion: DiscussionSummary
  lang: string
  paperId: string
}) {
  const { t } = useTranslation('reader')
  return (
    <li
      className="rounded-lg border border-border p-3"
      data-testid="discussion-card"
      data-discussion-id={discussion.discussion_id}
    >
      <div className="flex flex-wrap items-center gap-1.5">
        <StatusBadge status={discussion.status} />
        <TypeBadge type={discussion.type} />
        <ScopeBadge scope={discussion.scope} />
        <span className="ml-auto inline-flex items-center gap-0.5 text-xs text-muted-foreground">
          <MessageSquare className="size-3" />
          <span className="tabular-nums">{discussion.reply_count}</span>
          <span className="sr-only">{t('discussion.replies', { count: discussion.reply_count })}</span>
        </span>
      </div>
      <p className="mt-2 text-sm whitespace-pre-wrap break-words">{discussion.body}</p>
      <div className="mt-2 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        <span className="inline-flex items-center gap-1">
          <User className="size-3" />
          {discussion.created_by}
        </span>
        {discussion.model && (
          <span className="inline-flex items-center gap-1" title={t('discussion.modelDeclared')}>
            <Cpu className="size-3" />
            {discussion.model}
          </span>
        )}
        <span className="inline-flex items-center gap-1">
          <Clock className="size-3" />
          {formatWhen(discussion.updated_at)}
        </span>
        <span className="font-mono" title={t('discussion.revisionTitle')}>
          r{discussion.revision}
        </span>
      </div>
      <Link
        to="/$lang/papers/$paperId/discussions"
        params={{ lang, paperId }}
        search={{ d: discussion.discussion_id }}
        className="mt-1.5 inline-block text-xs text-primary hover:underline"
      >
        {t('discussion.openThread')}
      </Link>
    </li>
  )
}

// Right pane: discussions anchored to the selected block, sourced from the
// combined block read (source + anchor + content + discussions, §7.1).
export function DiscussionPanel({
  reading,
  loading,
  error,
  lang,
  paperId,
}: {
  reading: BlockReading | undefined
  loading: boolean
  error: string
  lang: string
  paperId: string
}) {
  const { t } = useTranslation('reader')

  if (!reading && !loading && !error) {
    return (
      <p className="text-sm text-muted-foreground" data-testid="discussion-panel-empty">
        {t('discussion.pickBlockFirst')}
      </p>
    )
  }

  return (
    <div className="flex min-h-0 flex-col gap-2" data-testid="discussion-panel">
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      {loading && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('discussion.loading')}
        </p>
      )}
      {reading && (
        <>
          <div className="rounded-lg border border-border bg-muted/30 p-2.5 text-xs" data-testid="block-context">
            <div className="flex flex-wrap items-center gap-1.5 font-mono text-muted-foreground">
              <span className="rounded bg-muted px-1.5 py-0.5 font-semibold text-foreground">
                p{reading.anchor.page_idx + 1} #{reading.anchor.block_index}
              </span>
              <span>{reading.content.type}</span>
            </div>
            <p className="mt-1.5 font-mono break-all">{reading.content.content}</p>
            <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-2 gap-y-0.5 text-[11px] text-muted-foreground">
              <dt>{t('discussion.parseLabel')}</dt>
              <dd className="truncate font-mono" title={reading.source.parse_revision}>
                {reading.source.parse_revision}
              </dd>
              <dt>{t('discussion.schemaLabel')}</dt>
              <dd className="font-mono">{reading.source.schema} v{reading.source.schema_version}</dd>
              <dt>{t('discussion.pdfShaLabel')}</dt>
              <dd className="truncate font-mono" title={reading.source.pdf_sha256}>
                {reading.source.pdf_sha256.slice(0, 12)}…
              </dd>
            </dl>
          </div>
          <ul className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto pr-1">
            {reading.discussions.map((discussion) => (
              <DiscussionCard key={discussion.discussion_id} discussion={discussion} lang={lang} paperId={paperId} />
            ))}
            {reading.discussions.length === 0 && (
              <li className="rounded-lg border border-dashed border-border p-3 text-sm text-muted-foreground">
                {t('discussion.noDiscussions')}
              </li>
            )}
          </ul>
        </>
      )}
    </div>
  )
}
