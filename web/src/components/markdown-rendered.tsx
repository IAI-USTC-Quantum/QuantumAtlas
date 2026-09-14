import { useTranslation } from 'react-i18next'
import { MAX_MARKDOWN_LENGTH } from '@/lib/markdown-limits'
import { MarkdownDocument } from './markdown-document'
import 'katex/dist/katex.min.css'
import './markdown.css'

export default function MarkdownRendered({ content }: { content: string }) {
  const { t } = useTranslation('common')
  if (!content) return <p className="text-sm text-muted-foreground">{t('markdown.empty')}</p>
  if (content.length > MAX_MARKDOWN_LENGTH) {
    return <p role="status" className="text-sm text-muted-foreground">{t('markdown.tooLarge')}</p>
  }
  return <div data-testid="markdown-rendered" data-renderer="react-markdown" className="markdown-content">
    <MarkdownDocument content={content} />
  </div>
}
