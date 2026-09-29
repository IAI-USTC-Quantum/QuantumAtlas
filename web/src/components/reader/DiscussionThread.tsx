import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Link } from '@tanstack/react-router'
import {
  ChevronDown,
  ChevronRight,
  Clock,
  Cpu,
  History,
  MessageSquare,
  Pencil,
  Send,
  User,
} from 'lucide-react'

import { Button } from '@/components/ui/button'
import { cn } from '@/lib/utils'
import type {
  DiscussionStatus,
  DiscussionSummary,
  ReplyEntry,
} from '@/lib/reader-types'
import {
  newIdempotencyKey,
  useAddReply,
  useDiscussionDetail,
  useDiscussionRevisions,
  useEditDiscussionBody,
  useEditReplyBody,
  useReaderActor,
  useSetDiscussionStatus,
} from '@/lib/reader-queries'
import { ScopeBadge, StatusBadge, TypeBadge } from './badges'

function formatWhen(value: string): string {
  const parsed = new Date(value)
  return Number.isNaN(parsed.getTime()) ? value : parsed.toLocaleString()
}

// One discussion thread: summary header, replies (on expand), and the
// §12.2 write controls — reply, status change (root author / admin, with
// mandatory reason), author-only body edits under If-Match CAS, and the
// revision/status history. All bodies render as plain text: comment
// content is untrusted data, never markup.
export function DiscussionThread({
  discussion,
  lang,
  paperId,
  startExpanded = false,
}: {
  discussion: DiscussionSummary
  lang: string
  paperId: string
  startExpanded?: boolean
}) {
  const { t } = useTranslation('reader')
  const actor = useReaderActor()
  const [expanded, setExpanded] = useState(startExpanded)
  const detail = useDiscussionDetail(expanded ? discussion.discussion_id : null)

  const summary = detail.data ?? discussion
  const canManage = actor.is_admin || summary.created_by === actor.id

  return (
    <li
      className="rounded-lg border border-border"
      data-testid="discussion-card"
      data-discussion-id={summary.discussion_id}
    >
      <button
        type="button"
        data-testid="discussion-toggle"
        aria-expanded={expanded}
        onClick={() => setExpanded((v) => !v)}
        className="flex w-full items-start gap-2 p-3 text-left"
      >
        {expanded ? (
          <ChevronDown className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        ) : (
          <ChevronRight className="mt-0.5 size-4 shrink-0 text-muted-foreground" />
        )}
        <span className="min-w-0 flex-1">
          <ThreadHeader summary={summary} />
        </span>
      </button>
      {expanded && (
        <div className="flex flex-col gap-3 border-t border-border p-3">
          <RootBody discussion={summary} />
          {(detail.data?.replies ?? []).map((reply) => (
            <ReplyItem key={reply.reply_id} reply={reply} discussionId={summary.discussion_id} />
          ))}
          {detail.isLoading && (
            <p role="status" className="text-sm text-muted-foreground">
              {t('thread.loadingDetail')}
            </p>
          )}
          <ReplyForm discussionId={summary.discussion_id} />
          {canManage && <StatusControl discussion={summary} />}
          <HistoryBlock discussionId={summary.discussion_id} />
          <Link
            to="/$lang/papers/$paperId/discussions"
            params={{ lang, paperId }}
            search={{ d: summary.discussion_id }}
            className="text-xs text-primary hover:underline"
          >
            {t('discussion.openThread')}
          </Link>
        </div>
      )}
    </li>
  )
}

function ThreadHeader({ summary }: { summary: DiscussionSummary }) {
  const { t } = useTranslation('reader')
  return (
    <>
      <span className="flex flex-wrap items-center gap-1.5">
        <StatusBadge status={summary.status} />
        <TypeBadge type={summary.type} />
        <ScopeBadge scope={summary.scope} />
        <span className="ml-auto inline-flex items-center gap-0.5 text-xs text-muted-foreground">
          <MessageSquare className="size-3" />
          <span className="tabular-nums">{summary.reply_count}</span>
          <span className="sr-only">{t('discussion.replies', { count: summary.reply_count })}</span>
        </span>
      </span>
      <span className="mt-1.5 block text-sm whitespace-pre-wrap break-words line-clamp-3">
        {summary.body}
      </span>
      <span className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-[11px] text-muted-foreground">
        <span className="inline-flex items-center gap-1">
          <User className="size-3" />
          {summary.created_by}
        </span>
        {summary.model && (
          <span className="inline-flex items-center gap-1" title={t('discussion.modelDeclared')}>
            <Cpu className="size-3" />
            {summary.model}
          </span>
        )}
        <span className="inline-flex items-center gap-1">
          <Clock className="size-3" />
          {formatWhen(summary.updated_at)}
        </span>
        <span className="font-mono" title={t('discussion.revisionTitle')}>
          r{summary.revision}
        </span>
      </span>
    </>
  )
}

