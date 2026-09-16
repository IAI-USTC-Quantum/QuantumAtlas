import { afterEach, describe, expect, it, vi } from 'vitest'
import { generateScorer, getScoringCapabilities, isScoringQuotaError, rankedSearch, ScoringError } from '../src/lib/scoring-api'
import { postJson } from '../src/lib/api'
import { ScorerSession } from '../src/lib/scorer-session'
import en from '../src/i18n/locales/en.json'
import zh from '../src/i18n/locales/zh.json'

vi.mock('../src/lib/pb', () => ({ pb: { authStore: { token: 'synthetic-token' } } }))
afterEach(() => vi.unstubAllGlobals())

function mockResponse(body: unknown, status = 200) {
  const fetch = vi.fn().mockResolvedValue(new Response(JSON.stringify(body), { status }))
  vi.stubGlobal('fetch', fetch)
  return fetch
}

describe('scoring API contracts', () => {
  it('retains capability metadata and sends authenticated, abortable explicit generation', async () => {
    const capabilities = { language: 'qatlas-expr-v1', feature_version: '1', functions: ['min'], features: { year: 'number' }, generation_available: false }
    const fetch = mockResponse(capabilities)
    const controller = new AbortController()
    expect(await getScoringCapabilities(controller.signal)).toEqual(capabilities)
    expect(fetch).toHaveBeenCalledWith('/api/search/scoring/capabilities', expect.objectContaining({ method: 'GET', signal: controller.signal }))
    fetch.mockResolvedValue(new Response(JSON.stringify({ scorer: { language: 'qatlas-expr-v1', filter: 'true', score: '1' }, summary: 'AI explanation', warnings: [], scorer_hash: 'hash', feature_version: '1', usage: { today: 1, limit: 5, llm_tokens: 50 } })))
    const body = { query: 'quantum codes', requirements: 'prefer recent papers' }
    const data = await generateScorer(body, controller.signal)
    expect(data.usage.today).toBe(1)
    expect(fetch).toHaveBeenLastCalledWith('/api/search/scoring/generate', expect.objectContaining({
      method: 'POST', body: JSON.stringify(body), signal: controller.signal,
      headers: { Authorization: 'Bearer synthetic-token', 'content-type': 'application/json' },
    }))
  })

  it('preserves ranked hit order, unbounded scores, registry/raw fields and structured explanations', async () => {
    const response = { hits: [
      { title: 'title-only first', source: 'arxiv', score: 42, raw_rank: 9, raw_score: 0.2, score_detail: { recency: 42 }, score_explanation: { trace: ['<script>unsafe()</script>'] } },
      { title: 'registry second', source: 'openalex', score: -12, paper_id: 'paper-1', created: true, has_md: true, status: 'ready', score_detail: null },
    ], ranking: { scorer_hash: 'hash' }, errors: { other: 'timeout' }, remote: true, usage: { llm_tokens: 0 } }
    const fetch = mockResponse(response)
    const controller = new AbortController()
    const body = { text: 'codes', sources: ['arxiv', 'openalex'], scorer: { language: 'qatlas-expr-v1' as const, filter: 'true', score: '-12' }, explain: false }
    expect(await rankedSearch(body, controller.signal)).toEqual(response)
    expect(fetch).toHaveBeenCalledExactlyOnceWith('/api/search/ranked', expect.objectContaining({ body: JSON.stringify(body), signal: controller.signal }))
  })

  it.each([422, 429, 503])('retains status %s, structured detail including zero position, and quota', async (status) => {
    const detail = { code: 'invalid_expression', message: 'Invalid score', field: 'score', position: 0 }
    const usage = { today: 5, limit: 5, llm_tokens: 0 }
    mockResponse({ detail, usage }, status)
    await expect(generateScorer({ query: 'codes', requirements: 'new' })).rejects.toMatchObject({
      name: 'ScoringError', status, message: 'Invalid score', detail, usage,
    })
  })

  it('retains the real structured DSL position, including zero-valued coordinates', async () => {
    const detail = {
      code: 'invalid_expression', message: 'Unknown score feature', field: 'scorer.score',
      position: { line: 1, column: 0, end_line: 1, end_column: 7 },
    }
    mockResponse({ detail, usage: { llm_tokens: 0 } }, 422)
    await expect(rankedSearch({ text: 'codes', sources: ['arxiv'], scorer: {
      language: 'qatlas-expr-v1', filter: 'true', score: 'unknown',
    } })).rejects.toMatchObject({ status: 422, detail, usage: { llm_tokens: 0 } })
  })

  it.each([
    { code: 'busy', usage: undefined, expected: false },
    { code: 'rate_limited', usage: undefined, expected: false },
    { code: 'busy', usage: { today: 1, limit: 10 }, expected: false },
    { code: 'rate_limited', usage: { today: 1, limit: 10 }, expected: false },
    { code: 'unknown', usage: undefined, expected: false },
    { code: 'quota_exceeded', usage: undefined, expected: true },
    { code: 'quota_exceeded', usage: { today: 0, limit: 0 }, expected: true },
    { code: 'legacy_quota', usage: { today: 10, limit: 10 }, expected: true },
  ])('classifies 429 $code with usage $usage as daily quota: $expected', ({ code, usage, expected }) => {
    expect(isScoringQuotaError(new ScoringError(429, { code, message: 'request rejected' }, usage))).toBe(expected)
  })

  it('only classifies HTTP 429 and supports legacy string detail with explicit quota usage', () => {
    expect(isScoringQuotaError(new Error('429'))).toBe(false)
    expect(isScoringQuotaError(new ScoringError(503, { code: 'quota_exceeded', message: 'unavailable' }, { limit: 5 }))).toBe(false)
    expect(isScoringQuotaError(new ScoringError(429, 'Too many requests'))).toBe(false)
    expect(isScoringQuotaError(new ScoringError(429, 'Quota exceeded', { limit: 0 }))).toBe(true)
  })

  it('retains string and non-JSON error status without changing legacy postJson', async () => {
    const fetch = mockResponse({ detail: 'unavailable' }, 503)
    await expect(getScoringCapabilities()).rejects.toMatchObject({ status: 503, detail: 'unavailable' })
    fetch.mockResolvedValue(new Response('not JSON', { status: 502, statusText: 'Bad Gateway' }))
    await expect(getScoringCapabilities()).rejects.toMatchObject({ status: 502, message: 'Bad Gateway' })
    fetch.mockResolvedValue(new Response(JSON.stringify({ detail: 'legacy error' }), { status: 400 }))
    await expect(postJson('/api/search', {})).rejects.toThrow('400: legacy error')
    expect(new ScoringError(503, 'unavailable')).toBeInstanceOf(Error)
  })
})

