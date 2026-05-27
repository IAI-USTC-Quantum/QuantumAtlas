// /pat — Personal Access Tokens management page.
//
// Mirrors the authentik "User Settings → Tokens" experience: list of
// the signed-in user's PATs (no plaintext, no hash), one-shot create
// flow that surfaces the plaintext exactly once in a modal, hard-
// delete via per-row "Revoke" with confirmation.
//
// Talks to /api/pat (not the auto-generated PocketBase collection API)
// so the server can generate plaintext securely server-side and we get
// a predictable response shape independent of the collection schema.

import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { Clipboard, KeyRound, Plus, Trash2 } from 'lucide-react'
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
  expires_at?: string
  last_used_at?: string
  created: string
}

type PATCreateResponse = {
  id: string
  name: string
  prefix: string
  plaintext: string
  description?: string
  expires_at?: string
  created: string
}

// authHeader feeds the bearer token from pb.authStore into every fetch.
// We can't go through PocketBase's collection-API client here because
// these endpoints are custom (POST /api/pat creates the secret server-
// side; the generic collection API would never see the plaintext).
function authHeader(): Record<string, string> {
  const token = pb.authStore.token
  return token ? { Authorization: `Bearer ${token}` } : {}
}

async function listPATs(): Promise<PATSummary[]> {
  const res = await fetch('/api/pat', { headers: { ...authHeader() } })
  if (!res.ok) throw new Error(`${res.status} ${res.statusText}`)
  const body = (await res.json()) as { tokens?: PATSummary[] }
  return body.tokens ?? []
}

async function createPAT(input: {
  name: string
  description?: string
  expires_in_days?: number
}): Promise<PATCreateResponse> {
  const res = await fetch('/api/pat', {
    method: 'POST',
    headers: { ...authHeader(), 'Content-Type': 'application/json' },
    body: JSON.stringify(input),
  })
  if (!res.ok) {
    let detail = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { detail?: string }
      if (body.detail) detail = body.detail
    } catch {
      // body wasn't JSON; keep the status text
    }
    throw new Error(detail)
  }
  return (await res.json()) as PATCreateResponse
}

async function revokePAT(id: string): Promise<void> {
  const res = await fetch(`/api/pat/${encodeURIComponent(id)}`, {
    method: 'DELETE',
    headers: { ...authHeader() },
  })
  if (!res.ok) {
    let detail = `${res.status} ${res.statusText}`
    try {
      const body = (await res.json()) as { detail?: string }
      if (body.detail) detail = body.detail
    } catch {
      // body wasn't JSON; keep the status text
    }
    throw new Error(detail)
  }
}

function PATPage() {
  const qc = useQueryClient()
  const list = useQuery({ queryKey: ['pat-list'], queryFn: listPATs })

  // Modal state — open/close + draft form + the one-shot plaintext we
  // show after a successful create. The plaintext is held only in this
  // component's state and cleared on modal close.
  const [creating, setCreating] = useState(false)
  const [name, setName] = useState('')
  const [description, setDescription] = useState('')
  const [expiresDays, setExpiresDays] = useState<string>('') // empty = never
  const [issued, setIssued] = useState<PATCreateResponse | null>(null)
  const [copied, setCopied] = useState('')

  const createMutation = useMutation({
    mutationFn: createPAT,
    onSuccess: (data) => {
      setIssued(data)
      setName('')
      setDescription('')
      setExpiresDays('')
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

  function onCreateSubmit(event: React.FormEvent<HTMLFormElement>) {
    event.preventDefault()
    const days = expiresDays.trim() === '' ? 0 : Number(expiresDays)
    if (Number.isNaN(days) || days < 0) {
      return
    }
    createMutation.mutate({
      name: name.trim(),
      description: description.trim() || undefined,
      expires_in_days: days || undefined,
    })
  }

  return (
    <section className="stack">
      <PageHeader
        eyebrow="Authentication"
        title="Personal Access Tokens"
        copy="Long-lived bearer tokens for CLI, CI, and other automation. PocketBase user sessions only last 14 days; PATs let you set your own expiry (or none)."
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
          >
            <Plus size={17} /> New token
          </button>
          {createMutation.error && (
            <span className="copy-state" style={{ color: '#a13b1f' }}>
              {createMutation.error.message}
            </span>
          )}
          {revokeMutation.error && (
            <span className="copy-state" style={{ color: '#a13b1f' }}>
              {revokeMutation.error.message}
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
                  <div>
                    <strong>{token.name}</strong>
                    <p className="muted" style={{ margin: '4px 0 0', fontSize: 13 }}>
                      <code>{token.prefix}…</code>
                      {token.description && <> · {token.description}</>}
                    </p>
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
                <dl className="token-meta" style={{ display: 'grid', gridTemplateColumns: 'repeat(3, minmax(0, 1fr))', gap: 8, fontSize: 12, margin: 0 }}>
                  <div><dt style={{ color: '#607169' }}>Created</dt><dd style={{ margin: 0 }}>{formatDate(token.created)}</dd></div>
                  <div><dt style={{ color: '#607169' }}>Expires</dt><dd style={{ margin: 0 }}>{token.expires_at ? formatDate(token.expires_at) : 'Never'}</dd></div>
                  <div><dt style={{ color: '#607169' }}>Last used</dt><dd style={{ margin: 0 }}>{token.last_used_at ? formatDate(token.last_used_at) : 'Never'}</dd></div>
                </dl>
              </article>
            ))}
          </div>
        </StatusBlock>
      </Panel>

      {creating && !issued && (
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
                <span>Expires in N days (blank = never expires)</span>
                <input
                  type="number"
                  min={0}
                  value={expiresDays}
                  onChange={(event) => setExpiresDays(event.target.value)}
                  placeholder="leave blank for no expiry"
                />
              </label>
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
              Use as <code>Authorization: Bearer {issued.prefix}…</code>
              {issued.expires_at ? <> · expires {formatDate(issued.expires_at)}</> : <> · never expires</>}.
            </p>
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

// formatDate renders the server's PocketBase DateTime string ("2026-01-
// 02 15:04:05.000Z") into a short locale-aware form. Falls back to the
// raw value if Date parsing fails (shouldn't normally happen).
function formatDate(value: string): string {
  if (!value) return ''
  const parsed = new Date(value.replace(' ', 'T'))
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString()
}
