import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Plus } from 'lucide-react'

import { Button } from '@/components/ui/button'
import {
  MAX_BODY_CHARS,
  TYPE_SLUG,
} from '@/mocks/reader-mock-store'
import type { DiscussionScope, DiscussionStatus } from '@/lib/reader-types'
import { newIdempotencyKey, useCreateDiscussion } from '@/lib/reader-queries'
import { PRESET_TYPES } from './badges'

// New-discussion form for one exact anchor (revision + page + block).
// Bodies are plain text (untrusted data — never markup); the 20k budget
// (§12.2) is enforced client-side for feedback and by the store/server.
export function DiscussionComposer({
  paperId,
  parseRevision,
  anchor,
}: {
  paperId: string
  parseRevision: string
  anchor: { page_idx: number; block_index: number }
}) {
  const { t } = useTranslation('reader')
  const [open, setOpen] = useState(false)
  const [type, setType] = useState<string>('normal')
  const [customType, setCustomType] = useState('')
  const [scope, setScope] = useState<DiscussionScope>('public')
  const [status, setStatus] = useState<DiscussionStatus | 'none'>('pending')
  const [model, setModel] = useState('')
  const [body, setBody] = useState('')
  const [error, setError] = useState('')

  const create = useCreateDiscussion(paperId)

  const effectiveType = type === '__custom' ? customType : type
  const typeValid = TYPE_SLUG.test(effectiveType)
  const bodyValid = body.trim().length > 0 && [...body].length <= MAX_BODY_CHARS

  const submit = () => {
    setError('')
    create.mutate(
      {
        idempotencyKey: newIdempotencyKey(),
        input: {
          parse_revision: parseRevision,
          page_idx: anchor.page_idx,
          block_index: anchor.block_index,
          type: effectiveType,
          scope,
          status: status === 'none' ? null : status,
          body,
          model: model.trim() || null,
        },
      },
      {
        onSuccess: () => {
          setBody('')
          setModel('')
          setOpen(false)
        },
        onError: (e: Error) => setError(e.message),
      },
    )
  }

  if (!open) {
    return (
      <Button variant="outline" size="sm" onClick={() => setOpen(true)} data-testid="composer-toggle">
        <Plus className="size-3.5" />
        {t('composer.toggle')}
      </Button>
    )
  }

  return (
    <form
      className="flex flex-col gap-2 rounded-lg border border-border p-3"
      data-testid="discussion-composer"
      onSubmit={(e) => {
        e.preventDefault()
        if (typeValid && bodyValid) submit()
      }}
    >
      <div className="flex flex-wrap gap-2">
        <label className="flex items-center gap-1.5 text-xs">
          <span className="text-muted-foreground">{t('composer.type')}</span>
          <select
            data-testid="composer-type"
            value={type}
            onChange={(e) => setType(e.target.value)}
            className="h-8 rounded-md border border-border bg-transparent px-2"
          >
            {PRESET_TYPES.map((preset) => (
              <option key={preset} value={preset}>
                {t(`type.${preset}`)}
              </option>
            ))}
            <option value="__custom">{t('composer.typeCustom')}</option>
          </select>
        </label>
        {type === '__custom' && (
          <input
            data-testid="composer-custom-type"
            value={customType}
            onChange={(e) => setCustomType(e.target.value)}
            placeholder={t('composer.customPlaceholder')}
            aria-label={t('composer.customPlaceholder')}
            className="h-8 w-44 rounded-md border border-border bg-transparent px-2 font-mono text-xs"
          />
        )}
        <label className="flex items-center gap-1.5 text-xs">
          <span className="text-muted-foreground">{t('composer.scope')}</span>
          <select
            data-testid="composer-scope"
            value={scope}
            onChange={(e) => setScope(e.target.value as DiscussionScope)}
            className="h-8 rounded-md border border-border bg-transparent px-2"
          >
            <option value="public">{t('scope.public')}</option>
            <option value="lean">{t('scope.lean')}</option>
          </select>
        </label>
        <label className="flex items-center gap-1.5 text-xs">
          <span className="text-muted-foreground">{t('filters.status')}</span>
          <select
            data-testid="composer-status"
            value={status}
            onChange={(e) => setStatus(e.target.value as DiscussionStatus | 'none')}
            className="h-8 rounded-md border border-border bg-transparent px-2"
          >
            <option value="none">{t('filters.statusNone')}</option>
            <option value="pending">{t('status.pending')}</option>
            <option value="confirmed">{t('status.confirmed')}</option>
            <option value="retracted">{t('status.retracted')}</option>
          </select>
        </label>
      </div>
      <textarea
        data-testid="composer-body"
        value={body}
        onChange={(e) => setBody(e.target.value)}
        placeholder={t('composer.bodyPlaceholder')}
        rows={4}
        className="w-full resize-y rounded-md border border-border bg-transparent p-2 text-sm"
      />
      <div className="flex flex-wrap items-center gap-2">
        <input
          data-testid="composer-model"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          placeholder={t('composer.modelPlaceholder')}
          aria-label={t('composer.model')}
          className="h-8 w-48 rounded-md border border-border bg-transparent px-2 font-mono text-xs"
        />
        <span className="text-xs tabular-nums text-muted-foreground" data-testid="composer-count">
          {t('composer.charCount', { count: [...body].length, max: MAX_BODY_CHARS })}
        </span>
        <div className="ml-auto flex gap-1.5">
          <Button type="button" variant="ghost" size="sm" onClick={() => setOpen(false)}>
            {t('composer.cancel')}
          </Button>
          <Button
            type="submit"
            size="sm"
            disabled={create.isPending || !typeValid || !bodyValid}
            data-testid="composer-submit"
          >
            {create.isPending ? t('composer.submitting') : t('composer.submit')}
          </Button>
        </div>
      </div>
      {type === '__custom' && customType && !typeValid && (
        <p role="alert" className="text-xs text-destructive">
          {t('composer.typeInvalid')}
        </p>
      )}
      {error && (
        <p role="alert" className="text-xs text-destructive" data-testid="composer-error">
          {error}
        </p>
      )}
    </form>
  )
}
