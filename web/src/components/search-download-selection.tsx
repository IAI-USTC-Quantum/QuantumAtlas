import { createContext, useContext, useId, useMemo, useRef, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { Button } from '@/components/ui/button'
import { downloaderFetch, isDownloaderAvailable, type SearchHit } from '@/lib/api'
import { downloadAliases, downloadChoices, downloadInput, type DownloadChoice } from '@/lib/search-downloads'
import { usePlugins } from '@/lib/queries'
import { useLang } from '@/hooks/use-lang'

const SelectionContext = createContext<{
  choices: DownloadChoice[]
  selected: Set<string>
  submitted: Set<string>
  disabled: boolean
  toggle: (key: string) => void
} | null>(null)

type Props = { hits: SearchHit[]; resetKey: string; children: ReactNode }

// A fresh query/mode/source/result set gets a fresh, empty selection. Keep the
// provider above all backend tabs so duplicate papers share a single checkbox.
export function SearchDownloadSelection({ hits, resetKey, children }: Props) {
  return <SelectionSession key={resetKey} hits={hits}>{children}</SelectionSession>
}

function SelectionSession({ hits, children }: Omit<Props, 'resetKey'>) {
  const { t } = useTranslation('papers')
  const lang = useLang()
  const qc = useQueryClient()
  const plugins = usePlugins()
  // Same availability/auth contract as the downloader page: browser sessions
  // use ScopeMaster; the authenticated endpoint enforces papers:write.
  const available = isDownloaderAvailable(plugins.data?.plugins)
  const choices = useMemo(() => downloadChoices(hits), [hits])
  const [selected, setSelected] = useState<Set<string>>(() => new Set())
  const [submitted, setSubmitted] = useState<Set<string>>(() => new Set())
  const [failures, setFailures] = useState<Record<string, string>>({})
  const [pending, setPending] = useState(false)
  const [accepted, setAccepted] = useState<number>()
  const busy = useRef(false)

  function toggle(key: string) {
    if (busy.current || !available || submitted.has(key)) return
    setSelected((previous) => {
      const next = new Set(previous)
      if (next.has(key)) next.delete(key)
      else next.add(key)
      return next
    })
  }

  async function submit(retryOnly = false) {
    if (busy.current || !available) return
    const batch = choices.filter((choice) => selected.has(choice.key) && !submitted.has(choice.key) && (!retryOnly || choice.key in failures))
    if (!batch.length || batch.length > 50) return
    busy.current = true // protects same-turn duplicate activations before React renders
    setPending(true)
    setAccepted(undefined)
    const errors: Record<string, string> = {}
    const succeeded = new Set<string>()
    try {
      const response = await downloaderFetch(batch.map((choice) => choice.input))
      for (const choice of batch) {
        const item = response.items.find((item) => item.input === choice.input)
        if (!item) errors[choice.key] = t('downloadSelection.missingResponse')
        else if (item.error || item.kind === 'invalid') errors[choice.key] = item.error || t('downloadSelection.unsupported')
        else succeeded.add(choice.key)
      }
      setAccepted(succeeded.size)
      void qc.invalidateQueries({ queryKey: ['paper'] })
      void qc.invalidateQueries({ queryKey: ['downloader-jobs'] })
      void qc.invalidateQueries({ queryKey: ['downloader-remote-jobs'] })
    } catch (cause) {
      const message = cause instanceof Error ? cause.message : String(cause)
      for (const choice of batch) errors[choice.key] = message
    } finally {
      setFailures((previous) => {
        const next = { ...previous }
        for (const choice of batch) delete next[choice.key]
        return { ...next, ...errors }
      })
      setSubmitted((previous) => new Set([...previous, ...succeeded]))
      setSelected((previous) => new Set([...previous].filter((key) => !succeeded.has(key))))
      busy.current = false
      setPending(false)
    }
  }

  const retryCount = Object.keys(failures).filter((key) => selected.has(key)).length
  return (
    <SelectionContext.Provider value={{ choices, selected, submitted, disabled: pending || !available, toggle }}>
      {hits.length > 0 && <section aria-label={t('downloadSelection.title')} className="space-y-2 rounded-lg border p-3">
        <p className="text-sm text-muted-foreground">{t('downloadSelection.hint')}</p>
        {!available && <p className="text-sm text-muted-foreground">{t('downloader:unavailable')}</p>}
        <div className="flex flex-wrap items-center gap-3">
          <span className="text-sm" data-testid="download-selection-count">{t('downloadSelection.count', { count: selected.size })}</span>
          <Button type="button" disabled={!available || pending || selected.size === 0 || selected.size > 50} onClick={() => void submit()}>
            {t(pending ? 'downloadSelection.submitting' : 'downloadSelection.submit')}
          </Button>
          {retryCount > 0 && <Button type="button" variant="outline" disabled={!available || pending || retryCount > 50} onClick={() => void submit(true)}>
            {t('downloadSelection.retry', { count: retryCount })}
          </Button>}
          <Link to="/$lang/downloader" params={{ lang }} className="text-sm text-primary underline">{t('downloadSelection.jobs')}</Link>
        </div>
        {selected.size > 50 && <p className="text-sm text-destructive">{t('downloadSelection.limit')}</p>}
        {accepted !== undefined && <p role="status" className="text-sm">{t('downloadSelection.accepted', { count: accepted })}</p>}
        {Object.keys(failures).length > 0 && <div role="alert" className="space-y-1 text-sm text-destructive">
          <p>{t('downloadSelection.failed')}</p>
          <ul>{choices.filter((choice) => choice.key in failures).map((choice) => <li key={choice.key} className="break-words">{choice.title}: {failures[choice.key]}</li>)}</ul>
        </div>}
      </section>}
      {children}
    </SelectionContext.Provider>
  )
}

export function SearchDownloadCheckbox({ hit, paperId }: { hit: SearchHit; paperId?: string }) {
  const selection = useContext(SelectionContext)
  const { t } = useTranslation('papers')
  const explanationId = useId()
  if (!selection) return null
  const aliases = downloadAliases({ ...hit, paper_id: paperId ?? hit.paper_id })
  const choice = downloadInput(hit) ? selection.choices.find((choice) => aliases.some((alias) => choice.aliases.has(alias))) : undefined
  const submitted = choice && selection.submitted.has(choice.key)
  const title = hit.title || hit.arxiv_id || hit.doi || t('untitled')
  return <div className="space-y-1">
    <label className="flex items-center gap-2 text-sm">
      <input type="checkbox" className="size-4 accent-primary" checked={Boolean(choice && selection.selected.has(choice.key))}
        disabled={!choice || selection.disabled || Boolean(submitted)}
        aria-label={t('downloadSelection.select', { title })} aria-describedby={!choice ? explanationId : undefined}
        onChange={() => { if (choice) selection.toggle(choice.key) }} />
      {t(submitted ? 'downloadSelection.submitted' : 'downloadSelection.selectLabel')}
    </label>
    {!choice && <p id={explanationId} className="text-xs text-muted-foreground">{t('downloadSelection.unsupported')}</p>}
  </div>
}