describe('scorer confirmation and request lifetime', () => {
  it('requires explicit confirmation and invalidates it even when a draft is restored', () => {
    const session = new ScorerSession()
    expect(session.confirmed).toBe(false)
    session.confirm()
    expect(session.confirmed).toBe(true)
    session.invalidate() // topic/requirements/expression/source edits all share this path
    expect(session.confirmed).toBe(false)
    session.invalidate() // restoring an older value must not restore its confirmation
    expect(session.confirmed).toBe(false)
  })

  it('locks synchronously against duplicate clicks; confirmation cannot change while pending', () => {
    const session = new ScorerSession()
    const request = session.begin()!
    expect(session.busy).toBe(true)
    expect(session.begin()).toBeNull()
    session.confirm()
    expect(session.confirmed).toBe(false)
    session.finish(request)
    expect(session.busy).toBe(false)
    session.confirm()
    expect(session.confirmed).toBe(true)
  })

  it('aborts and ignores late responses even when transport ignores abort', async () => {
    const session = new ScorerSession()
    const old = session.begin()!
    let resolve!: (value: string) => void
    const oldResponse = new Promise<string>((done) => { resolve = done })
    let displayed = ''
    const delivery = oldResponse.then((value) => { if (session.current(old)) displayed = value })
    session.invalidate()
    expect(old.controller.signal.aborted).toBe(true)
    const latest = session.begin()!
    expect(session.current(latest)).toBe(true)
    displayed = 'current response'
    resolve('stale response')
    await delivery
    session.finish(old)
    expect(displayed).toBe('current response')
    expect(session.busy).toBe(true)
    session.finish(latest)
    expect(session.busy).toBe(false)
  })

  it('has matching English and Chinese scoring copy', () => {
    expect(Object.keys(en.papers.scoring).sort()).toEqual(Object.keys(zh.papers.scoring).sort())
    for (const text of [...Object.values(en.papers.scoring), ...Object.values(zh.papers.scoring)]) expect(text.trim()).not.toBe('')
  })
})
