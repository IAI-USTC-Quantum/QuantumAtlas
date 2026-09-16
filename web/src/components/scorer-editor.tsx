import { useCallback, useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Textarea } from '@/components/ui/textarea'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { ScorerResults } from '@/components/scorer-results'
import { useGenerateScorer, useRankedSearch, useScoringCapabilities } from '@/lib/queries'
import { isScoringQuotaError, ScoringError, type GenerateScorerResponse, type RankedSearchResponse, type Scorer } from '@/lib/scoring-api'
import { ScorerSession } from '@/lib/scorer-session'

export function ScorerEditor({ initialQuery, sources }: { initialQuery: string; sources: string[] }) {
  const { t } = useTranslation('papers')
  const capabilities = useScoringCapabilities(true)
  const generate = useGenerateScorer()
  const execute = useRankedSearch()
  const [session] = useState(() => new ScorerSession())
  const [topic, setTopic] = useState(initialQuery)
  const [requirements, setRequirements] = useState('')
  const [scorer, setScorer] = useState<Scorer>({ language: 'qatlas-expr-v1', filter: 'true', score: '' })
  const [generated, setGenerated] = useState<GenerateScorerResponse>()
  const [usage, setUsage] = useState<GenerateScorerResponse['usage']>()
  const [results, setResults] = useState<RankedSearchResponse>()
  const [error, setError] = useState<Error>()
  const [pending, setPending] = useState<'generate' | 'execute' | null>(null)
  const [confirmed, setConfirmed] = useState(false)
  const [explain, setExplain] = useState(false)

  const invalidate = useCallback(() => {
    session.invalidate()
    setConfirmed(false)
    setPending(null)
    setResults(undefined)
    setError(undefined)
  }, [session])

  // Backend changes invalidate both confirmation and in-flight results; leaving
  // custom mode aborts outstanding work and cannot revive it on remount.
  const sourceKey = JSON.stringify(sources)
  useEffect(() => { invalidate() }, [sourceKey, invalidate])
  useEffect(() => () => session.invalidate(), [session])

  const canConfirm = Boolean(topic.trim() && scorer.filter.trim() && scorer.score.trim() && sources.length && !pending)
  const canGenerate = Boolean(capabilities.data?.generation_available && topic.trim() && requirements.trim() && !pending)
  const edited = generated && (generated.scorer.filter !== scorer.filter || generated.scorer.score !== scorer.score)

  async function onGenerate() {
    if (!canGenerate || session.busy) return
    invalidate()
    setGenerated(undefined)
    const request = session.begin()
    if (!request) return
    setPending('generate')
    try {
      const data = await generate.mutateAsync({
        body: { query: topic.trim(), requirements: requirements.trim() }, signal: request.controller.signal,
      })
      if (!session.current(request)) return
      setScorer(data.scorer)
      setGenerated(data)
      setUsage(data.usage)
    } catch (cause) {
      if (session.current(request)) setError(cause instanceof Error ? cause : new Error(String(cause)))
    } finally {
      if (session.current(request)) { session.finish(request); setPending(null) }
    }
  }

  async function onExecute() {
    if (!canConfirm || !confirmed || !session.confirmed || session.busy) return
    const request = session.begin()
    if (!request) return
    setPending('execute')
    setResults(undefined)
    setError(undefined)
    try {
      const data = await execute.mutateAsync({
        body: { text: topic.trim(), sources, scorer: { ...scorer }, explain }, signal: request.controller.signal,
      })
      if (session.current(request)) setResults(data)
    } catch (cause) {
      if (session.current(request)) setError(cause instanceof Error ? cause : new Error(String(cause)))
    } finally {
      if (session.current(request)) { session.finish(request); setPending(null) }
    }
  }

  return (
    <div className="space-y-4" data-testid="scorer-editor">
      <div className="space-y-4 rounded-lg border p-4">
        <h2 className="font-semibold">{t('scoring.title')}</h2>
        <p className="text-sm text-muted-foreground">{t('scoring.cost')}</p>
        <label className="block space-y-2 text-sm">
          <span>{t('scoring.topic')}</span>
          <Input maxLength={4000} value={topic} onChange={(event) => {
            invalidate(); setGenerated(undefined); setTopic(event.target.value)
          }} />
        </label>
        <label className="block space-y-2 text-sm">
          <span>{t('scoring.requirements')}</span>
          <Textarea maxLength={4000} value={requirements} placeholder={t('scoring.requirementsPlaceholder')} onChange={(event) => {
            invalidate(); setGenerated(undefined); setRequirements(event.target.value)
          }} />
        </label>
        {capabilities.isLoading ? <p role="status">{t('scoring.loadingCapabilities')}</p> :
          !capabilities.data?.generation_available && <p className="text-sm text-muted-foreground">{t('scoring.generationUnavailable')}</p>}
        {capabilities.error && <ScorerError error={capabilities.error} />}
        <Button type="button" disabled={!canGenerate} onClick={() => void onGenerate()}>
          {t(pending === 'generate' ? 'scoring.generating' : 'scoring.generate')}
        </Button>
        {usage && <p className="text-sm">{t('agentic.usage', usage)} · {t('scoring.tokens', { count: usage.llm_tokens })}</p>}
        {generated && (
          <section className="space-y-2 rounded-md bg-muted p-3" aria-label={t('scoring.aiSummary')}>
            <h3 className="font-medium">{t('scoring.aiSummary')}</h3>
            <p className="whitespace-pre-wrap break-words text-sm">{generated.summary}</p>
            {edited && <p className="text-sm">{t('scoring.summaryEdited')}</p>}
            {generated.warnings.length > 0 && <div>
              <h4 className="text-sm font-medium">{t('scoring.warnings')}</h4>
              <ul className="list-inside list-disc text-sm">{generated.warnings.map((warning, index) => <li key={index}>{warning}</li>)}</ul>
            </div>}
            <p className="break-all font-mono text-xs">{t('scoring.generatedVersion', { version: generated.feature_version, hash: generated.scorer_hash })}</p>
          </section>
        )}
        <details open className="space-y-3">
          <summary className="cursor-pointer text-sm font-medium">{t('scoring.advanced')}</summary>
          <p className="text-sm text-muted-foreground">{t('scoring.dslHint')}</p>
          <p className="font-mono text-xs">{scorer.language}</p>
          <label className="block space-y-2 text-sm">
            <span>{t('scoring.filter')}</span>
            <Textarea className="font-mono" spellCheck={false} value={scorer.filter} onChange={(event) => {
              invalidate(); setScorer({ ...scorer, filter: event.target.value })
            }} />
          </label>
          <label className="block space-y-2 text-sm">
            <span>{t('scoring.score')}</span>
            <Textarea className="font-mono" spellCheck={false} value={scorer.score} onChange={(event) => {
              invalidate(); setScorer({ ...scorer, score: event.target.value })
            }} />
          </label>
          {capabilities.data && <details>
            <summary className="cursor-pointer text-sm">{t('scoring.capabilities')}</summary>
            <pre className="mt-2 max-h-72 overflow-auto whitespace-pre-wrap break-all text-xs">{JSON.stringify(capabilities.data, null, 2)}</pre>
          </details>}
        </details>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={explain} onChange={(event) => { invalidate(); setExplain(event.target.checked) }} />
          {t('scoring.explain')}
        </label>
        <p className="text-sm text-muted-foreground">{t('scoring.confirmHint')}</p>
        <label className="flex items-center gap-2 text-sm">
          <input type="checkbox" checked={confirmed} disabled={!canConfirm} onChange={(event) => {
            if (event.target.checked) { session.confirm(); setConfirmed(session.confirmed) }
            else invalidate()
          }} />
          {t('scoring.confirm')}
        </label>
        <div className="flex flex-wrap gap-2">
          <Button type="button" disabled={!canConfirm || !confirmed} onClick={() => void onExecute()}>
            {t(pending === 'execute' ? 'scoring.executing' : 'scoring.execute')}
          </Button>
          {pending && <Button type="button" variant="outline" onClick={invalidate}>{t('scoring.cancel')}</Button>}
        </div>
        {pending && <p role="status" className="text-sm">{t('scoring.cancelHint')}</p>}
        {sources.length === 0 && <p className="text-sm">{t('backends.noneSelected')}</p>}
        {error && <ScorerError error={error} />}
      </div>
      {results && <ScorerResults results={results} />}
    </div>
  )
}

function ScorerError({ error }: { error: Error }) {
  const { t } = useTranslation('papers')
  const structured = error instanceof ScoringError && typeof error.detail !== 'string' ? error.detail : undefined
  const isQuota = isScoringQuotaError(error)
  const quota = isQuota ? error.usage : undefined
  const position = typeof structured?.position === 'object'
    ? JSON.stringify(structured.position)
    : structured?.position
  return (
    <Alert variant="destructive" role="alert">
      <AlertTitle>{t(isQuota ? 'agentic.rateLimitedTitle' : 'scoring.errorTitle')}</AlertTitle>
      <AlertDescription className="space-y-1 break-all">
        <p>{error instanceof ScoringError ? `${error.status}: ` : ''}{error.message}</p>
        {structured && <p>{structured.code}{structured.field !== undefined && ` · ${t('scoring.errorField', { field: structured.field })}`}{position !== undefined && ` · ${t('scoring.errorPosition', { position })}`}</p>}
        {quota?.today !== undefined && quota.limit !== undefined && <p>{t('agentic.rateLimited', quota)}</p>}
      </AlertDescription>
    </Alert>
  )
}
