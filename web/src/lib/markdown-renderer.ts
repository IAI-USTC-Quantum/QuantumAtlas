import katex from 'katex'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'
import remarkRehype, { type Options } from 'remark-rehype'
import { fromHtml } from 'hast-util-from-html'
import type { Element, Properties, Root } from 'hast'
import type { Definition, Link, RootContent, Nodes } from 'mdast'
import remarkMathCompat from './remark-math-compat'
import { MAX_MARKDOWN_LENGTH, MAX_RENDERED_TREE_LENGTH } from './markdown-protocol'
import { isAllowedMarkdownLink, validateMarkdownTree } from './markdown-tree'

export { MAX_MARKDOWN_LENGTH } from './markdown-protocol'
const MAX_FORMULA_LENGTH = 10_000

/**
 * DOM-free, synchronous pipeline, used ONLY in a disposable Worker in the UI.
 * Markdown is parsed once, not converted to HTML and then reparsed. Only the
 * public KaTeX output is parsed as HTML (parse5, no DOM/jsdom/browser requests).
 * No rehype-raw, MDX, code-fence math autodetection or shared macros/config.
 */
export function renderMarkdownToTree(source: string): Root {
  if (source.length > MAX_MARKDOWN_LENGTH) throw new RangeError('Markdown input too large')
  let mathOutputLength = 0
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
    if (mathOutputLength > MAX_RENDERED_TREE_LENGTH) throw new RangeError('Math output too large')
    // Adapt only the fixed public KaTeX API, not its unstable internal DOM tree.
    // Validation also strips parse5 source metadata before Worker serialization.
    return validateMarkdownTree(fromHtml(html, { fragment: true })).children as Element[]
  }
  function allowedDestination(node: Link | Definition): boolean {
    // micromark replaces literal NUL with U+FFFD before constructing mdast.
    // Do not let that lossy normalization turn a rejected control URL into a link.
    const raw = source.slice(node.position?.start.offset, node.position?.end.offset)
    return !raw.includes('\0') && isAllowedMarkdownLink(node.url)
  }
  const handlers: NonNullable<Options['handlers']> = {
    math: (_state, node) => formula(node),
    inlineMath: (_state, node) => formula(node),
    // Visible literal HTML, NOT skipHtml (which silently discards authored text).
    html: (_state, node) => ({ type: 'text', value: node.value }),
    image: (_state, node) => ({ type: 'element', tagName: 'span', properties: { className: ['markdown-image'] },
      children: [{ type: 'text', value: `[Image: ${node.alt ?? ''}]` }] }),
    imageReference: (_state, node) => ({ type: 'element', tagName: 'span', properties: { className: ['markdown-image'] },
      children: [{ type: 'text', value: `[Image: ${node.alt ?? ''}]` }] }),
    linkReference: (state, node) => {
      const definition = state.definitionById.get(node.identifier.toUpperCase())
      const properties: Properties = definition && allowedDestination(definition)
        ? { href: definition.url, ...(definition.title ? { title: definition.title } : {}) } : {}
      return { type: 'element', tagName: properties.href ? 'a' : 'span', properties, children: state.all(node) }
    },
    link: (state, node) => {
      // Check mdast's decoded destination BEFORE URI normalization could hide
      // control characters. The independent receiver check uses the same policy.
      const properties: Properties = allowedDestination(node)
        ? { href: node.url, ...(node.title ? { title: node.title } : {}) } : {}
      return { type: 'element', tagName: properties.href ? 'a' : 'span', properties, children: state.all(node) }
    },
    code: (_state, node) => ({ type: 'element', tagName: 'pre', properties: {}, children: [{
      type: 'element', tagName: 'code',
      properties: node.lang && /^[a-z\d_+-]{1,32}$/i.test(node.lang) ? { className: [`language-${node.lang}`] } : {},
      children: [{ type: 'text', value: node.value + '\n' }],
    }] }),
  }
  const processor = unified().use(remarkParse).use(remarkGfm).use(remarkMathCompat)
    .use(function previewSyntax() {
      // The old preview has no footnote IDs/backlinks; leave that extra GFM
      // syntax literal rather than introduce DOM IDs, forms or English labels.
      const data = this.data()
      ;(data.micromarkExtensions ??= []).push({ disable: { null: ['gfmFootnoteDefinition', 'gfmFootnoteCall'] } })
      return (tree: Nodes) => {
        function tasks(node: Nodes) {
          if (node.type === 'listItem' && typeof node.checked === 'boolean') {
            const prefix = node.checked ? '[x] ' : '[ ] '
            const first = node.children[0]
            if (first?.type === 'paragraph') first.children.unshift({ type: 'text', value: prefix })
            else node.children.unshift({ type: 'paragraph', children: [{ type: 'text', value: prefix }] })
            delete node.checked
          }
          if ('children' in node) node.children.forEach(tasks)
        }
        tasks(tree)
      }
    })
    .use(remarkRehype, { handlers })
  const tree = validateMarkdownTree(processor.runSync(processor.parse(source)))
  // Also bound the complete wire representation (including node/prop overhead).
  if (JSON.stringify(tree).length > MAX_RENDERED_TREE_LENGTH) throw new RangeError('Tree output too large')
  return tree
}
