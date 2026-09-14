import type { Element, Properties, Root, RootContent } from 'hast'
import { MAX_RENDERED_DEPTH, MAX_RENDERED_NODES, MAX_RENDERED_TREE_LENGTH } from './markdown-protocol'

const htmlTags = new Set('p br hr h1 h2 h3 h4 h5 h6 blockquote ul ol li strong em del s pre code a span table thead tbody tr th td'.split(' '))
const mathTags = new Set('math semantics annotation mrow mi mo mn mtext mspace msup msub msubsup mfrac msqrt mroot mstyle mover munder munderover mtable mtr mtd mpadded mphantom menclose'.split(' '))
const svgTags = new Set(['svg', 'path', 'line'])
const mathProperties = new Set('display encoding mathvariant mathsize mathcolor mathbackground displaystyle scriptlevel stretchy fence separator minsize maxsize movablelimits largeop accent accentunder columnalign columnlines columnspacing rowalign rowlines rowspacing linethickness notation linebreak width height depth lspace rspace voffset'.split(' '))
const svgProperties = new Set('width height viewBox preserveAspectRatio d x1 x2 y1 y2 fill stroke strokeWidth'.split(' '))
const layoutClasses = new Set(`mord mop mbin mrel mopen mclose mpunct minner mspace msupsub mfrac mover munder mtable mtr-glue
textbf textit textrm textsf texttt textbb textfrak textboldfrak textscr textboldsf textitsf
mathnormal mathit mathrm mathbf boldsymbol amsrm mathbb mathcal mathfrak mathboldfrak mathtt mathscr mathsf mathboldsf mathsfit mathitsf mainrm
vlist-t vlist-r vlist vlist-t2 vlist-s pstrut frac-line overline-line underline-line sqrt
llap rlap clap fontsize-ensurer delimsizing mult delim-size1 delim-size4 nulldelimiter delimcenter
op-symbol small-op large-op op-limits accent-body accent-full vertical-separator arraycolsep col-align-c col-align-l col-align-r
svg-align hide-tail halfarrow-left halfarrow-right brace-left brace-center brace-right x-arrow-pad cd-arrow-pad x-arrow boxpad fbox fcolorbox cancel-pad cancel-lap angl anglpad reflectbox eqn-num mml-eqn-num cd-vert-arrow cd-label-left cd-label-right leqno fleqn`.split(/\s+/))
const styleProperties = new Set(`color background-color height width min-width max-width min-height max-height top bottom left right vertical-align
margin margin-left margin-right margin-top margin-bottom padding padding-left padding-right padding-top padding-bottom
border border-width border-style border-color border-top-width border-bottom-width border-left-width border-right-width border-right-style
font-size text-align text-shadow`.split(/\s+/))

