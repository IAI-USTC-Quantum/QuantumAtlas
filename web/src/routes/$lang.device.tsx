import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Check,
  CircleCheck,
  CircleX,
  Loader2,
  Terminal,
  X,
} from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { pb } from '@/lib/pb'

// DeviceSearch carries the optional user_code that the CLI's
// verification_uri_complete deep-links with. When absent we render
// an input box and let the user paste / type the 8-char code they
// were shown in their terminal (formatted XXXX-XXXX).
type DeviceSearch = {
  user_code?: string
}

export const Route = createFileRoute('/$lang/device')({
  component: DevicePage,
  validateSearch: (search: Record<string, unknown>): DeviceSearch => ({
    user_code:
      typeof search.user_code === 'string' ? search.user_code : undefined,
  }),
})

type LookupResponse = {
  user_code: string
  name: string
  description?: string
  scopes: string[]
  expires_in_days: number
  status: 'pending' | 'approved' | 'consumed' | 'denied' | 'expired'
  available_scopes: string[]
  scope_descriptions: Record<string, string>
  max_expiry_days: number
}

type ApproveOverrides = {
  user_code: string
  name?: string
  scopes?: string[]
  expires_in_days?: number
}

type ApproveResponse = {
  status: 'approved'
  user_code: string
}

type DenyResponse = {
  status: 'denied'
  user_code: string
}

const EXPIRY_PRESETS = [7, 30, 60, 90, 365] as const

function authHeader(): Record<string, string> {
  const token = pb.authStore.token
  return token ? { Authorization: `Bearer ${token}` } : {}
}

async function fetchJSON<T>(url: string, init?: RequestInit): Promise<T> {
  const res = await fetch(url, {
    ...init,
    headers: { ...authHeader(), ...(init?.headers ?? {}) },
  })
  if (!res.ok) {
    let detail = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { detail?: string; error?: string }
      detail = body.detail ?? body.error ?? detail
    } catch {
      // body wasn't JSON
    }
    throw new Error(detail)
  }
  return (await res.json()) as T
}

async function lookupDeviceCode(userCode: string): Promise<LookupResponse> {
  return fetchJSON<LookupResponse>(
    `/api/oauth/device/code?user_code=${encodeURIComponent(userCode)}`,
  )
}

async function approveDeviceCode(
  overrides: ApproveOverrides,
): Promise<ApproveResponse> {
  return fetchJSON<ApproveResponse>('/api/oauth/device/approve', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(overrides),
  })
}

async function denyDeviceCode(userCode: string): Promise<DenyResponse> {
  return fetchJSON<DenyResponse>('/api/oauth/device/deny', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user_code: userCode }),
  })
}

// Loose normalization for what the user types into the manual input
// box. The server's NormalizeUserCode is the canonical validator;
// we just upper-case + strip whitespace so a paste of "wdjb mjht"
// or "wdjb-mjht" reaches the server in a recognizable shape.
function normalizeUserCode(input: string): string {
  return input.trim().toUpperCase().replace(/\s+/g, '')
}

function describeExpiry(
  days: number,
  t: (key: string, opts?: Record<string, unknown>) => string,
): string {
  if (days === 365) return t('expiry.year', { count: 1 })
  if (days >= 90) return t('expiry.months', { count: Math.round(days / 30) })
  if (days >= 30) return t('expiry.month', { count: Math.round(days / 30) })
  return t('expiry.days', { count: days })
}

