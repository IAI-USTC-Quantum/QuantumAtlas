import { useTranslation } from 'react-i18next'
import { normalizedBBoxToRect, type CanvasRotation, type Rect } from '@/lib/reader-geometry'
import type { ReaderBlock } from '@/lib/reader-api'
import { cn } from '@/lib/utils'

// Bbox frames drawn over the rendered PDF page. Frames derive from the same
// normalized [0,1] coordinates regardless of zoom or rotation, so they stay
// glued to the block (plan §7.3: no fake frames — blocks without a bbox are
// simply not drawn here and are flagged in the block list instead).
export function BlockOverlay({
  blocks,
  rotation,
  cssWidth,
  cssHeight,
  selectedIndex,
  discussionCounts,
  onSelect,
}: {
  blocks: ReaderBlock[]
  rotation: CanvasRotation
  cssWidth: number
  cssHeight: number
  selectedIndex: number | null
  discussionCounts: Map<number, number>
  onSelect: (block: ReaderBlock) => void
}) {
  const { t } = useTranslation('reader')
  const positioned = blocks.filter(
    (block): block is ReaderBlock & { bbox: [number, number, number, number] } =>
      Array.isArray(block.bbox) && block.bbox.length === 4 && block.bbox.every((v) => Number.isFinite(v)),
  )
  return (
    <>
      {positioned.map((block) => {
        const rect: Rect = normalizedBBoxToRect(block.bbox, rotation, cssWidth, cssHeight)
        const count = discussionCounts.get(block.index) ?? 0
        const selected = selectedIndex === block.index
        return (
          <button
            key={block.index}
            type="button"
            data-testid={`block-overlay-${block.index}`}
            data-block-index={block.index}
            aria-label={t('overlay.selectBlock', {
              index: block.index,
              page: block.page_idx + 1,
              count,
            })}
            onClick={() => onSelect(block)}
            className={cn(
              'pointer-events-auto absolute rounded-sm border transition-colors',
              selected
                ? 'border-primary bg-primary/25 ring-2 ring-primary/70'
                : 'border-primary/50 bg-primary/10 hover:border-primary hover:bg-primary/20',
            )}
            style={{
              left: `${rect.left}px`,
              top: `${rect.top}px`,
              width: `${rect.width}px`,
              height: `${rect.height}px`,
            }}
          >
            <span
              className={cn(
                'absolute -top-2.5 -left-0.5 rounded-full px-1.5 text-[10px] leading-4 font-semibold tabular-nums',
                selected ? 'bg-primary text-primary-foreground' : 'bg-primary/80 text-primary-foreground',
              )}
            >
              {block.index}
              {count > 0 ? ` · ${count}` : ''}
            </span>
          </button>
        )
      })}
    </>
  )
}
