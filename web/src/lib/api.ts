export type Stats = {
  total_pages?: number
  by_type?: Record<string, number>
  by_status?: Record<string, number>
  by_category?: Record<string, number>
}

export type PageSummary = {
  id: string
  title: string
  type: string
  category?: string | null
  status?: string
  tags?: string[]
}

export type PageDetail = PageSummary & {
  content?: string | null
  created_at?: string | null
  updated_at?: string | null
}

export type PageListPayload = {
  total: number
  pages: PageSummary[]
}

export type SearchPayload = {
  query: string
  total: number
  results: PageSummary[]
}

export type GraphStats = {
  nodes?: number
  relationships?: number
  labels?: string[]
  label_counts?: Record<string, number>
  error?: string
  [key: string]: number | string | string[] | Record<string, number> | undefined
}

import { pb } from './pb'

// Attach the current PocketBase auth token (if any) to outbound fetches so
// that protected /api/* endpoints accept us. Reads via the SDK so token
// rotation (authRefresh) is picked up automatically.
function authHeaders(): Record<string, string> {
  const token = pb.authStore.token
  return token ? { Authorization: `Bearer ${token}` } : {}
}

export async function getJson<T>(url: string): Promise<T> {
  const response = await fetch(url, { headers: { ...authHeaders() } })
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`)
  }
  return response.json() as Promise<T>
}

export function statValue(stats: Stats | null | undefined, group: keyof Stats, key: string) {
  const value = stats?.[group]
  if (!value || typeof value !== 'object') return 0
  return (value as Record<string, number>)[key] ?? 0
}

export function graphLabelCounts(stats: GraphStats | null | undefined) {
  if (!stats) return {}
  if (stats.label_counts) return stats.label_counts

  const counts: Record<string, number> = {}
  for (const [key, value] of Object.entries(stats)) {
    if (['nodes', 'relationships', 'labels', 'error'].includes(key)) continue
    if (typeof value === 'number') counts[key] = value
  }
  return counts
}

// ---------------------------------------------------------------------------
// Paper Markdown (server-side silent MinerU conversion)
// ---------------------------------------------------------------------------

// Status payload returned by the markdown endpoints. The content endpoint
// (GET .../markdown) returns this JSON for any non-200 response; the status
// endpoint (GET .../markdown/status) always returns 200 with this shape.
export type MarkdownStatus = {
  arxiv_id?: string
  status?: 'processing' | 'done' | 'failed' | 'no_pdf' | 'unavailable' | 'not_started' | string
  state?: string
  started_at?: string
  finished_at?: string
  error?: string
  detail?: string
  markdown_url?: string
  status_url?: string
  image_count?: number
}

export class MarkdownPendingError extends Error {
  readonly status: MarkdownStatus
  // Status resource URL to poll (from Operation-Location / status_url), if known.
  readonly statusUrl?: string
  // Server's Retry-After hint in ms, if any.
  readonly retryAfterMs?: number
  constructor(status: MarkdownStatus, statusUrl?: string, retryAfterMs?: number) {
    super(status.detail || 'markdown conversion still in progress')
    this.name = 'MarkdownPendingError'
    this.status = status
    this.statusUrl = statusUrl
    this.retryAfterMs = retryAfterMs
  }
}

export class MarkdownUnavailableError extends Error {
  readonly httpStatus: number
  readonly status: MarkdownStatus
  constructor(httpStatus: number, status: MarkdownStatus) {
    super(status.detail || status.error || `markdown unavailable (HTTP ${httpStatus})`)
    this.name = 'MarkdownUnavailableError'
    this.httpStatus = httpStatus
    this.status = status
  }
}

function markdownUrl(arxivId: string): string {
  return `/api/papers/${encodeURIComponent(arxivId)}/markdown`
}

function markdownStatusUrl(arxivId: string): string {
  return `/api/papers/${encodeURIComponent(arxivId)}/markdown/status`
}

// Parse a numeric Retry-After header (seconds → ms). HTTP-date form is
// ignored; the server only sends integer seconds.
function parseRetryAfterMs(response: Response): number | undefined {
  const raw = response.headers.get('Retry-After')
  if (!raw) return undefined
  const secs = Number(raw)
  return Number.isFinite(secs) && secs >= 0 ? secs * 1000 : undefined
}

async function jsonBody(response: Response): Promise<MarkdownStatus> {
  try {
    return (await response.json()) as MarkdownStatus
  } catch {
    return {}
  }
}

// One-shot fetch of the content endpoint: returns the Markdown text on a
// cache hit (200); throws MarkdownPendingError when the server kicked off a
// conversion (202, carrying the status URL + Retry-After), or
// MarkdownUnavailableError for 404/502/503/other.
export async function fetchMarkdownOnce(arxivId: string): Promise<string> {
  const response = await fetch(markdownUrl(arxivId), {
    headers: { ...authHeaders(), Accept: 'text/markdown, application/json' },
  })
  if (response.ok) {
    return response.text()
  }
  const payload = await jsonBody(response)
  if (response.status === 202) {
    const opLoc = response.headers.get('Operation-Location') || payload.status_url
    const statusUrl = opLoc || markdownStatusUrl(arxivId)
    throw new MarkdownPendingError(payload, statusUrl, parseRetryAfterMs(response))
  }
  throw new MarkdownUnavailableError(response.status, payload)
}

export type MarkdownStatusResult = {
  payload: MarkdownStatus
  retryAfterMs?: number
}

// Poll the side-effect-free status resource once.
export async function fetchMarkdownStatus(statusUrl: string): Promise<MarkdownStatusResult> {
  const response = await fetch(statusUrl, {
    headers: { ...authHeaders(), Accept: 'application/json' },
  })
  return { payload: await jsonBody(response), retryAfterMs: parseRetryAfterMs(response) }
}

export type FetchMarkdownOptions = {
  // Initial / minimum poll interval in ms (default 3000). Grows with backoff.
  pollIntervalMs?: number
  // Upper bound for the backoff between polls in ms (default 30000).
  maxPollIntervalMs?: number
  // Give up after this many ms (default 1_800_000 = 30 min).
  timeoutMs?: number
  // Called on each poll so the UI can show progress.
  onPending?: (status: MarkdownStatus) => void
  // Optional AbortSignal to cancel polling.
  signal?: AbortSignal
}

const BACKOFF_FACTOR = 1.5
const JITTER_FRACTION = 0.2
const MIN_SLEEP_MS = 500

function nextSleepMs(
  attempt: number,
  baseMs: number,
  capMs: number,
  retryAfterMs: number | undefined,
): number {
  let interval = Math.min(baseMs * BACKOFF_FACTOR ** attempt, capMs)
  if (retryAfterMs !== undefined) interval = Math.max(interval, retryAfterMs)
  interval *= 1 + JITTER_FRACTION * (2 * Math.random() - 1)
  return Math.max(MIN_SLEEP_MS, interval)
}

function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(new DOMException('markdown fetch aborted', 'AbortError'))
      return
    }
    const timer = setTimeout(resolve, ms)
    signal?.addEventListener(
      'abort',
      () => {
        clearTimeout(timer)
        reject(new DOMException('markdown fetch aborted', 'AbortError'))
      },
      { once: true },
    )
  })
}

// Fetch Markdown end to end: trigger via the content endpoint, then poll the
// status resource (capped exponential backoff + jitter, honouring the
// server's Retry-After) until it reports done, and fetch the content once
// more. Throws MarkdownUnavailableError on failure / no PDF / unavailable.
export async function fetchMarkdown(
  arxivId: string,
  options: FetchMarkdownOptions = {},
): Promise<string> {
  const pollIntervalMs = options.pollIntervalMs ?? 3000
  const maxPollIntervalMs = options.maxPollIntervalMs ?? 30000
  const timeoutMs = options.timeoutMs ?? 1_800_000
  const deadline = Date.now() + timeoutMs

  if (options.signal?.aborted) {
    throw new DOMException('markdown fetch aborted', 'AbortError')
  }

  let statusUrl: string
  let retryAfterMs: number | undefined
  try {
    return await fetchMarkdownOnce(arxivId)
  } catch (err) {
    if (!(err instanceof MarkdownPendingError)) throw err
    options.onPending?.(err.status)
    statusUrl = err.statusUrl ?? markdownStatusUrl(arxivId)
    retryAfterMs = err.retryAfterMs
  }

  let attempt = 0
  for (;;) {
    if (Date.now() >= deadline) {
      throw new MarkdownPendingError({ status: 'processing', detail: 'timed out waiting for conversion' }, statusUrl)
    }
    await sleep(nextSleepMs(attempt, pollIntervalMs, maxPollIntervalMs, retryAfterMs), options.signal)
    attempt += 1

    const { payload, retryAfterMs: ra } = await fetchMarkdownStatus(statusUrl)
    retryAfterMs = ra
    const status = payload.status
    if (status === 'done') {
      return fetchMarkdownOnce(arxivId)
    }
    if (status === 'failed' || status === 'unavailable') {
      throw new MarkdownUnavailableError(status === 'failed' ? 502 : 503, payload)
    }
    if (status === 'not_started') {
      // Job was evicted before we saw done — re-trigger and keep polling.
      try {
        return await fetchMarkdownOnce(arxivId)
      } catch (err) {
        if (!(err instanceof MarkdownPendingError)) throw err
        options.onPending?.(err.status)
        statusUrl = err.statusUrl ?? statusUrl
        retryAfterMs = err.retryAfterMs
        attempt = 0
        continue
      }
    }
    // processing (or unexpected) — keep polling.
    options.onPending?.(payload)
  }
}
