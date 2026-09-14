import type { Extension as FromMarkdownExtension, Handle } from 'mdast-util-from-markdown'
import type { Code, Construct, Extension, State, Token, Tokenizer } from 'micromark-util-types'
import type { Processor } from 'unified'

// Type-only ecosystem registrations; no additional parser or renderer is loaded.
import type {} from 'mdast-util-math'
import type {} from 'remark-parse'
import type {} from 'remark-rehype'

declare module 'micromark-util-types' {
  interface TokenTypeMap {
    qatlasMathFlow: 'qatlasMathFlow'
    qatlasMathText: 'qatlasMathText'
    qatlasMathData: 'qatlasMathData'
  }
  interface Token {
    _qatlasMathDisplay?: boolean
    _qatlasMathMarkerSize?: number
  }
}

declare module 'mdast' {
  interface Data {
    /** Display math in a phrasing context must remain a valid inlineMath node. */
    mathDisplay?: boolean
  }
}

const dollar = 36
const backslash = 92

// micromark's public Code stream represents CR, LF and CRLF as -5, -4 and -3.
function lineEnding(code: Code): boolean {
  return code !== null && code >= -5 && code <= -3
}

function space(code: Code): boolean {
  return code === 32 || code === -2 || code === -1 // Space, tab, virtual tab space.
}

/**
 * A paired-delimiter construct, not a Markdown or TeX parser. micromark still
 * owns code, escapes, containers, links, GFM and backtracking. The two entry
 * points differ only in whether a display pair occupies its own flow block.
 *
 * Reviewed against micromark-extension-math 3's math-text/math-flow and
 * mdast-util-math 3's handlers. Independent named constructs are necessary:
 * upstream mathText crosses newlines and renders $$x$$ inline; mathFlow treats
 * same-line content as fence metadata and accepts unclosed fences. Falling back
 * to either after a failed compatibility match would reintroduce those bugs.
 */
function pairedMath(flow: boolean): Construct {
  const type = flow ? 'qatlasMathFlow' : 'qatlasMathText'
  const tokenize: Tokenizer = function (effects, ok, nok) {
    let token: Token
    let dollarSize = 0
    let texClose = 0
    let display = false
    const { events, previous, parser, now, interrupt } = this
    return start

    function start(code: Code): State | undefined {
      // Do not reinterpret the tail of an unsupported dollar run. An escaped
      // dollar and the closing dollar of a preceding formula are boundaries.
      const previousType = events[events.length - 1]?.[1].type
      if (!flow && code === dollar && previous === dollar &&
        previousType !== 'characterEscape' && previousType !== 'qatlasMathText') {
        return nok(code)
      }
      token = effects.enter(type)
      effects.enter('qatlasMathData')
      if (code === dollar) return openDollar(code)
      effects.consume(code)
      return openTex
    }

    function openDollar(code: Code): State | undefined {
      if (code === dollar) {
        dollarSize++
        effects.consume(code)
        return openDollar
      }
      // This app supports $ and $$, not arbitrary-length remark-math fences.
      if (dollarSize > 2 || (flow && dollarSize !== 2)) return nok(code)
      display = dollarSize === 2
      token._qatlasMathDisplay = display
      token._qatlasMathMarkerSize = dollarSize
      return inside(code)
    }

    function openTex(code: Code): State | undefined {
      if (code !== 91 && (flow || code !== 40)) return nok(code)
      display = code === 91
      texClose = display ? 93 : 41
      token._qatlasMathDisplay = display
      token._qatlasMathMarkerSize = 2
      effects.consume(code)
      return inside
    }

    function inside(code: Code): State | undefined {
      if (code === null || (!display && lineEnding(code))) return nok(code)
      if (lineEnding(code)) {
        effects.exit('qatlasMathData')
        return atLineEnding(code)
      }
      if (code === backslash) {
        effects.consume(code)
        return escaped
      }
      if (!texClose && code === dollar) {
        effects.consume(code)
        return dollarSize === 1 ? closed : closeDollar
      }
      effects.consume(code)
      return inside
    }

    function atLineEnding(code: Code): State | undefined {
      // An interrupt probe only has the current source line. Accept the
      // display opener there; the actual parse still requires a closing pair.
      if (flow && interrupt) {
        effects.exit(type)
        return ok(code)
      }
      effects.enter('lineEnding')
      effects.consume(code)
      effects.exit('lineEnding')
      return lineStart
    }

    function lineStart(code: Code): State | undefined {
      // Like upstream mathFlow, do not cross out of a quote/list through a
      // lazy continuation. Prefix stripping itself belongs to micromark.
      if (flow && parser.lazy[now().line]) return nok(code)
      if (lineEnding(code)) return atLineEnding(code)
      effects.enter('qatlasMathData')
      return inside(code)
    }

    function escaped(code: Code): State | undefined {
      if (texClose && code === texClose) {
        effects.consume(code)
        return closed
      }
      if (code === null || lineEnding(code)) return inside(code)
      // Consume an escaped pair atomically: \$ and \\ do not close math,
      // and \\] / \\) are not TeX closing delimiters.
      effects.consume(code)
      return inside
    }

    function closeDollar(code: Code): State | undefined {
      if (code !== dollar) return inside(code)
      effects.consume(code)
      return closed
    }

    function closed(code: Code): State | undefined {
      effects.exit('qatlasMathData')
      effects.exit(type)
      if (!flow) return ok(code)
      if (space(code)) {
        effects.enter('whitespace')
        return trailingSpace(code)
      }
      return endOfBlock(code)
    }

    function trailingSpace(code: Code): State | undefined {
      if (space(code)) {
        effects.consume(code)
        return trailingSpace
      }
      effects.exit('whitespace')
      return endOfBlock(code)
    }

    function endOfBlock(code: Code): State | undefined {
      // A suffix means this belongs to phrasing, where display mode is metadata
      // on inlineMath rather than a block math node nested inside a paragraph.
      return code === null || lineEnding(code) ? ok(code) : nok(code)
    }
  }
  return { name: type, tokenize, ...(flow ? { concrete: true } : {}) }
}

