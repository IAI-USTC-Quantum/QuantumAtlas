import { createFileRoute } from '@tanstack/react-router'
import { useEffect, useMemo, useRef, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import {
  Clipboard,
  KeyRound,
  Plus,
  Send,
  Shield,
  Terminal,
  Trash2,
} from 'lucide-react'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card, CardContent } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
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

// PATSearch carries the optional CLI-loopback hand-off parameters. The
// qatlas client's `auth login` (loopback flow) bounces the user to
// /<lang>/pat with these set so the SPA can:
//   1. show a "CLI requesting a token" consent banner,
//   2. pre-fill the create-token dialog with the suggested name,
//      scopes and expiry,
//   3. POST the freshly-minted plaintext back to the local
//      127.0.0.1:<port> server the CLI spun up.
//
// All five fields are optional; missing any (or supplying an
// untrustworthy cli_callback) collapses the UI back to the normal
// "create a token, show it once" UX.
type PATSearch = {
  cli_callback?: string
  cli_state?: string
  cli_name?: string
  cli_scopes?: string
  cli_expires_days?: string
}

export const Route = createFileRoute('/$lang/pat')({
  component: PATPage,
  validateSearch: (search: Record<string, unknown>): PATSearch => ({
    cli_callback:
      typeof search.cli_callback === 'string' ? search.cli_callback : undefined,
    cli_state:
      typeof search.cli_state === 'string' ? search.cli_state : undefined,
    cli_name: typeof search.cli_name === 'string' ? search.cli_name : undefined,
    cli_scopes:
      typeof search.cli_scopes === 'string' ? search.cli_scopes : undefined,
    cli_expires_days:
      typeof search.cli_expires_days === 'string'
        ? search.cli_expires_days
        : undefined,
  }),
})

type PATSummary = {
  id: string
  name: string
  prefix: string
  description?: string
  scopes: string[]
  expires_at: string
  last_used_at?: string
  created: string
}

type PATCreateResponse = {
  id: string
  name: string
  prefix: string
  plaintext: string
  description?: string
  scopes: string[]
  expires_at: string
  created: string
}

type ScopeInfo = { name: string; description: string }
type ScopesPayload = { scopes: ScopeInfo[]; max_expiry_days: number }

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
      const body = (await res.json()) as { detail?: string }
      if (body.detail) detail = body.detail
    } catch {
      // body wasn't JSON
    }
    throw new Error(detail)
  }
  return (await res.json()) as T
}

async function listPATs(): Promise<PATSummary[]> {
  const body = await fetchJSON<{ tokens?: PATSummary[] }>('/api/pat')
  return body.tokens ?? []
}

async function fetchScopes(): Promise<ScopesPayload> {
  return fetchJSON<ScopesPayload>('/api/pat/scopes')
}