function DevicePage() {
  const { t } = useTranslation('pat')
  const { t: tc } = useTranslation('common')
  const search = Route.useSearch()
  const initialCode = normalizeUserCode(search.user_code ?? '')

  const [code, setCode] = useState(initialCode)
  const [submittedCode, setSubmittedCode] = useState<string>(
    initialCode || '',
  )
  const [terminalState, setTerminalState] = useState<
    'approved' | 'denied' | null
  >(null)

  useEffect(() => {
    const next = normalizeUserCode(search.user_code ?? '')
    if (next && next !== submittedCode) {
      setCode(next)
      setSubmittedCode(next)
      setTerminalState(null)
    }
  }, [search.user_code, submittedCode])

  const lookup = useQuery({
    queryKey: ['device-lookup', submittedCode],
    queryFn: () => lookupDeviceCode(submittedCode),
    enabled: submittedCode.length > 0,
    retry: false,
  })

  const approveMutation = useMutation({
    mutationFn: approveDeviceCode,
    onSuccess: () => setTerminalState('approved'),
  })
  const denyMutation = useMutation({
    mutationFn: denyDeviceCode,
    onSuccess: () => setTerminalState('denied'),
  })

  function onLookupSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const norm = normalizeUserCode(code)
    if (!norm) return
    setSubmittedCode(norm)
    setTerminalState(null)
  }

  const errorMsg =
    approveMutation.error?.message ??
    denyMutation.error?.message ??
    (lookup.error instanceof Error ? lookup.error.message : '')

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('device.eyebrow')}
        title={t('device.title')}
        copy={t('device.subtitle')}
      />

      {!submittedCode && (
        <Panel title={t('device.enterCode')} icon={Terminal}>
          <form onSubmit={onLookupSubmit} className="space-y-3">
            <div className="space-y-1.5">
              <Label htmlFor="device-code-input">
                {t('device.codeLabel')}
              </Label>
              <Input
                id="device-code-input"
                value={code}
                onChange={(event) => setCode(event.target.value)}
                placeholder="WDJB-MJHT"
                autoFocus
                autoComplete="off"
                spellCheck={false}
                className="font-mono uppercase tracking-[0.18em]"
                maxLength={32}
              />
            </div>
            <Button type="submit" disabled={normalizeUserCode(code) === ''}>
              {t('device.lookup')}
            </Button>
          </form>
        </Panel>
      )}

      {submittedCode && (
        <Panel title={t('device.confirm')} icon={Terminal}>
          <StatusBlock
            loading={lookup.isLoading}
            error={lookup.isError ? errorMsg : ''}
            empty={!lookup.data}
          >
            {lookup.data && (
              <DeviceConfirmation
                info={lookup.data}
                terminalState={terminalState}
                approving={approveMutation.isPending}
                denying={denyMutation.isPending}
                onApprove={(overrides) =>
                  approveMutation.mutate({
                    user_code: lookup.data!.user_code,
                    ...overrides,
                  })
                }
                onDeny={() => denyMutation.mutate(lookup.data!.user_code)}
                onTryAnother={() => {
                  setSubmittedCode('')
                  setCode('')
                  setTerminalState(null)
                  approveMutation.reset()
                  denyMutation.reset()
                }}
                errorMsg={errorMsg}
                tc={tc}
                t={t}
              />
            )}
          </StatusBlock>
        </Panel>
      )}
    </section>
  )
}

