import { useEffect, useMemo, useState } from 'react'
import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, ChevronDown, ChevronRight, Clock, Cpu, MessageSquare, User } from 'lucide-react'

import { PageHeader } from '@/components/page-header'
import { StatusBlock } from '@/components/status-block'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { useLang } from '@/hooks/use-lang'
import { READER_API_MODE, type DiscussionFilters as FilterState, type DiscussionSummary } from '@/lib/reader-api'
import { useDiscussionDetail, useDiscussions, usePaperParses } from '@/lib/reader-queries'
import { usePaperDetail } from '@/lib/queries'
import { DiscussionFilterBar } from '@/components/reader/DiscussionFilters'
import { ScopeBadge, StatusBadge, TypeBadge } from '@/components/reader/badges'

type DiscussionsSearch = {
  scope?: FilterState['scope']
  status?: FilterState['status']
  type?: string
  d?: string
}

export const Route = createFileRoute('/$lang/papers/$paperId_/discussions')({
  validateSearch: (search: Record<string, unknown>): DiscussionsSearch => ({
    scope:
      search.scope === 'public' || search.scope === 'lean' ? search.scope : undefined,
    status:
      search.status === 'pending' ||
      search.status === 'confirmed' ||
      search.status === 'retracted' ||
      search.status === 'none'
        ? search.status
        : undefined,
    type: typeof search.type === 'string' && search.type ? search.type : undefined,
    d: typeof search.d === 'string' ? search.d : undefined,
  }),
  component: PaperDiscussionsPage,
})