async function createPAT(input: {
  name: string
  description?: string
  scopes: string[]
  expires_in_days: number
}): Promise<PATCreateResponse> {
  return fetchJSON<PATCreateResponse>('/api/pat', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
}

async function revokePAT(id: string): Promise<void> {
  await fetchJSON(`/api/pat/${encodeURIComponent(id)}`, { method: 'DELETE' })
}

function PATPage() {
  const { t } = useTranslation('pat')
  const { t: tc } = useTranslation('common')
  const qc = useQueryClient()
  const search = Route.useSearch()
  const list = useQuery({ queryKey: ['pat-list'], queryFn: listPATs })
  const scopes = useQuery({ queryKey: ['pat-scopes'], queryFn: fetchScopes })

  // Parse + sanity-check the CLI hand-off params exactly once per
  // mount. parseCLIRequest is intentionally strict: only a loopback
  // http://127.0.0.1:<port> URL is accepted, so a malicious link like
  // `/pat?cli_callback=https://attacker.example/` cannot trick the
  // user's browser into POSTing their plaintext PAT off-host.
  const cliRequest = useMemo(() => parseCLIRequest(search), [search])

  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [expiresDays, setExpiresDays] = useState<number>(90)
  const [selectedScopes, setSelectedScopes] = useState<Set<string>>(new Set())
  const [issued, setIssued] = useState<PATCreateResponse | null>(null)
  const [cliDelivered, setCLIDelivered] = useState(false)
  const [forceReveal, setForceReveal] = useState(false)
  const cliPrefilledRef = useRef(false)
  // When the CLI flow is active we deliver the plaintext to the
  // local loopback via form-POST navigation and SHOULD NOT auto-pop
  // the reveal dialog: the tab is about to navigate away anyway, and
  // flashing the secret in the SPA is unnecessary attack surface.
  // The dialog stays available as a manual fallback (see the
  // "delivered" banner's "didn't pick up?" link).
  const revealOpen = !!issued && (!cliRequest || forceReveal)

  // When the user lands here via the CLI flow, auto-open the create
  // dialog with all suggested values pre-filled. The ref guard makes
  // this idempotent — if the user dismisses the dialog we won't
  // re-open it on every re-render.
  useEffect(() => {
    if (!cliRequest) return
    if (cliPrefilledRef.current) return
    cliPrefilledRef.current = true
    setName(cliRequest.suggestedName)
    setDescription(cliRequest.description ?? '')
    setExpiresDays(cliRequest.expiresInDays ?? 90)
    setSelectedScopes(new Set(cliRequest.scopes))
    setCreating(true)
  }, [cliRequest])

  const createMutation = useMutation({
    mutationFn: createPAT,
    onSuccess: (data) => {
      setIssued(data)
      setCreating(false)
      setName('')
      setDescription('')
      setExpiresDays(90)
      setSelectedScopes(new Set())
      void qc.invalidateQueries({ queryKey: ['pat-list'] })
      // If the user reached this page through the CLI loopback flow,
      // hand the plaintext back to the local 127.0.0.1:<port> server
      // via a form-POST navigation (see deliverToCLI's comment for
      // why form-POST rather than fetch). The current tab navigates
      // to the CLI's success page; the issued-token reveal dialog
      // serves only as a fallback escape hatch (see below).
      if (cliRequest) {
        setCLIDelivered(true)
        deliverToCLI(cliRequest, data)
      }
    },
  })

  const revokeMutation = useMutation({
    mutationFn: revokePAT,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['pat-list'] }),
  })

  async function copy(text: string) {
    await navigator.clipboard.writeText(text)
    toast.success(t('copied'))
  }

  function toggleScope(scope: string) {
    setSelectedScopes((prev) => {
      const next = new Set(prev)
      if (next.has(scope)) next.delete(scope)
      else next.add(scope)
      return next
    })
  }

  function onCreateSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    if (!name.trim()) return
    if (!Number.isInteger(expiresDays) || expiresDays <= 0) return
    createMutation.mutate({
      name: name.trim(),
      description: description.trim() || undefined,
      scopes: Array.from(selectedScopes),
      expires_in_days: expiresDays,
    })
  }

  const errorMsg =
    createMutation.error?.message ??
    revokeMutation.error?.message ??
    scopes.error?.message ??
    ''

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />

      {cliRequest && !cliDelivered && (
        <Alert>
          <Terminal className="size-4" />
          <AlertTitle>{t('cliBanner.title')}</AlertTitle>
          <AlertDescription>
            {t('cliBanner.body', {
              callback: cliRequest.callback,
              name: cliRequest.suggestedName,
              scopes:
                cliRequest.scopes.length > 0
                  ? cliRequest.scopes.join(', ')
                  : t('cliBanner.noScopes'),
              days: cliRequest.expiresInDays,
            })}
          </AlertDescription>
        </Alert>
      )}

      {cliRequest && cliDelivered && (
        <Alert>
          <Send className="size-4" />
          <AlertTitle>{t('cliBanner.deliveredTitle')}</AlertTitle>
          <AlertDescription>
            {t('cliBanner.deliveredBody', { callback: cliRequest.callback })}
            {issued && (
              <Button
                type="button"
                variant="link"
                className="h-auto px-0 pl-2 align-baseline"
                onClick={() => setForceReveal(true)}
              >
                {t('cliBanner.fallbackReveal')}
              </Button>
            )}
          </AlertDescription>
        </Alert>
      )}

      {search.cli_callback && !cliRequest && (
        <Alert variant="destructive">
          <AlertTitle>{t('cliBanner.invalidTitle')}</AlertTitle>
          <AlertDescription>
            {t('cliBanner.invalidBody', { callback: search.cli_callback })}
          </AlertDescription>
        </Alert>
      )}

      <Panel
        title={t('yourTokens')}
        icon={KeyRound}
        suffix={`${list.data?.length ?? 0}`}
      >
        <div className="mb-3 flex flex-wrap items-center gap-3">
          <Button
            type="button"
            disabled={!scopes.data}
            onClick={() => setCreating(true)}
          >
            <Plus className="size-4" /> {t('newToken')}
          </Button>
          {errorMsg && (
            <span className="text-sm text-destructive">{errorMsg}</span>
          )}
        </div>

        <StatusBlock
          loading={list.isLoading}
          error={list.error?.message ?? ''}
          empty={!list.data?.length}
        >
          <div className="flex flex-col gap-3">
            {(list.data ?? []).map((token) => (
              <Card key={token.id}>
                <CardContent className="space-y-3 p-4">
                  <div className="flex items-start justify-between gap-3">
                    <div className="min-w-0 flex-1 space-y-1.5">
                      <strong className="block truncate text-base">
                        {token.name}
                      </strong>
                      <p className="truncate text-xs text-muted-foreground">
                        <code className="rounded bg-muted px-1 py-0.5">
                          {token.prefix}…
                        </code>
                        {token.description && <> · {token.description}</>}
                      </p>
                      <div className="flex flex-wrap gap-1.5">
                        {token.scopes.length === 0 && (
                          <Badge
                            variant="secondary"
                            className="border-amber-500/40 bg-amber-500/10 text-amber-900 dark:text-amber-200"
                          >
                            {t('noScopesBadge')}
                          </Badge>
                        )}
                        {token.scopes.map((s) => (
                          <Badge key={s} variant="outline">
                            <Shield className="size-3" /> {s}
                          </Badge>
                        ))}
                      </div>
                    </div>
                    <Button
                      type="button"
                      size="sm"
                      variant="ghost"
                      disabled={revokeMutation.isPending}
                      onClick={() => {
                        if (
                          window.confirm(
                            t('revokeConfirm', { name: token.name }),
                          )
                        ) {
                          revokeMutation.mutate(token.id)
                        }
                      }}
                    >
                      <Trash2 className="size-3.5" /> {tc('actions.revoke')}
                    </Button>
                  </div>
                  <dl className="grid grid-cols-3 gap-2 text-xs text-muted-foreground">
                    <div>
                      <dt>{t('tokenMeta.created')}</dt>
                      <dd className="mt-0.5 text-foreground">
                        {formatDate(token.created)}
                      </dd>
                    </div>
                    <div>
                      <dt>{t('tokenMeta.expiresAt')}</dt>
                      <dd className="mt-0.5 text-foreground">
                        {formatDate(token.expires_at)}
                      </dd>
                    </div>
                    <div>
                      <dt>{t('tokenMeta.lastUsed')}</dt>
                      <dd className="mt-0.5 text-foreground">
                        {token.last_used_at
                          ? formatDate(token.last_used_at)
                          : t('tokenMeta.never')}
                      </dd>
                    </div>
                  </dl>
                </CardContent>
              </Card>
            ))}
          </div>
        </StatusBlock>
      </Panel>

      {/* Create-token dialog */}
      <Dialog
        open={creating && !issued}
        onOpenChange={(open) => !open && setCreating(false)}
      >
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t('createTitle')}</DialogTitle>
          </DialogHeader>
          <form onSubmit={onCreateSubmit} className="space-y-4">
            <div className="space-y-1.5">
              <Label htmlFor="pat-name">{t('fields.name')}</Label>
              <Input
                id="pat-name"
                value={name}
                onChange={(event) => setName(event.target.value)}
                required
                maxLength={80}
                placeholder={t('fields.namePlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="pat-desc">{t('fields.description')}</Label>
              <Input
                id="pat-desc"
                value={description}
                onChange={(event) => setDescription(event.target.value)}
                maxLength={200}
                placeholder={t('fields.descriptionPlaceholder')}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="pat-expires">
                {t('fields.expires', {
                  max: scopes.data?.max_expiry_days ?? 365,
                })}
              </Label>
              <Select
                value={String(expiresDays)}
                onValueChange={(value) => setExpiresDays(Number(value))}
              >
                <SelectTrigger id="pat-expires">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {EXPIRY_PRESETS.map((days) => (
                    <SelectItem key={days} value={String(days)}>
                      {describeExpiry(days, t)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>{t('fields.scopes')}</Label>
              <div className="grid gap-2">
                {scopes.data?.scopes.map((scope) => (
                  <label
                    key={scope.name}
                    className="flex cursor-pointer items-start gap-3 rounded-md border border-border bg-muted/30 p-2.5 text-sm hover:bg-muted/60"
                  >
                    <input
                      type="checkbox"
                      checked={selectedScopes.has(scope.name)}
                      onChange={() => toggleScope(scope.name)}
                      className="mt-1 size-4 accent-primary"
                    />
                    <span className="min-w-0 flex-1">
                      <code className="font-semibold">{scope.name}</code>
                      <span className="mt-0.5 block text-xs text-muted-foreground">
                        {scope.description}
                      </span>
                    </span>
                  </label>
                ))}
              </div>
              {selectedScopes.size === 0 && (
                <Alert variant="destructive">
                  <AlertDescription>{t('noScopesWarning')}</AlertDescription>
                </Alert>
              )}
            </div>
            <DialogFooter>
              <Button
                type="button"
                variant="ghost"
                onClick={() => setCreating(false)}
              >
                {tc('actions.cancel')}
              </Button>
              <Button
                type="submit"
                disabled={createMutation.isPending || name.trim() === ''}
              >
                {tc('actions.create')}
              </Button>
            </DialogFooter>
          </form>
        </DialogContent>
      </Dialog>

      {/* Issued-token reveal dialog */}
      <Dialog
        open={revealOpen}
        onOpenChange={(open) => {
          if (open) return
          setIssued(null)
          setForceReveal(false)
        }}
      >
        <DialogContent className="max-w-lg">
          <DialogHeader>
            <DialogTitle>{t('issued.title')}</DialogTitle>
            <DialogDescription className="font-medium text-destructive">
              {t('issued.warning')}
            </DialogDescription>
          </DialogHeader>
          {issued && (
            <div className="space-y-3">
              <div className="break-all rounded-md border border-border bg-muted/60 px-3 py-2.5 font-mono text-xs">
                {issued.plaintext}
              </div>
              <div className="flex flex-wrap gap-2">
                <Button type="button" onClick={() => copy(issued.plaintext)}>
                  <Clipboard className="size-4" /> {t('issued.copyToken')}
                </Button>
                <Button
                  type="button"
                  variant="secondary"
                  onClick={() => copy(`export QATLAS_TOKEN=${issued.plaintext}`)}
                >
                  <Clipboard className="size-4" /> {t('issued.copyExport')}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                {t('issued.use', {
                  prefix: issued.prefix,
                  expiresAt: formatDate(issued.expires_at),
                })}
              </p>
              <div className="flex flex-wrap gap-1.5">
                {issued.scopes.length === 0 && (
                  <Badge
                    variant="secondary"
                    className="border-amber-500/40 bg-amber-500/10 text-amber-900 dark:text-amber-200"
                  >
                    {t('noScopesBadge')}
                  </Badge>
                )}
                {issued.scopes.map((s) => (
                  <Badge key={s} variant="outline">
                    <Shield className="size-3" /> {s}
                  </Badge>
                ))}
              </div>
            </div>
          )}
          <DialogFooter>
            <Button variant="ghost" onClick={() => setIssued(null)}>
              {t('issued.closeHint')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </section>
  )
}

function formatDate(value: string): string {
  if (!value) return ''
  const parsed = new Date(value.replace(' ', 'T'))
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString()
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

type CLIRequest = {
  callback: string
  state: string
  suggestedName: string
  description?: string
  scopes: string[]
  expiresInDays: number
}

// parseCLIRequest is the *only* place we decide a CLI hand-off is
// trustworthy. The constraints:
//
//   - cli_callback parses as a URL
//   - scheme is exactly 'http:' (NOT 'https:': real loopback servers
//     don't have a cert; an https URL implies a remote attacker)
//   - hostname is exactly '127.0.0.1' (NOT 'localhost'; resolvers
//     differ across OSes and 'localhost' could leak via /etc/hosts)
//   - port is present (no empty port = default-80 attack)
//   - cli_state is non-empty (so we have something to round-trip
//     and the local server can pin its expected value)
//
// Anything missing or mis-shaped → return null and the page falls
// back to the normal "create-a-token, show it once" UX.
function parseCLIRequest(search: PATSearch): CLIRequest | null {
  if (!search.cli_callback || !search.cli_state) return null
  let url: URL
  try {
    url = new URL(search.cli_callback)
  } catch {
    return null
  }
  if (url.protocol !== 'http:') return null
  if (url.hostname !== '127.0.0.1') return null
  if (!url.port) return null
  const scopes = (search.cli_scopes ?? '')
    .split(',')
    .map((s) => s.trim())
    .filter((s) => s.length > 0)
  const daysRaw = Number(search.cli_expires_days ?? '90')
  const expiresInDays =
    Number.isFinite(daysRaw) && daysRaw >= 1 && daysRaw <= 365
      ? Math.floor(daysRaw)
      : 90
  return {
    callback: url.toString(),
    state: search.cli_state,
    suggestedName: search.cli_name ?? 'qatlas-cli',
    scopes,
    expiresInDays,
  }
}

// deliverToCLI hands the freshly-minted plaintext PAT back to the
// local 127.0.0.1:<port> server the qatlas client spun up.
//
// Why form-POST navigation (not fetch())?
//
//   - Top-level form navigations are exempt from CORS — they were
//     allowed cross-origin before CORS existed and that has not
//     changed. fetch() with mode:"no-cors" returns an opaque response
//     so the SPA can't tell if the delivery actually succeeded; the
//     user just sees the SPA sitting on /pat with no clear feedback.
//   - Form POST navigates the current tab to the loopback URL. The
//     local Python server returns a full HTML success page, so the
//     user sees a real "✓ logged in, you can close this tab" page
//     and not a SPA-side "I tried" toast.
//   - Token plaintext lives in the request body (not the URL), so it
//     never enters browser history / referer headers / server logs
//     that index URLs. (gh auth login -w uses GET; we use POST
//     precisely to avoid this.)
function deliverToCLI(req: CLIRequest, data: PATCreateResponse): void {
  const form = document.createElement('form')
  form.method = 'POST'
  form.action = req.callback
  form.target = '_self'
  form.enctype = 'application/x-www-form-urlencoded'
  form.style.display = 'none'
  const fields: Record<string, string> = {
    state: req.state,
    token: data.plaintext,
    name: data.name,
    prefix: data.prefix,
    scopes: data.scopes.join(','),
    expires_at: data.expires_at,
  }
  for (const [key, value] of Object.entries(fields)) {
    const input = document.createElement('input')
    input.type = 'hidden'
    input.name = key
    input.value = value
    form.appendChild(input)
  }
  document.body.appendChild(form)
  form.submit()
}