const enterMath: Handle = function (token) {
  const block = token.type === 'qatlasMathFlow'
  const display = token._qatlasMathDisplay === true
  const markerSize = token._qatlasMathMarkerSize ?? 2
  // Serialize only this token's micromark stream (container prefixes have
  // already been removed). Never rewrite/scan the whole source. node.position
  // remains untouched, so rendering failures can recover the EXACT source with
  // source.slice(node.position.start.offset, node.position.end.offset), even
  // with CRLF, whitespace, escapes, and the original choice of delimiters.
  const value = this.sliceSerialize(token).slice(markerSize, -markerSize).trim()
  const children = [{ type: 'text' as const, value }]
  const properties = { className: ['language-math', display ? 'math-display' : 'math-inline'] }
  this.enter(block ? {
    type: 'math', value, meta: null,
    data: {
      mathDisplay: true, hName: 'pre',
      hChildren: [{ type: 'element', tagName: 'code', properties, children }],
    },
  } : {
    type: 'inlineMath', value,
    data: { mathDisplay: display, hName: 'code', hProperties: properties, hChildren: children },
  }, token)
}

const exitMath: Handle = function (token) {
  this.exit(token)
}

/**
 * Use with remark-parse: unified().use(remarkParse).use(remarkMathCompat).
 * A micromark/mdast application dialect emitting standard math/inlineMath nodes,
 * with data.mathDisplay for display spans. No remark-math registration is needed:
 * its conflicting constructs would be unused and its package brings another
 * KaTeX version. mdast-util-math supplies standard node definitions as types only.
 * This is parsing-only: stock math serialization does not preserve these
 * delimiters or inline display metadata.
 */
export default function remarkMathCompat(this: Processor): void {
  const data = this.data()
  const flow = pairedMath(true)
  const text = pairedMath(false)
  const syntax: Extension = {
    // Defensive only: do not re-enable conflicting defaults if another consumer
    // also registers stock math. This plugin does not depend on that extension.
    disable: { null: ['mathFlow', 'mathText'] },
    flow: { [dollar]: flow, [backslash]: flow },
    text: { [dollar]: text, [backslash]: text },
  }
  const fromMarkdown: FromMarkdownExtension = {
    enter: { qatlasMathFlow: enterMath, qatlasMathText: enterMath },
    exit: { qatlasMathFlow: exitMath, qatlasMathText: exitMath },
  }
  ;(data.micromarkExtensions ??= []).push(syntax)
  ;(data.fromMarkdownExtensions ??= []).push(fromMarkdown)
}
