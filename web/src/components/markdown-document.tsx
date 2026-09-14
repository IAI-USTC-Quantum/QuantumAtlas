import { memo } from 'react'
import Markdown, { type Components } from 'react-markdown'
import { createMarkdownOptions } from '@/lib/markdown-renderer'
import { isAllowedMarkdownLink } from '@/lib/markdown-policy'

const components: Components = {
  // No img element, src/srcset or React image preloads, including references.
  img: ({ alt }) => <span className="markdown-image">[Image: {alt ?? ''}]</span>,
  input: ({ checked }) => checked ? '[x]' : '[ ]',
  a: ({ href, title, children }) => {
    if (!href || !isAllowedMarkdownLink(href)) return <span>{children}</span>
    const external = !href.startsWith('#')
    return <a href={href} title={title}
      target={external ? '_blank' : undefined}
      rel={external ? 'noopener noreferrer nofollow' : undefined}
      referrerPolicy={external ? 'no-referrer' : undefined}>{children}</a>
  },
}

// react-markdown owns parsing, the unified pipeline and HAST-to-React conversion.
// Memoization avoids unrelated parent rerenders; it does NOT move work off the
// main thread or make synchronous parsing interruptible.
export const MarkdownDocument = memo(function MarkdownDocument({ content }: { content: string }) {
  return <Markdown {...createMarkdownOptions(content)} components={components}>{content}</Markdown>
})