// Root body with author-only edit under If-Match CAS (§12.2). A 409 shows
// an explicit conflict message — never silently overwriting a newer edit.
function RootBody({ discussion }: { discussion: DiscussionSummary }) {
  const { t } = useTranslation('reader')
  const actor = useReaderActor()
  const [editing, setEditing] = useState(false)
  const edit = useEditDiscussionBody()

  if (!editing) {
    return (
      <div className="relative rounded-md bg-muted/30 p-2.5">
        <p className="text-sm whitespace-pre-wrap break-words">{discussion.body}</p>
        {discussion.created_by === actor.id && (
          <Button
            variant="ghost"
            size="icon-xs"
            aria-label={t('thread.edit')}
            className="absolute top-1.5 right-1.5"
            onClick={() => setEditing(true)}
            data-testid="edit-discussion-body"
          >
            <Pencil className="size-3" />
          </Button>
        )}
      </div>
    )
  }
  return (
    <BodyEditForm
      initial={discussion.body}
      pending={edit.isPending}
      error={edit.error?.message ?? ''}
      onCancel={() => setEditing(false)}
      onSave={(body) =>
        edit.mutate(
          { discussionId: discussion.discussion_id, body, ifMatchRevision: discussion.revision },
          { onSuccess: () => setEditing(false) },
        )
      }
    />
  )
}

function BodyEditForm({
  initial,
  pending,
  error,
  onCancel,
  onSave,
}: {
  initial: string
  pending: boolean
  error: string
  onCancel: () => void
  onSave: (body: string) => void
}) {
  const { t } = useTranslation('reader')
  const [body, setBody] = useState(initial)
  return (
    <form
      className="flex flex-col gap-1.5"
      onSubmit={(e) => {
        e.preventDefault()
        if (body.trim()) onSave(body)
      }}
    >
      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        rows={4}
        className="w-full resize-y rounded-md border border-border bg-transparent p-2 text-sm"
        data-testid="body-edit-textarea"
      />
      {error.includes('409') && (
        <p role="alert" className="text-xs text-destructive" data-testid="edit-conflict">
          {t('thread.editConflict')}
        </p>
      )}
      {!error.includes('409') && error && (
        <p role="alert" className="text-xs text-destructive">
          {error}
        </p>
      )}
      <div className="flex justify-end gap-1.5">
        <Button type="button" variant="ghost" size="sm" onClick={onCancel}>
          {t('composer.cancel')}
        </Button>
        <Button type="submit" size="sm" disabled={pending || !body.trim()} data-testid="body-edit-save">
          {pending ? t('composer.submitting') : t('thread.save')}
        </Button>
      </div>
    </form>
  )
}

function ReplyItem({ reply, discussionId }: { reply: ReplyEntry; discussionId: string }) {
  const { t } = useTranslation('reader')
  const actor = useReaderActor()
  const [editing, setEditing] = useState(false)
  const edit = useEditReplyBody()

  return (
    <div className="rounded-md border border-border p-2.5" data-testid="reply-item">
      {editing ? (
        <BodyEditForm
          initial={reply.body}
          pending={edit.isPending}
          error={edit.error?.message ?? ''}
          onCancel={() => setEditing(false)}
          onSave={(body) =>
            edit.mutate(
              { discussionId, replyId: reply.reply_id, body, ifMatchRevision: reply.revision },
              { onSuccess: () => setEditing(false) },
            )
          }
        />
      ) : (
        <div className="relative">
          <p className="text-sm whitespace-pre-wrap break-words">{reply.body}</p>
          {reply.created_by === actor.id && (
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={t('thread.editReply')}
              className="absolute top-0 right-0"
              onClick={() => setEditing(true)}
              data-testid={`edit-reply-${reply.reply_id}`}
            >
              <Pencil className="size-3" />
            </Button>
          )}
        </div>
      )}
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
        <span className="font-mono" title={t('discussion.revisionTitle')}>
          r{reply.revision}
        </span>
      </p>
    </div>
  )
}

export function ReplyForm({ discussionId }: { discussionId: string }) {
  const { t } = useTranslation('reader')
  const [body, setBody] = useState('')
  const [model, setModel] = useState('')
  const [error, setError] = useState('')
  const reply = useAddReply()

  return (
    <form
      className="flex flex-col gap-1.5"
      data-testid="reply-form"
      onSubmit={(e) => {
        e.preventDefault()
        if (!body.trim()) return
        setError('')
        reply.mutate(
          {
            discussionId,
            idempotencyKey: newIdempotencyKey(),
            input: { body, model: model.trim() || null },
          },
          {
            onSuccess: () => {
              setBody('')
              setModel('')
            },
            onError: (e: Error) => setError(e.message),
          },
        )
      }}
    >
      <textarea
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder={t('thread.replyPlaceholder')}
        rows={2}
        className="w-full resize-y rounded-md border border-border bg-transparent p-2 text-sm"
        data-testid="reply-body"
      />
      <div className="flex flex-wrap items-center gap-2">
        <input
          value={model}
          onChange={(e) => setModel(e.target.value)}
          placeholder={t('composer.modelPlaceholder')}
          aria-label={t('composer.model')}
          className="h-8 w-44 rounded-md border border-border bg-transparent px-2 font-mono text-xs"
        />
        <Button
          type="submit"
          size="sm"
          className="ml-auto"
          disabled={reply.isPending || !body.trim()}
          data-testid="reply-submit"
        >
          <Send className="size-3.5" />
          {reply.isPending ? t('composer.submitting') : t('thread.replySubmit')}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-xs text-destructive" data-testid="reply-error">
          {error}
        </p>
      )}
    </form>
  )
}

