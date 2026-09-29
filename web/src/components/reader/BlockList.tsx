import { useTranslation } from 'react-i18next'
import { AlertTriangle, MessageSquare } from 'lucide-react'
import type { ReaderBlock } from '@/lib/reader-api'
import { cn } from '@/lib/utils'

// Middle pane: the top-level blocks of the current page for the selected
// parse revision. Non-contiguous block indexes are the norm (plan §12.5) —
// the list shows the real block.index, never a renumbered sequence.
export function BlockList({
  blocks,
  loading,
  error,
  selectedIndex,
  discussionCounts,
  onSelect,
}: {
  blocks: ReaderBlock[]
  loading: boolean
  error: string
  selectedIndex: number | null
  discussionCounts: Map<number, number>
  onSelect: (block: ReaderBlock) => void
}) {
  const { t } = useTranslation('reader')
  return (
    <div className="flex min-h-0 flex-col gap-2" data-testid="block-list">
      <p className="text-xs text-muted-foreground">
        {t('blockList.hint', { count: blocks.length })}
      </p>
      {loading && (
        <p role="status" className="text-sm text-muted-foreground">
          {t('blockList.loading')}
        </p>
      )}
      {error && (
        <p role="alert" className="text-sm text-destructive">
          {error}
        </p>
      )}
      <ul className="flex min-h-0 flex-1 flex-col gap-1.5 overflow-y-auto pr-1">
        {blocks.map((block) => {
          const count = discussionCounts.get(block.index) ?? 0
          const selected = selectedIndex === block.index
          return (
            <li key={block.index}>
              <button
                type="button"
                data-testid={`block-item-${block.index}`}
                aria-pressed={selected}
                onClick={() => onSelect(block)}
                className={cn(
                  'w-full rounded-lg border p-2.5 text-left transition-colors',
                  selected
                    ? 'border-primary bg-primary/10'
                    : 'border-border hover:border-primary/50 hover:bg-accent/50',
                )}
              >
                <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
                  <span className="rounded bg-muted px-1.5 py-0.5 font-mono font-semibold tabular-nums text-foreground">
                    #{block.index}
                  </span>
                  <span className="font-mono">{block.type}</span>
                  {!block.bbox && (
                    <span
                      className="inline-flex items-center gap-0.5 text-amber-600 dark:text-amber-400"
                      title={t('blockList.noBbox')}
                    >
                      <AlertTriangle className="size-3" />
                      <span className="sr-only">{t('blockList.noBbox')}</span>
                    </span>
                  )}
                  {count > 0 && (
                    <span className="ml-auto inline-flex items-center gap-0.5 text-primary">
                      <MessageSquare className="size-3" />
                      <span className="tabular-nums">{count}</span>
                      <span className="sr-only">{t('blockList.discussionCount', { count })}</span>
                    </span>
                  )}
                </span>
                <span className="mt-1 block line-clamp-3 font-mono text-xs break-words text-foreground/90">
                  {block.content}
                </span>
              </button>
            </li>
          )
        })}
        {!loading && !error && blocks.length === 0 && (
          <li className="rounded-lg border border-dashed border-border p-3 text-sm text-muted-foreground">
            {t('blockList.emptyPage')}
          </li>
        )}
      </ul>
    </div>
  )
}
