import { Component, lazy, Suspense, type ReactNode } from 'react'
import { useTranslation } from 'react-i18next'

import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'

// react-markdown, KaTeX and local CSS/fonts load only when a preview is opened.
// Lazy loading does not make subsequent main-thread parsing cancellable.
const MarkdownRendered = lazy(() => import('./markdown-rendered'))

class RenderBoundary extends Component<
  { children: ReactNode; fallback: ReactNode },
  { failed: boolean }
> {
  state = { failed: false }

  static getDerivedStateFromError() {
    return { failed: true }
  }

  render() {
    return this.state.failed ? this.props.fallback : this.props.children
  }
}

export function MarkdownPreview({ content }: { content: string }) {
  const { t } = useTranslation('common')
  return (
    <div data-testid="markdown-preview" className="min-w-0 space-y-2">
      <Tabs defaultValue="rendered" className="min-w-0">
        <TabsList aria-label={t('markdown.viewLabel')}>
          <TabsTrigger value="rendered">{t('markdown.rendered')}</TabsTrigger>
          <TabsTrigger value="source">{t('markdown.source')}</TabsTrigger>
        </TabsList>
        <TabsContent value="rendered" className="min-w-0">
          <div className="max-h-[min(60vh,600px)] overflow-auto rounded-md border border-border bg-background p-4">
            <RenderBoundary key={content} fallback={<p role="alert" className="text-sm text-destructive">{t('markdown.failed')}</p>}>
              <Suspense fallback={<p role="status" className="text-sm text-muted-foreground">{t('markdown.rendering')}</p>}>
                <MarkdownRendered content={content} />
              </Suspense>
            </RenderBoundary>
          </div>
        </TabsContent>
        <TabsContent value="source" className="min-w-0">
          <pre
            data-testid="markdown-source"
            className="max-h-[min(60vh,600px)] overflow-auto rounded-md border border-border bg-muted/30 p-4 font-mono text-xs leading-5 break-words whitespace-pre-wrap"
          >{content}</pre>
          {!content && <p className="mt-2 text-sm text-muted-foreground">{t('markdown.empty')}</p>}
        </TabsContent>
      </Tabs>
      <p className="text-xs text-muted-foreground">{t('markdown.safetyNote')}</p>
    </div>
  )
}