function formatWhen(value: string): string {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

function PaperDiscussionsPage() {
  const { t } = useTranslation('reader')
  const lang = useLang()
  const navigate = useNavigate({ from: '/$lang/papers/$paperId/discussions' })
  const { paperId } = Route.useParams()
  const search = Route.useSearch()
  const detail = usePaperDetail(paperId || null)
  const parses = usePaperParses(paperId || null)
  const paper = detail.data

  const filters = {
    scope: search.scope ?? '',
    status: search.status ?? '',
    type: search.type ?? '',
  } satisfies FilterState
  const [cursor, setCursor] = useState<string | undefined>(undefined)
  // Filters reset pagination; the cursor only pages forward within one
  // filter combination.
  const filterKey = JSON.stringify(filters)
  useEffect(() => setCursor(undefined), [filterKey])

  const discussions = useDiscussions(paperId || null, { ...filters, cursor })

  // Custom types discovered in the current page of results feed the type
  // filter so custom slugs stay reachable (plan §12.1.4).
  const knownTypes = useMemo(
    () => (discussions.data?.discussions ?? []).map((d) => d.type),
    [discussions.data],
  )

  const revisionLabel = useMemo(() => {
    const byId = new Map((parses.data?.parses ?? []).map((p) => [p.revision_id, p]))
    return (revision: string) => {
      const parse = byId.get(revision)
      const short = revision.length > 14 ? `${revision.slice(0, 14)}…` : revision
      return parse?.is_current ? `${short} · ${t('discussionsPage.currentTag')}` : short
    }
  }, [parses.data, t])

  const patchSearch = (patch: DiscussionsSearch) =>
    void navigate({
      to: '/$lang/papers/$paperId/discussions',
      params: { lang, paperId: paperId! },
      search: (prev: DiscussionsSearch) => ({ ...prev, ...patch }),
    })

  return (
    <section className="space-y-5">
      <Link
        to="/$lang/papers/$paperId"
        params={{ lang, paperId: paperId! }}
        className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" />
        {t('discussionsPage.back')}
      </Link>

      <StatusBlock
        loading={detail.isLoading}
        error={detail.error?.message ?? ''}
        empty={!paper}
      >
        {paper && (
          <>
            <PageHeader
              eyebrow={t('discussionsPage.eyebrow')}
              title={t('discussionsPage.title')}
              copy={paper.title || paper.paper_ref || paper.paper_id}
            />
            {READER_API_MODE === 'mock' && (
              <Badge variant="secondary" data-testid="reader-mock-badge" title={t('workbench.mockHint')}>
                {t('workbench.mockBadge')}
              </Badge>
            )}
            <DiscussionFilterBar
              value={filters}
              onChange={(next) =>
                patchSearch({
                  scope: next.scope || undefined,
                  status: next.status || undefined,
                  type: next.type || undefined,
                })
              }
              extraTypes={knownTypes}
            />
            <StatusBlock
              loading={discussions.isLoading && !discussions.data}
              error={discussions.error?.message ?? ''}
              empty={(discussions.data?.discussions ?? []).length === 0 && !discussions.isLoading}
            >
              {discussions.data && discussions.data.discussions.length === 0 && (
                <p className="text-sm text-muted-foreground" data-testid="discussions-empty">
                  {t('discussionsPage.empty')}
                </p>
              )}
              <ul className="flex flex-col gap-2" data-testid="discussions-list">
                {(discussions.data?.discussions ?? []).map((discussion) => (
                  <DiscussionRow
                    key={discussion.discussion_id}
                    discussion={discussion}
                    expanded={search.d === discussion.discussion_id}
                    revisionLabel={revisionLabel(discussion.parse_revision)}
                    lang={lang}
                    paperId={paperId!}
                    onToggle={() =>
                      patchSearch({ d: search.d === discussion.discussion_id ? undefined : discussion.discussion_id })
                    }
                  />
                ))}
              </ul>
              {discussions.data?.next_cursor && (
                <Button
                  variant="outline"
                  size="sm"
                  className="mt-3"
                  onClick={() => setCursor(discussions.data?.next_cursor ?? undefined)}
                  data-testid="discussions-load-more"
                >
                  {t('discussionsPage.loadMore')}
                </Button>
              )}
            </StatusBlock>
          </>
        )}
      </StatusBlock>
    </section>
  )
}

function DiscussionRow({
  discussion,
  expanded,
  revisionLabel,
  lang,
  paperId,
  onToggle,
}: {
  discussion: DiscussionSummary
  expanded: boolean
  revisionLabel: string
  lang: string
  paperId: string
  onToggle: () => void
}) {
  const { t } = useTranslation('reader')
  const detail = useDiscussionDetail(expanded ? discussion.discussion_id : null)

  return (
    <li className="rounded-xl border border-border" data-testid="discussion-row" data-discussion-id={discussion.discussion_id}>
      <div className="flex flex-col gap-2 p-3">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          className="flex items-start gap-2 text-left"
          data-testid="discussion-row-toggle"
        >
          {expanded ? (
            <ChevronDown className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronRight className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
          )}
          <span className="min-w-0 flex-1">
            <span className="flex flex-wrap items-center gap-1.5">
              <StatusBadge status={discussion.status} />
              <TypeBadge type={discussion.type} />
              <ScopeBadge scope={discussion.scope} />
              <span className="ml-auto inline-flex items-center gap-0.5 text-xs text-muted-foreground">
                <MessageSquare className="size-3" />
                <span className="tabular-nums">{discussion.reply_count}</span>
                <span className="sr-only">{t('discussion.replies', { count: discussion.reply_count })}</span>
              </span>
            </span>
            <span className="mt-1.5 block text-sm whitespace-pre-wrap break-words line-clamp-3">
              {discussion.body}
            </span>
            <span className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
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
            </span>
          </span>
        </button>
        <div className="flex flex-wrap items-center gap-2">
          <Link
            to="/$lang/papers/$paperId"
            params={{ lang, paperId }}
            search={{ rev: discussion.parse_revision, page: discussion.page_idx + 1, block: discussion.block_index }}
            className="rounded-md border border-border bg-muted/40 px-2 py-0.5 font-mono text-[11px] hover:border-primary/50"
            data-testid="anchor-link"
          >
            {t('discussionsPage.anchorLink', {
              page: discussion.page_idx + 1,
              block: discussion.block_index,
              rev: revisionLabel,
            })}
          </Link>
        </div>
        {expanded && (
          <div className="rounded-lg bg-muted/30 p-3" data-testid="discussion-detail">
            {detail.isLoading && (
              <p role="status" className="text-sm text-muted-foreground">
                {t('discussionsPage.loadingDetail')}
              </p>
            )}
            {detail.error && (
              <p role="alert" className="text-sm text-destructive">
                {detail.error.message}
              </p>
            )}
            {detail.data && (
              <>
                <p className="text-sm whitespace-pre-wrap break-words">{detail.data.body}</p>
                {detail.data.replies.length > 0 && (
                  <>
                    <p className="mt-3 text-xs font-semibold text-muted-foreground">
                      {t('discussionsPage.repliesHeading', { count: detail.data.replies.length })}
                    </p>
                    <ul className="mt-1.5 flex flex-col gap-2">
                      {detail.data.replies.map((reply) => (
                        <li key={reply.reply_id} className="rounded-md border border-border bg-card p-2.5" data-testid="reply-item">
                          <p className="text-sm whitespace-pre-wrap break-words">{reply.body}</p>
                          <p className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
                            <span className="inline-flex items-center gap-1">
                              <User className="size-3" />
                              {reply.created_by}
                            </span>
                            {reply.model && (
                              <span className="inline-flex items-center gap-1" title={t('discussion.modelDeclared')}>
                                <Cpu className="size-3" />
                                {reply.model}
                              </span>
                            )}
                            <span className="inline-flex items-center gap-1">
                              <Clock className="size-3" />
                              {formatWhen(reply.created_at)}
                            </span>
                            <span className="font-mono" title={t('discussion.revisionTitle')}>r{reply.revision}</span>
                          </p>
                        </li>
                      ))}
                    </ul>
                  </>
                )}
                {detail.data.replies.length === 0 && (
                  <p className="mt-2 text-xs text-muted-foreground">{t('discussionsPage.noReplies')}</p>
                )}
              </>
            )}
          </div>
        )}
      </div>
    </li>
  )
}