function DeviceConfirmation(props: {
  info: LookupResponse
  terminalState: 'approved' | 'denied' | null
  approving: boolean
  denying: boolean
  onApprove: (overrides: Omit<ApproveOverrides, 'user_code'>) => void
  onDeny: () => void
  onTryAnother: () => void
  errorMsg: string
  tc: (key: string) => string
  t: (key: string, opts?: Record<string, unknown>) => string
}) {
  const {
    info,
    terminalState,
    approving,
    denying,
    onApprove,
    onDeny,
    onTryAnother,
    errorMsg,
    tc,
    t,
  } = props

  // The user can edit name / scopes / expiry before approving. We
  // initialize from the CLI-seeded values, except scopes: if the CLI
  // did not pin any, default-check everything available so the user's
  // first instinct ("I want a CLI token that does everything I can
  // do") is the path of least resistance. Users who want to narrow
  // simply uncheck.
  const initialScopeSet = useMemo(() => {
    if (info.scopes.length > 0) return new Set(info.scopes)
    return new Set(info.available_scopes)
  }, [info.scopes, info.available_scopes])

  const [editedName, setEditedName] = useState(info.name)
  const [editedScopes, setEditedScopes] = useState<Set<string>>(initialScopeSet)
  const [editedDays, setEditedDays] = useState<number>(info.expires_in_days)

  // Re-sync if the underlying record updates while we're looking at it
  // (e.g. the SPA re-fetches after the tab regains focus). We don't
  // want to clobber user edits, so we only sync when the user hasn't
  // started typing. Cheap proxy: name still matches the server value.
  useEffect(() => {
    setEditedName(info.name)
    setEditedScopes(initialScopeSet)
    setEditedDays(info.expires_in_days)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [info.user_code])

  if (terminalState === 'approved') {
    return (
      <Alert>
        <CircleCheck className="size-4" />
        <AlertTitle>{t('device.approvedTitle')}</AlertTitle>
        <AlertDescription>{t('device.approvedBody')}</AlertDescription>
      </Alert>
    )
  }

  if (terminalState === 'denied') {
    return (
      <Alert>
        <CircleX className="size-4" />
        <AlertTitle>{t('device.deniedTitle')}</AlertTitle>
        <AlertDescription>{t('device.deniedBody')}</AlertDescription>
      </Alert>
    )
  }

  // Server has already rolled the row to a non-pending state. The
  // user shouldn't be able to act on it.
  if (info.status !== 'pending') {
    return (
      <Alert variant={info.status === 'denied' ? 'destructive' : undefined}>
        <AlertTitle>{t(`device.status.${info.status}.title`)}</AlertTitle>
        <AlertDescription>
          {t(`device.status.${info.status}.body`)}
          <Button
            type="button"
            variant="link"
            className="h-auto px-0 pl-2 align-baseline"
            onClick={onTryAnother}
          >
            {t('device.tryAnother')}
          </Button>
        </AlertDescription>
      </Alert>
    )
  }

  function toggleScope(scope: string) {
    setEditedScopes((prev) => {
      const next = new Set(prev)
      if (next.has(scope)) next.delete(scope)
      else next.add(scope)
      return next
    })
  }

  function onSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const trimmedName = editedName.trim()
    if (!trimmedName) return
    if (!Number.isInteger(editedDays) || editedDays <= 0) return

    // Only send fields the user actually changed. This keeps the
    // payload minimal and lets server-side defaults remain authoritative
    // for untouched values.
    const overrides: Omit<ApproveOverrides, 'user_code'> = {}
    if (trimmedName !== info.name) overrides.name = trimmedName
    if (editedDays !== info.expires_in_days) overrides.expires_in_days = editedDays
    const sortedEdited = Array.from(editedScopes).sort()
    const sortedSeeded = [...info.scopes].sort()
    if (
      sortedEdited.length !== sortedSeeded.length ||
      sortedEdited.some((s, i) => s !== sortedSeeded[i])
    ) {
      overrides.scopes = sortedEdited
    }
    onApprove(overrides)
  }

  const max = info.max_expiry_days || 365
  const presets = EXPIRY_PRESETS.filter((d) => d <= max)
  if (!presets.includes(editedDays as (typeof EXPIRY_PRESETS)[number])) {
    presets.push(editedDays as (typeof EXPIRY_PRESETS)[number])
  }
  presets.sort((a, b) => a - b)

  return (
    <form onSubmit={onSubmit} className="space-y-4">
      <div className="rounded-md border border-border bg-muted/40 p-4 text-sm">
        <div className="mb-3 font-mono text-lg font-semibold tracking-[0.18em]">
          {info.user_code}
        </div>
        {info.description && (
          <p className="mb-3 text-xs text-muted-foreground">
            {info.description}
          </p>
        )}

        <div className="space-y-3">
          <div className="space-y-1.5">
            <Label htmlFor="device-name">{t('device.tokenName')}</Label>
            <Input
              id="device-name"
              value={editedName}
              onChange={(event) => setEditedName(event.target.value)}
              maxLength={80}
              required
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="device-expires">
              {t('device.tokenExpires')}
              <span className="ml-1 text-xs text-muted-foreground">
                ({t('fields.expiresMax', { max })})
              </span>
            </Label>
            <Select
              value={String(editedDays)}
              onValueChange={(value) => setEditedDays(Number(value))}
            >
              <SelectTrigger id="device-expires">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {presets.map((days) => (
                  <SelectItem key={days} value={String(days)}>
                    {describeExpiry(days, t)}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>

          <div className="space-y-2">
            <Label>{t('device.tokenScopes')}</Label>
            <div className="grid gap-2">
              {info.available_scopes.map((scope) => (
                <label
                  key={scope}
                  className="flex cursor-pointer items-start gap-3 rounded-md border border-border bg-background/40 p-2.5 text-sm hover:bg-background/70"
                >
                  <input
                    type="checkbox"
                    checked={editedScopes.has(scope)}
                    onChange={() => toggleScope(scope)}
                    className="mt-1 size-4 accent-primary"
                  />
                  <span className="min-w-0 flex-1">
                    <code className="font-semibold">{scope}</code>
                    {info.scope_descriptions[scope] && (
                      <span className="mt-0.5 block text-xs text-muted-foreground">
                        {info.scope_descriptions[scope]}
                      </span>
                    )}
                  </span>
                </label>
              ))}
            </div>
            {editedScopes.size === 0 && (
              <Alert variant="destructive">
                <AlertDescription>{t('noScopesWarning')}</AlertDescription>
              </Alert>
            )}
          </div>
        </div>
      </div>

      <Alert>
        <AlertDescription>{t('device.consent')}</AlertDescription>
      </Alert>

      {errorMsg && (
        <Alert variant="destructive">
          <AlertDescription>{errorMsg}</AlertDescription>
        </Alert>
      )}

      <div className="flex flex-wrap gap-2">
        <Button
          type="submit"
          disabled={
            approving ||
            denying ||
            editedName.trim() === '' ||
            !Number.isInteger(editedDays) ||
            editedDays <= 0
          }
        >
          {approving ? (
            <Loader2 className="size-4 animate-spin" />
          ) : (
            <Check className="size-4" />
          )}
          {t('device.approve')}
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={onDeny}
          disabled={approving || denying}
        >
          {denying ? (
            <Loader2 className="size-4 animate-spin" />
          ) : (
            <X className="size-4" />
          )}
          {t('device.deny')}
        </Button>
        <Button
          type="button"
          variant="ghost"
          onClick={onTryAnother}
          disabled={approving || denying}
        >
          {tc('actions.cancel')}
        </Button>
      </div>
    </form>
  )
}
