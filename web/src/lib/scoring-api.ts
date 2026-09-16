import { authHeaders, type SearchHit, type SearchUsage } from './api'

export type Scorer = {
  language: 'qatlas-expr-v1'
  filter: string
  score: string
}

// Keep the DSL capability document intact, including future feature metadata.
export type ScoringCapabilities = {
  language: string
  feature_version: string
  generation_available: boolean
  [key: string]: unknown
}
export type GenerateScorerEntry = { query: string; requirements: string }
export type GenerateScorerResponse = {
  scorer: Scorer
  summary: string
  warnings: string[]
  scorer_hash: string
  feature_version: string
  usage: SearchUsage & { llm_tokens: number }
}
export type RankedSearchEntry = {
  text: string
  sources: string[]
  max_results?: number
  scorer: Scorer
  explain?: boolean
}
export type RankedSearchResponse = {
  hits: SearchHit[]
  ranking: Record<string, unknown>
  usage: { llm_tokens: number }
  errors: Record<string, string>
  remote: true
}
export type ScoringErrorDetail = {
  code: string
  message: string
  field?: string
  position?: number | Record<string, number>
}
export class ScoringError extends Error {
  readonly status: number
  readonly detail: ScoringErrorDetail | string
  readonly usage?: Partial<SearchUsage>

  constructor(status: number, detail: ScoringErrorDetail | string, usage?: Partial<SearchUsage>) {
    super(typeof detail === 'string' ? detail : detail.message)
    this.name = 'ScoringError'
    this.status = status
    this.detail = detail
    this.usage = usage
  }
}

// Concurrency/provider throttling is not exhaustion of the user's daily quota,
// even when a failed generation also returns an informational daily usage block.
export function isScoringQuotaError(error: Error): error is ScoringError {
  if (!(error instanceof ScoringError) || error.status !== 429) return false
  const code = typeof error.detail === 'string' ? undefined : error.detail.code
  if (code === 'busy' || code === 'rate_limited') return false
  return code === 'quota_exceeded' || error.usage?.limit !== undefined
}

// Deliberately separate from legacy postJson: structured DSL errors must retain
// their HTTP status, field/position and quota data. AbortSignal reaches fetch.
async function scoringJson<T>(url: string, signal?: AbortSignal, body?: unknown): Promise<T> {
  const response = await fetch(url, {
    method: body === undefined ? 'GET' : 'POST',
    headers: { ...authHeaders(), ...(body === undefined ? {} : { 'content-type': 'application/json' }) },
    signal,
    ...(body === undefined ? {} : { body: JSON.stringify(body) }),
  })
  if (!response.ok) {
    let detail: ScoringErrorDetail | string = response.statusText || String(response.status)
    let usage: Partial<SearchUsage> | undefined
    try {
      const json = await response.json() as { detail?: ScoringErrorDetail | string; usage?: Partial<SearchUsage> }
      if (typeof json.detail === 'string' || (json.detail && typeof json.detail.message === 'string')) {
        detail = json.detail
      }
      usage = json.usage
    } catch { /* Non-JSON errors still retain their HTTP status. */ }
    throw new ScoringError(response.status, detail, usage)
  }
  return response.json() as Promise<T>
}

export const getScoringCapabilities = (signal?: AbortSignal) =>
  scoringJson<ScoringCapabilities>('/api/search/scoring/capabilities', signal)
export const generateScorer = (body: GenerateScorerEntry, signal?: AbortSignal) =>
  scoringJson<GenerateScorerResponse>('/api/search/scoring/generate', signal, body)
export const rankedSearch = (body: RankedSearchEntry, signal?: AbortSignal) =>
  scoringJson<RankedSearchResponse>('/api/search/ranked', signal, body)
