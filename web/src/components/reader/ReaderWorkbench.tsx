import { useEffect, useMemo, useState } from 'react'
import { Link, useNavigate, useSearch } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { BookOpenText, ExternalLink, MessageSquareText, PanelLeftClose, PanelLeftOpen, PanelRightClose, PanelRightOpen } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import { useLang } from '@/hooks/use-lang'
import {
  MOCK_PAPER_ID,
  READER_API_MODE,
  type ReaderBlock,
} from '@/lib/reader-api'
import {
  useBlockReading,
  useDiscussions,
  usePaperParses,
  usePaperPdfBytes,
  usePaperSources,
  useParseBlocks,
} from '@/lib/reader-queries'
import type { CanvasRotation } from '@/lib/reader-geometry'
import { PdfCanvas, type PdfGeometry } from './PdfCanvas'
import { BlockOverlay } from './BlockOverlay'
import { BlockList } from './BlockList'
import { DiscussionPanel } from './DiscussionPanel'

type ReaderSearch = { rev?: string; page?: number; block?: number }

// Three-pane reading workbench (PDF | blocks | discussions, plan §7.3 and
// §12.1): the paper detail page hosts it. Selection state lives in the URL
// (?rev=&page=&block=) so the Issue-style discussions page can deep-link
// back to an exact block of an exact parse revision — old links must keep
// pointing at the material they were created against (§2.2.12).
export function ReaderWorkbench({ paperId }: { paperId: string }) {
  const { t } = useTranslation('reader')
  const lang = useLang()
  const navigate = useNavigate({ from: '/$lang/papers/$paperId' })
  const search = useSearch({ strict: false }) as ReaderSearch

  const [rotation, setRotation] = useState<CanvasRotation>(0)
  const [geometry, setGeometry] = useState<PdfGeometry | null>(null)
  const [showBlocks, setShowBlocks] = useState(true)
  const [showDiscussions, setShowDiscussions] = useState(true)

  const sources = usePaperSources(paperId)
  const parses = usePaperParses(paperId)

  const parseList = parses.data?.parses ?? []
  const revision =
    (search.rev && parseList.some((p) => p.revision_id === search.rev)
      ? search.rev
      : parseList.find((p) => p.is_current)?.revision_id) ?? null
  const pageIdx = Math.max(0, (search.page ?? 1) - 1)
  const selected =
    search.block !== undefined && search.rev !== undefined
      ? { page_idx: pageIdx, block_index: search.block }
      : null

  // Keep the URL honest when we fell back to the current revision or page 1.
  useEffect(() => {
    if (!parseList.length) return
    if (search.rev === revision && search.page === pageIdx + 1) return
    void navigate({
      to: '/$lang/papers/$paperId',
      params: { lang, paperId },
      search: (prev: ReaderSearch) => ({
        ...prev,
        rev: revision ?? undefined,
        page: pageIdx + 1,
      }),
      replace: true,
    })
  }, [parseList.length, revision, pageIdx, search.rev, search.page, lang, paperId, navigate])

  const activeParse = parseList.find((p) => p.revision_id === revision) ?? null
  const activeSource =
    sources.data?.sources.find((s) => s.source_id === activeParse?.source_id) ?? null

  const pdfBytes = usePaperPdfBytes(paperId, activeSource?.source_id ?? null)
  const blocks = useParseBlocks(paperId, revision, pageIdx)
  const discussions = useDiscussions(paperId, revision ? { parse_revision: revision } : {})
  const reading = useBlockReading(paperId, revision, selected)

  const discussionCounts = useMemo(() => {
    const counts = new Map<number, number>()
    for (const d of discussions.data?.discussions ?? []) {
      if (d.page_idx !== pageIdx) continue
      counts.set(d.block_index, (counts.get(d.block_index) ?? 0) + 1)
    }
    return counts
  }, [discussions.data, pageIdx])

  const pageBlocks = blocks.data?.blocks ?? []

  const patchSearch = (patch: Partial<ReaderSearch>) =>
    void navigate({
      to: '/$lang/papers/$paperId',
      params: { lang, paperId },
      search: (prev: ReaderSearch) => ({ ...prev, ...patch }),
      replace: false,
    })

  const selectBlock = (block: ReaderBlock) => {
    patchSearch({ page: block.page_idx + 1, block: block.index })
  }

  const unavailable =
    READER_API_MODE === 'mock' &&
    (sources.isError || parses.isError) &&
    paperId !== MOCK_PAPER_ID

  const gridClass = showBlocks && showDiscussions
    ? 'lg:grid-cols-[minmax(0,1.5fr)_minmax(15rem,0.8fr)_minmax(16rem,1fr)]'
    : showBlocks
      ? 'lg:grid-cols-[minmax(0,2fr)_minmax(15rem,0.9fr)]'
      : showDiscussions
        ? 'lg:grid-cols-[minmax(0,2fr)_minmax(16rem,1fr)]'
        : 'lg:grid-cols-1'

  return (
    <section
      className="rounded-xl border border-border bg-card"
      aria-label={t('workbench.title')}
      data-testid="reader-workbench"
    >
      <header className="flex flex-wrap items-center gap-2 border-b border-border px-4 py-3">
        <BookOpenText className="size-4 text-primary" />
        <h2 className="text-sm font-semibold">{t('workbench.title')}</h2>
        {READER_API_MODE === 'mock' && (
          <Badge variant="secondary" data-testid="reader-mock-badge" title={t('workbench.mockHint')}>
            {t('workbench.mockBadge')}
          </Badge>
        )}
        <label className="ml-auto flex items-center gap-1.5 text-xs text-muted-foreground">
          {t('workbench.parseLabel')}
          <select
            data-testid="parse-select"
            value={revision ?? ''}
            disabled={parseList.length === 0}
            onChange={(e) => patchSearch({ rev: e.target.value, block: undefined })}
            className="h-8 max-w-56 rounded-md border border-border bg-transparent px-2 font-mono text-xs"
          >
            {parseList.length === 0 && <option value="">{t('workbench.noParses')}</option>}
            {parseList.map((parse) => (
              <option key={parse.revision_id} value={parse.revision_id}>
                {parse.revision_id}
                {parse.is_current ? ` · ${t('workbench.currentTag')}` : ''} ({parse.schema} v{parse.schema_version})
              </option>
            ))}
          </select>
        </label>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={showBlocks ? t('workbench.collapseBlocks') : t('workbench.expandBlocks')}
          aria-pressed={showBlocks}
          onClick={() => setShowBlocks((v) => !v)}
        >
          {showBlocks ? <PanelLeftClose className="size-4" /> : <PanelLeftOpen className="size-4" />}
        </Button>
        <Button
          variant="ghost"
          size="icon-sm"
          aria-label={showDiscussions ? t('workbench.collapseDiscussions') : t('workbench.expandDiscussions')}
          aria-pressed={showDiscussions}
          onClick={() => setShowDiscussions((v) => !v)}
        >
          {showDiscussions ? <PanelRightClose className="size-4" /> : <PanelRightOpen className="size-4" />}
        </Button>
        <Button variant="outline" size="sm" asChild>
          <Link to="/$lang/papers/$paperId/discussions" params={{ lang, paperId }}>
            <MessageSquareText className="size-3.5" />
            {t('workbench.discussionsLink')}
            <ExternalLink className="size-3" />
          </Link>
        </Button>
      </header>

      {activeSource && (
        <p className="border-b border-border px-4 py-1.5 font-mono text-[11px] text-muted-foreground">
          {t('workbench.sourceLabel')}: {activeSource.source_id} · {activeSource.origin} · sha256 {activeSource.sha256.slice(0, 12)}… · {activeSource.size_bytes} B
        </p>
      )}

      {unavailable ? (
        <div className="px-4 py-6 text-sm text-muted-foreground" data-testid="reader-unavailable">
          <p>{t('workbench.mockUnavailable', { paperId })}</p>
          <Button variant="outline" size="sm" asChild className="mt-2">
            <Link to="/$lang/papers/$paperId" params={{ lang, paperId: MOCK_PAPER_ID }}>
              {t('workbench.openFixture')}
            </Link>
          </Button>
        </div>
      ) : (
        <div className={cn('grid min-h-[32rem] grid-cols-1 gap-3 p-3 lg:h-[42rem]', gridClass)}>
          <div className="flex min-h-[24rem] min-w-0 flex-col lg:min-h-0">
            <PdfCanvas
              bytes={pdfBytes.data ?? null}
              loading={pdfBytes.isLoading || sources.isLoading || parses.isLoading}
              error={pdfBytes.error?.message ?? ''}
              page={pageIdx + 1}
              onPageChange={(page) => patchSearch({ page, block: undefined })}
              rotation={rotation}
              onRotate={setRotation}
              onGeometry={setGeometry}
              overlay={
                <BlockOverlay
                  blocks={pageBlocks}
                  rotation={geometry?.rotation ?? 0}
                  cssWidth={geometry?.cssWidth ?? 0}
                  cssHeight={geometry?.cssHeight ?? 0}
                  selectedIndex={selected?.page_idx === pageIdx ? selected.block_index : null}
                  discussionCounts={discussionCounts}
                  onSelect={selectBlock}
                />
              }
            />
          </div>
          {showBlocks && (
            <div className="flex min-h-0 min-w-0 flex-col border-t border-border pt-3 lg:border-t-0 lg:border-l lg:pt-0 lg:pl-3">
              <BlockList
                blocks={pageBlocks}
                loading={blocks.isLoading || parses.isLoading}
                error={blocks.error?.message ?? ''}
                selectedIndex={selected?.page_idx === pageIdx ? selected.block_index : null}
                discussionCounts={discussionCounts}
                onSelect={selectBlock}
              />
            </div>
          )}
          {showDiscussions && (
            <div className="flex min-h-0 min-w-0 flex-col border-t border-border pt-3 lg:border-t-0 lg:border-l lg:pt-0 lg:pl-3">
              <DiscussionPanel
                reading={reading.data}
                loading={reading.isLoading}
                error={reading.error?.message ?? ''}
                lang={lang}
                paperId={paperId}
              />
            </div>
          )}
        </div>
      )}
    </section>
  )
}
