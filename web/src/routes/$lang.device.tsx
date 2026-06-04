import { createFileRoute } from '@tanstack/react-router'
import { useMutation, useQuery } from '@tanstack/react-query'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  Check,
  CircleCheck,
  CircleX,
  Loader2,
  Shield,
  Terminal,
  X,
} from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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
}

type ApproveResponse = {
  status: 'approved'
  user_code: string
}

type DenyResponse = {
  status: 'denied'
  user_code: string
}

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

async function approveDeviceCode(userCode: string): Promise<ApproveResponse> {
  return fetchJSON<ApproveResponse>('/api/oauth/device/approve', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ user_code: userCode }),
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
                onApprove={() => approveMutation.mutate(lookup.data!.user_code)}
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
  onApprove: () => void
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

  return (
    <div className="space-y-4">
      <div className="rounded-md border border-border bg-muted/40 p-4 text-sm">
        <div className="mb-2 font-mono text-lg font-semibold tracking-[0.18em]">
          {info.user_code}
        </div>
        <dl className="grid gap-2 text-sm sm:grid-cols-[auto,1fr] sm:gap-x-4">
          <dt className="text-muted-foreground">{t('device.tokenName')}</dt>
          <dd className="font-medium">{info.name}</dd>
          {info.description && (
            <>
              <dt className="text-muted-foreground">
                {t('device.tokenDescription')}
              </dt>
              <dd>{info.description}</dd>
            </>
          )}
          <dt className="text-muted-foreground">{t('device.tokenScopes')}</dt>
          <dd>
            <div className="flex flex-wrap gap-1.5">
              {info.scopes.length === 0 && (
                <Badge variant="secondary">{t('noScopesBadge')}</Badge>
              )}
              {info.scopes.map((s) => (
                <Badge key={s} variant="outline">
                  <Shield className="size-3" /> {s}
                </Badge>
              ))}
            </div>
          </dd>
          <dt className="text-muted-foreground">{t('device.tokenExpires')}</dt>
          <dd>{t('device.expiresIn', { count: info.expires_in_days })}</dd>
        </dl>
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
          type="button"
          onClick={onApprove}
          disabled={approving || denying}
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
    </div>
  )
}
