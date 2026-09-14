import { useEffect, useState, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import {
  MARKDOWN_RENDER_TIMEOUT_MS,
  MAX_MARKDOWN_LENGTH,
} from '@/lib/markdown-protocol'
import { markdownReplyToReact } from '@/lib/markdown-react'
import 'katex/dist/katex.min.css'
import './markdown.css'

type RenderResult = { source: string; rendered?: ReactNode; failed?: boolean }

export default function MarkdownRendered({ content }: { content: string }) {
  const { t } = useTranslation('common')
  const [result, setResult] = useState<RenderResult | null>(null)

  useEffect(() => {
    if (!content || content.length > MAX_MARKDOWN_LENGTH) return
    let active = true
    let settled = false
    let worker: Worker | undefined
    let timer: ReturnType<typeof setTimeout> | undefined
    const stop = () => {
      clearTimeout(timer)
      worker?.terminate()
    }
    const fail = () => {
      if (active && !settled) {
        settled = true
        setResult({ source: content, failed: true })
      }
      stop()
    }

    try {
      worker = new Worker(new URL('../lib/markdown.worker.ts', import.meta.url), { type: 'module' })
      timer = setTimeout(fail, MARKDOWN_RENDER_TIMEOUT_MS)
      worker.onerror = (event) => {
        event.preventDefault()
        fail()
      }
      worker.onmessageerror = fail
      worker.onmessage = (event: MessageEvent<unknown>) => {
        if (!active || settled) return
        try {
          // A Worker result is not trusted JSX. Check its serialized size,
          // reconstruct a bounded inert tree, then create React elements.
          const rendered = markdownReplyToReact(event.data)
          settled = true
          setResult({ source: content, rendered })
          stop()
        } catch {
          fail()
        }
      }
      worker.postMessage(content)
    } catch {
      fail()
    }

    return () => {
      active = false
      stop()
    }
  }, [content])

  if (!content) {
    return <p className="text-sm text-muted-foreground">{t('markdown.empty')}</p>
  }
  if (content.length > MAX_MARKDOWN_LENGTH) {
    return <p role="status" className="text-sm text-muted-foreground">{t('markdown.tooLarge')}</p>
  }
  // Never show a previous document while the next worker is running.
  if (!result || result.source !== content) {
    return <p role="status" className="text-sm text-muted-foreground">{t('markdown.rendering')}</p>
  }
  if (result.failed || result.rendered === undefined) {
    return <p role="alert" className="text-sm text-destructive">{t('markdown.failed')}</p>
  }
  return (
    <div
      data-testid="markdown-rendered"
      className="markdown-content"
    >{result.rendered}</div>
  )
}
