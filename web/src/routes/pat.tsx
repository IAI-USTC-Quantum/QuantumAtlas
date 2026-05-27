// /pat — Personal Access Tokens management page.
//
// Modelled after GitHub's fine-grained Personal Access Tokens:
//   - mandatory expiration (max 1 year, no "never expires")
//   - explicit scope opt-in (default deny, empty checkbox state)
//   - one-shot plaintext reveal on create, never retrievable after
//   - hard-delete revocation with confirmation
//
// Talks to /api/pat (not the auto-generated PocketBase collection
// API) so the server can generate plaintext securely server-side and
// the response shape stays predictable independent of the underlying
// collection schema. Endpoints sit behind sessionGuard, NOT authGuard
// — only a real browser session can create / list / revoke PATs (a
// leaked PAT cannot mint more PATs).

import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { Clipboard, KeyRound, Plus, Shield, Trash2 } from 'lucide-react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { pb } from '@/lib/pb'
import { Panel } from '@/components/Panel'
import { PageHeader } from '@/components/PageHeader'
import { StatusBlock } from '@/components/StatusBlock'

export const Route = createFileRoute('/pat')({
  component: PATPage,
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

// Expiry presets shown in the SPA. GitHub uses 7/30/60/90/custom +
// no-expiration; we drop no-expiration entirely (server-enforced max
// 1 year) and offer the same common buckets + 365 as the explicit
// maximum so users don't have to type it.
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
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['pat-list'], queryFn: listPATs })
  const scopes = useQuery({ queryKey: ['pat-scopes'], queryFn: fetchScopes })

  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [expiresDays, setExpiresDays] = useState<number>(90)
  const [selectedScopes, setSelectedScopes] = useState<Set<string>>(new Set())
  const [issued, setIssued] = useState<PATCreateResponse | null>(null)
  const [copied, setCopied] = useState('')

  const createMutation = useMutation({
    mutationFn: createPAT,
    onSuccess: (data) => {
      setIssued(data)
      setName('')
      setDescription('')
      setExpiresDays(90)
      setSelectedScopes(new Set())
      qc.invalidateQueries({ queryKey: ['pat-list'] })
    },
  })

  const revokeMutation = useMutation({
    mutationFn: revokePAT,
    onSuccess: () => qc.invalidateQueries({ queryKey: ['pat-list'] }),
  })

  async function copy(text: string) {
    await navigator.clipboard.writeText(text)
    setCopied('Copied')
    window.setTimeout(() => setCopied(''), 2200)
  }

  function closeIssuedModal() {
    setIssued(null)
    setCopied('')
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

  return (
    <section className="stack">
      <PageHeader
        eyebrow="Authentication"
        title="Personal Access Tokens"
        copy="Long-lived bearer tokens for CLI, CI, and other automation. Each PAT carries an explicit scope set (default deny) and a mandatory expiry (max 365 days), modelled after GitHub fine-grained PATs."
      />

      <Panel
        title="Your tokens"
        icon={KeyRound}
        suffix={`${list.data?.length ?? 0}`}
      >
        <div className="actions" style={{ marginBottom: 12 }}>
          <button
            className="primary"
            type="button"
            onClick={() => setCreating(true)}
            disabled={!scopes.data}
          >
            <Plus size={17} /> New token
          </button>
          {(createMutation.error || revokeMutation.error || scopes.error) && (
            <span className="copy-state" style={{ color: '#a13b1f' }}>
              {(createMutation.error || revokeMutation.error || scopes.error)?.message}
            </span>
          )}
        </div>

        <StatusBlock
          loading={list.isLoading}
          error={list.error?.message ?? ''}
          empty={!list.data?.length}
        >
          <div className="list">
            {(list.data ?? []).map((token) => (
              <article key={token.id} className="panel" style={{ padding: 14 }}>
                <div className="panel-heading" style={{ marginBottom: 8 }}>
                  <div style={{ flex: 1 }}>
                    <strong>{token.name}</strong>
                    <p className="muted" style={{ margin: '4px 0 0', fontSize: 13 }}>
                      <code>{token.prefix}…</code>
                      {token.description && <> · {token.description}</>}
                    </p>
                    <div style={{ marginTop: 6, display: 'flex', gap: 6, flexWrap: 'wrap' }}>
                      {token.scopes.length === 0 && (
                        <span className="badge" style={{ background: '#fff2df', color: '#7a4b27' }}>
                          no scopes (can't call any write endpoint)
                        </span>
                      )}
                      {token.scopes.map((s) => (
                        <span key={s} className="badge"><Shield size={11} /> {s}</span>
                      ))}
                    </div>
                  </div>
                  <button
                    className="ghost small"
                    type="button"
                    disabled={revokeMutation.isPending}
                    onClick={() => {
                      if (window.confirm(`Revoke token "${token.name}"? This cannot be undone — any script using it will start getting 401 immediately.`)) {
                        revokeMutation.mutate(token.id)
                      }
                    }}
                  >
                    <Trash2 size={14} /> Revoke
                  </button>
                </div>
                <dl style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 8, fontSize: 12, margin: 0 }}>
                  <div><dt style={{ color: '#607169' }}>Created</dt><dd style={{ margin: 0 }}>{formatDate(token.created)}</dd></div>
                  <div><dt style={{ color: '#607169' }}>Expires</dt><dd style={{ margin: 0 }}>{formatDate(token.expires_at)}</dd></div>
                  <div><dt style={{ color: '#607169' }}>Last used</dt><dd style={{ margin: 0 }}>{token.last_used_at ? formatDate(token.last_used_at) : 'Never'}</dd></div>
                </dl>
              </article>
            ))}
          </div>
        </StatusBlock>
      </Panel>

      {creating && !issued && scopes.data && (
        <div className="pat-modal-backdrop" onClick={() => setCreating(false)}>
          <div className="pat-modal" onClick={(event) => event.stopPropagation()}>
            <h2 style={{ marginTop: 0 }}>New token</h2>
            <form onSubmit={onCreateSubmit} className="stack">
              <label className="field-row" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
                <span>Name (required)</span>
                <input
                  type="text"
                  value={name}
                  onChange={(event) => setName(event.target.value)}
                  required
                  maxLength={80}
                  placeholder="e.g. nightly-ci"
                />
              </label>
              <label className="field-row" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
                <span>Description (optional)</span>
                <input
                  type="text"
                  value={description}
                  onChange={(event) => setDescription(event.target.value)}
                  maxLength={200}
                  placeholder="e.g. used by GitHub Actions"
                />
              </label>
              <label className="field-row" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
                <span>Expires in (required, max {scopes.data.max_expiry_days} days)</span>
                <select
                  value={expiresDays}
                  onChange={(event) => setExpiresDays(Number(event.target.value))}
                  style={{ minHeight: 40, padding: '0 12px', border: '1px solid rgba(23, 32, 28, 0.16)', background: 'rgba(255, 255, 255, 0.78)' }}
                >
                  {EXPIRY_PRESETS.map((days) => (
                    <option key={days} value={days}>
                      {days} days ({describeExpiry(days)})
                    </option>
                  ))}
                </select>
              </label>
              <div className="field-row" style={{ flexDirection: 'column', alignItems: 'stretch' }}>
                <span>Scopes (default deny — explicitly check what this token may do)</span>
                <div style={{ display: 'grid', gap: 8, marginTop: 6 }}>
                  {scopes.data.scopes.map((scope) => (
                    <label key={scope.name} style={{ display: 'flex', alignItems: 'flex-start', gap: 8, fontSize: 13 }}>
                      <input
                        type="checkbox"
                        checked={selectedScopes.has(scope.name)}
                        onChange={() => toggleScope(scope.name)}
                        style={{ marginTop: 3 }}
                      />
                      <span>
                        <code>{scope.name}</code>
                        <div style={{ color: '#607169', marginTop: 2 }}>{scope.description}</div>
                      </span>
                    </label>
                  ))}
                </div>
                {selectedScopes.size === 0 && (
                  <p className="muted" style={{ fontSize: 12, marginTop: 8, color: '#7a4b27' }}>
                    With no scopes selected, this token will be rejected (403) by every write endpoint. You can still create it as a placeholder; revoke and recreate to grant scopes later.
                  </p>
                )}
              </div>
              <div className="actions">
                <button
                  type="submit"
                  className="primary"
                  disabled={createMutation.isPending || name.trim() === ''}
                >
                  Create
                </button>
                <button type="button" className="secondary" onClick={() => setCreating(false)}>
                  Cancel
                </button>
              </div>
            </form>
          </div>
        </div>
      )}

      {issued && (
        <div className="pat-modal-backdrop" onClick={closeIssuedModal}>
          <div className="pat-modal" onClick={(event) => event.stopPropagation()}>
            <h2 style={{ marginTop: 0 }}>Copy your token now</h2>
            <p style={{ color: '#a13b1f', fontWeight: 600 }}>
              This is the only time you will see this value. After closing
              this dialog the server only keeps a one-way hash — there is
              no way to recover the plaintext.
            </p>
            <div className="token-box" style={{ wordBreak: 'break-all', userSelect: 'all' }}>
              {issued.plaintext}
            </div>
            <div className="actions" style={{ marginTop: 12 }}>
              <button className="primary" type="button" onClick={() => copy(issued.plaintext)}>
                <Clipboard size={16} /> Copy token
              </button>
              <button
                className="secondary"
                type="button"
                onClick={() => copy(`export QATLAS_TOKEN=${issued.plaintext}`)}
              >
                <Clipboard size={16} /> Copy export
              </button>
              <span className="copy-state" aria-live="polite">{copied}</span>
            </div>
            <p className="muted" style={{ marginTop: 16 }}>
              Use as <code>Authorization: Bearer {issued.prefix}…</code> · expires {formatDate(issued.expires_at)}.
            </p>
            <div style={{ marginTop: 8, display: 'flex', gap: 6, flexWrap: 'wrap' }}>
              {issued.scopes.length === 0 && (
                <span className="badge" style={{ background: '#fff2df', color: '#7a4b27' }}>no scopes</span>
              )}
              {issued.scopes.map((s) => (
                <span key={s} className="badge"><Shield size={11} /> {s}</span>
              ))}
            </div>
            <div className="actions" style={{ marginTop: 12 }}>
              <button type="button" className="ghost" onClick={() => { closeIssuedModal(); setCreating(false) }}>
                I've copied it — close
              </button>
            </div>
          </div>
        </div>
      )}
    </section>
  )
}

// formatDate renders the server's PocketBase DateTime string
// ("2026-01-02 15:04:05.000Z") into a short locale-aware form. Falls
// back to the raw value if Date parsing fails.
function formatDate(value: string): string {
  if (!value) return ''
  const parsed = new Date(value.replace(' ', 'T'))
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString()
}

// describeExpiry gives the user a human time hint next to each
// preset, e.g. "30 days (≈ 1 month)".
function describeExpiry(days: number): string {
  if (days === 365) return '1 year max'
  if (days >= 90) return `≈ ${Math.round(days / 30)} months`
  if (days >= 30) return `≈ ${Math.round(days / 30)} month`
  return `${days} days`
}
