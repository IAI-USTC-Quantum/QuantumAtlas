import katex from 'katex'
import remarkGfm from 'remark-gfm'
import { fromHtml } from 'hast-util-from-html'
import type { Options } from 'react-markdown'
import type { Processor } from 'unified'
import type { Element, Root } from 'hast'
import type { Root as MarkdownRoot, RootContent, Nodes } from 'mdast'
import remarkMathCompat from './remark-math-compat'
import {
  MAX_MARKDOWN_LENGTH, MAX_FORMULA_LENGTH, MAX_FORMULAS, MAX_MATH_HTML_LENGTH, MAX_RENDERED_NODES,
} from './markdown-limits'
import { checkMarkdownTreeBudget, isAllowedMarkdownLink } from './markdown-policy'

/**
 * Options for the actual react-markdown component, not an alternative parser or
 * React renderer. Keep only the app's syntax, URL and resource policies here.
 * KaTeX stays at the same version as the former Worker for a fair comparison.
 */
export function createMarkdownOptions(source: string): Options {
  if (source.length > MAX_MARKDOWN_LENGTH) throw new RangeError('Markdown input too large')
  // Fail cheaply before tokenization on a storm of unmatched TeX openers. This
  // conservative lexical budget also counts openers in code; it is NOT a formula
  // parser or source rewrite. Escaped backslashes are skipped, Source unchanged.
  let texOpeners = 0
  for (let i = 0; i < source.length; i++) {
    if (source[i] !== '\\') continue
    if (source[i + 1] === '\\') { i++; continue }
    if ((source[i + 1] === '(' || source[i + 1] === '[') && ++texOpeners > MAX_FORMULAS) {
      throw new RangeError('Too many TeX delimiter openers')
    }
  }
  let mathOutputLength = 0
  let mathNodes = 0
  function formula(node: Extract<RootContent, { type: 'math' | 'inlineMath' }>): Element[] {
    const start = node.position?.start.offset
    const end = node.position?.end.offset
    const raw = start !== undefined && end !== undefined ? source.slice(start, end) : node.value
    const fallback = (): Element[] => [{ type: 'element', tagName: 'code',
      properties: { className: ['math-error'] }, children: [{ type: 'text', value: raw }] }]
    if (node.value.length > MAX_FORMULA_LENGTH) return fallback()
    let html: string
    try {
      html = katex.renderToString(node.value, {
        displayMode: node.type === 'math' || node.data?.mathDisplay === true,
        output: 'htmlAndMathml', trust: false, strict: 'error', throwOnError: true,
        maxExpand: 1_000, maxSize: 20, macros: {},
      })
    } catch {
      return fallback()
    }
    mathOutputLength += html.length
    if (mathOutputLength > MAX_MATH_HTML_LENGTH) throw new RangeError('Math output too large')
    // Only KaTeX's public output enters this parser, never authored HTML or
    // exception messages. No rehype-raw/MDX or code-fence math autodetection.
    const tree = fromHtml(html, { fragment: true })
    mathNodes += checkMarkdownTreeBudget(tree)
    if (mathNodes > MAX_RENDERED_NODES) throw new RangeError('Math structure too large')
    return tree.children as Element[]
  }

  function previewPolicy(this: Processor) {
    const data = this.data()
    ;(data.micromarkExtensions ??= []).push({ disable: { null: ['gfmFootnoteDefinition', 'gfmFootnoteCall'] } })
    return (tree: MarkdownRoot) => {
      // A single options object can be reused by StrictMode or a parent render.
      // Reset on EVERY processing run, not only when constructing the options.
      mathOutputLength = 0
      mathNodes = 0
      checkMarkdownTreeBudget(tree, true)
      const pending: Nodes[] = [tree]
      while (pending.length) {
        const node = pending.pop()!
        if (node.type === 'link' || node.type === 'definition') {
          // Check decoded mdast BEFORE the official handlers URI-normalize it.
          // Literal NUL is replaced by micromark: check its source span as well.
          const raw = source.slice(node.position?.start.offset, node.position?.end.offset)
          if (raw.includes('\0') || !isAllowedMarkdownLink(node.url)) node.url = ''
        }
        if (node.type === 'code' && node.lang && !/^[a-z\d_+-]{1,32}$/i.test(node.lang)) delete node.lang
        if ('children' in node) pending.push(...node.children)
      }
    }
  }

  return {
    remarkPlugins: [remarkGfm, remarkMathCompat, previewPolicy],
    remarkRehypeOptions: {
      handlers: { math: (_state, node) => formula(node), inlineMath: (_state, node) => formula(node) },
    },
    rehypePlugins: [() => (tree: Root) => { checkMarkdownTreeBudget(tree) }],
    // Official react-markdown handles raw HTML as visible text by default.
    // Its default URL transform allows app-relative paths; this app does not.
    urlTransform: (url, key) => key === 'href' && isAllowedMarkdownLink(url) ? url : undefined,
  }
}
