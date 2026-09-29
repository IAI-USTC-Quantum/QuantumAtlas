import { useTranslation } from 'react-i18next'

import type { BlockReading } from '@/lib/reader-types'
import { DiscussionThread } from './DiscussionThread'
import { DiscussionComposer } from './DiscussionComposer'

// Right pane: the combined block read (source + anchor + content +
// discussions, §7.1) plus the write controls. Selection of a block happens
// in the PDF overlay or the block list; this pane is read+write for that
// exact anchor.
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
              {!reading.content.bbox && (
                <span className="text-amber-600 dark:text-amber-400">{t('blockList.noBbox')}</span>
              )}
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
          <DiscussionComposer
            paperId={paperId}
            parseRevision={reading.source.parse_revision}
            anchor={reading.anchor}
          />
          <ul className="flex min-h-0 flex-1 flex-col gap-2 overflow-y-auto pr-1">
            {reading.discussions.map((discussion) => (
              <DiscussionThread
                key={discussion.discussion_id}
                discussion={discussion}
                lang={lang}
                paperId={paperId}
              />
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