// Root author or admin only (§12.1.1); a non-empty reason is mandatory.
function StatusControl({ discussion }: { discussion: DiscussionSummary }) {
  const { t } = useTranslation('reader')
  const [status, setStatus] = useState<DiscussionStatus | 'none'>(
    (discussion.status ?? 'none') as DiscussionStatus | 'none',
  )
  const [reason, setReason] = useState('')
  const [error, setError] = useState('')
  const mutate = useSetDiscussionStatus()

  return (
    <form
      className="flex flex-col gap-1.5 rounded-md border border-dashed border-border p-2.5"
      data-testid="status-control"
      onSubmit={(e) => {
        e.preventDefault()
        if (!reason.trim()) return
        setError('')
        mutate.mutate(
          {
            discussionId: discussion.discussion_id,
            status: status === 'none' ? null : status,
            reason,
          },
          {
            onSuccess: () => setReason(''),
            onError: (e: Error) => setError(e.message),
          },
        )
      }}
    >
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <span className="text-muted-foreground">{t('thread.statusChange')}</span>
        <select
          value={status}
          onChange={(e) => setStatus(e.target.value as DiscussionStatus | 'none')}
          data-testid="status-select"
          className="h-8 rounded-md border border-border bg-transparent px-2"
        >
          <option value="none">{t('filters.statusNone')}</option>
          <option value="pending">{t('status.pending')}</option>
          <option value="confirmed">{t('status.confirmed')}</option>
          <option value="retracted">{t('status.retracted')}</option>
        </select>
        <input
          value={reason}
          onChange={(e) => setReason(e.target.value)}
          placeholder={t('thread.statusReasonPlaceholder')}
          aria-label={t('thread.statusReason')}
          className="h-8 min-w-40 flex-1 rounded-md border border-border bg-transparent px-2 text-xs"
          data-testid="status-reason"
        />
        <Button type="submit" size="sm" disabled={mutate.isPending || !reason.trim()} data-testid="status-apply">
          {mutate.isPending ? t('composer.submitting') : t('thread.apply')}
        </Button>
      </div>
      {error && (
        <p role="alert" className="text-xs text-destructive" data-testid="status-error">
          {error}
        </p>
      )}
    </form>
  )
}

function HistoryBlock({ discussionId }: { discussionId: string }) {
  const { t } = useTranslation('reader')
  const [open, setOpen] = useState(false)
  const revisions = useDiscussionRevisions(discussionId, open)

  if (!open) {
    return (
      <Button variant="ghost" size="sm" onClick={() => setOpen(true)} data-testid="history-toggle">
        <History className="size-3.5" />
        {t('thread.history')}
      </Button>
    )
  }
  return (
    <div className="rounded-md bg-muted/30 p-2.5" data-testid="history-block">
      <button
        type="button"
        onClick={() => setOpen(false)}
        className="flex items-center gap-1.5 text-xs font-semibold text-muted-foreground"
      >
        <History className="size-3.5" />
        {t('thread.history')}
        <ChevronDown className="size-3" />
      </button>
      {revisions.isLoading && (
        <p role="status" className="mt-1.5 text-xs text-muted-foreground">
          {t('thread.historyLoading')}
        </p>
      )}
      <ol className="mt-1.5 flex flex-col gap-1.5">
        {(revisions.data?.events ?? [])
          .slice()
          .reverse()
          .map((event, i) => (
            <li key={i} className="text-xs" data-testid="history-event">
              <span
                className={cn(
                  'mr-1.5 rounded px-1 py-0.5 font-mono text-[10px]',
                  event.kind === 'status'
                    ? 'bg-amber-500/15 text-amber-700 dark:text-amber-400'
                    : event.kind === 'body'
                      ? 'bg-muted text-muted-foreground'
                      : 'bg-secondary text-secondary-foreground',
                )}
              >
                {event.kind}
              </span>
              {event.kind === 'created' && <span>{t('thread.eventCreated')}</span>}
              {event.kind === 'reply' && <span>{t('thread.eventReply')}</span>}
              {event.kind === 'status' && (
                <span>
                  {t(`status.${event.from ?? 'none'}`)} → {t(`status.${event.to ?? 'none'}`)}
                  ：{event.reason}
                </span>
              )}
              {event.kind === 'body' && (
                <span>
                  {t('thread.eventBody', { target: event.target })}:{' '}
                  <span className="font-mono text-[11px] break-all">
                    {event.old.slice(0, 80)}…
                  </span>
                </span>
              )}
              <span className="ml-1 text-muted-foreground">
                — {event.kind === 'body' ? event.editor : event.actor}, {formatWhen(event.created_at)}
              </span>
            </li>
          ))}
      </ol>
    </div>
  )
}