export function isAllowedMarkdownLink(value: string): boolean {
  if (!value || /[\s\p{Cc}\p{Cf}\\]/u.test(value) || /%(?:0[\da-f]|1[\da-f]|7f|5c)/i.test(value)) return false
  if (value.startsWith('#')) return true
  if (/^mailto:[^?]+/i.test(value)) return true
  if (!/^https?:\/\//i.test(value)) return false
  try {
    const url = new URL(value)
    return Boolean(url.hostname) && !url.username && !url.password
  } catch {
    return false
  }
}

// Only the existing numerical/color layout grammar, never functions, escapes,
// variables, URLs, @rules or arbitrary CSS. The JSX runtime converts this to a
// React style object; we never spread a Worker-supplied style/props object.
function safeStyle(style: string): string {
  return style.split(';').flatMap((declaration) => {
    const separator = declaration.indexOf(':')
    if (separator === -1) return []
    const property = declaration.slice(0, separator).trim().toLowerCase()
    const value = declaration.slice(separator + 1).trim()
    if (property === 'position') return value === 'relative' ? ['position:relative'] : []
    if (!styleProperties.has(property) || !/^[\w\s.#%,+-]+$/.test(value)) return []
    return [`${property}:${value}`]
  }).join(';')
}

function object(value: unknown): Record<string, unknown> {
  if (!value || typeof value !== 'object' || Array.isArray(value)
    || ![Object.prototype, null].includes(Object.getPrototypeOf(value))) throw new Error('Invalid tree object')
  return value as Record<string, unknown>
}

function properties(value: unknown, tag: string, space: 'html' | 'math' | 'svg'): Properties {
  const input = object(value)
  const output: Properties = {}
  if (Object.keys(input).length > 40) throw new Error('Too many tree properties')
  for (const [key, raw] of Object.entries(input)) {
    if (key === 'className') {
      if (!Array.isArray(raw) || raw.length > 16 || !raw.every((item) => typeof item === 'string' && item.length <= 64)) {
        throw new Error('Invalid tree classes')
      }
      const classes = raw.filter((name: string) => tag === 'code'
        ? name === 'math-error' || /^language-[a-z\d_+-]{1,32}$/i.test(name)
        : tag === 'span' && (name === 'markdown-image' || name === 'katex' || /^katex-[a-z-]+$/.test(name)
          || /^(?:reset-size|size)(?:[1-9]|1[01])$/.test(name) || layoutClasses.has(name)))
      if (classes.length) output.className = classes
      continue
    }
    if (key === 'rel' && tag === 'a' && space === 'html' && Array.isArray(raw)
      && raw.every((item) => typeof item === 'string')) continue // Replaced below.
    // No objects/functions/arrays, React special props, event handlers, resource
    // attributes, custom-element `is`, data/ARIA wildcards or DOM-clobbering IDs.
    if (typeof raw !== 'string' && typeof raw !== 'number' && typeof raw !== 'boolean') throw new Error('Invalid tree property value')
    const text = String(raw)
    if (text.length > 100_000) throw new RangeError('Tree property too large')
    if (key === 'style' && ((space === 'html' && tag === 'span') || (space === 'svg' && tag === 'svg')
      || (space === 'math' && tag === 'mpadded'))) {
      const style = safeStyle(text)
      if (style) output.style = style
    } else if (key === 'ariaHidden' && space === 'html' && tag === 'span') {
      if (text === 'true') output.ariaHidden = 'true'
    } else if (space === 'html' && tag === 'a' && ['href', 'title', 'target', 'rel', 'referrerPolicy'].includes(key)) {
      if (key === 'href' && isAllowedMarkdownLink(text)) output.href = text
      if (key === 'title') output.title = text
      // Caller-supplied target/rel/referrerPolicy are replaced below.
    } else if (key === 'start' && space === 'html' && tag === 'ol') {
      if (/^\d{1,9}$/.test(text)) output.start = Number(text)
    } else if (key === 'align' && space === 'html' && ['th', 'td'].includes(tag)) {
      if (['left', 'right', 'center'].includes(text)) output.align = text
    } else if (key === 'xmlns' && (tag === 'math' || tag === 'svg')) {
      output.xmlns = tag === 'math' ? 'http://www.w3.org/1998/Math/MathML' : 'http://www.w3.org/2000/svg'
    } else if ((space === 'math' && mathProperties.has(key)) || (space === 'svg' && svgProperties.has(key))) {
      // These are inert presentation attributes, not href/style/annotation-xml.
      if (['fill', 'stroke', 'mathcolor', 'mathbackground'].includes(key)) {
        if (/^(?:#[\da-f]{3,8}|[a-z]+)$/i.test(text)) output[key] = text
      } else if (key === 'd') {
        if (/^[MmZzLlHhVvCcSsQqTtAaEe\d\s.,+-]*$/.test(text)) output[key] = text
      } else if (key === 'encoding') {
        if (tag === 'annotation' && text === 'application/x-tex') output[key] = text
      } else if (/^[\w\s.#%,+-]+$/.test(text)) output[key] = text
    } else {
      throw new Error('Unsupported tree property')
    }
  }
  if (typeof output.href === 'string' && !output.href.startsWith('#')) {
    output.target = '_blank'
    output.rel = ['noopener', 'noreferrer', 'nofollow']
    output.referrerPolicy = 'no-referrer'
  }
  return output
}

/**
 * Decode a minimal inert HAST subset, rebuilding nodes/properties from scratch.
 * Used before serialization in the Worker AND independently on the receiver.
 * Not a permissive HTML sanitizer: unsupported nodes/props fail closed. Metadata
 * (including position/data) is never copied and no raw/MDX node can reach JSX.
 */
export function validateMarkdownTree(value: unknown): Root {
  let count = 0
  let textLength = 0
  function visit(value: unknown, depth: number, space: 'html' | 'math' | 'svg'): Root | RootContent {
    if (++count > MAX_RENDERED_NODES || depth > MAX_RENDERED_DEPTH) throw new RangeError('Tree budget exceeded')
    const node = object(value)
    if (node.type === 'text') {
      if (typeof node.value !== 'string') throw new Error('Invalid tree text')
      textLength += node.value.length
      if (textLength > MAX_RENDERED_TREE_LENGTH) throw new RangeError('Tree text too large')
      return { type: 'text', value: node.value }
    }
    if (!Array.isArray(node.children) || node.children.length > MAX_RENDERED_NODES) throw new Error('Invalid tree children')
    if (node.type === 'root' && depth === 0) {
      return { type: 'root', children: node.children.map((child) => visit(child, depth + 1, 'html') as RootContent) }
    }
    if (node.type !== 'element' || typeof node.tagName !== 'string') throw new Error('Unsupported tree node')
    const tag = node.tagName
    const nextSpace = space === 'html' && tag === 'math' ? 'math' : space === 'html' && tag === 'svg' ? 'svg' : space
    if (!(nextSpace === 'html' ? htmlTags : nextSpace === 'math' ? mathTags : svgTags).has(tag)) {
      throw new Error('Unsupported tree element or namespace')
    }
    if (nextSpace === 'html' && (tag === 'br' || tag === 'hr') && node.children.length) {
      throw new Error('Void tree element has children')
    }
    const props = properties(node.properties, tag, nextSpace)
    if (tag === 'a' && !props.href) delete props.title
    // A blocked Markdown link retains its label but is not a phantom anchor.
    const result: Element = { type: 'element', tagName: tag === 'a' && !props.href ? 'span' : tag,
      properties: props, children: node.children.map((child) => visit(child, depth + 1, nextSpace) as Element['children'][number]) }
    return result
  }
  const root = visit(value, 0, 'html')
  if (root.type !== 'root') throw new Error('Expected tree root')
  return root
}

export function decodeMarkdownReply(value: unknown): Root {
  const reply = object(value)
  if (reply.ok !== true || typeof reply.treeJson !== 'string') throw new Error('Invalid Worker reply')
  if (reply.treeJson.length > MAX_RENDERED_TREE_LENGTH) throw new RangeError('Tree message too large')
  return validateMarkdownTree(JSON.parse(reply.treeJson))
}
