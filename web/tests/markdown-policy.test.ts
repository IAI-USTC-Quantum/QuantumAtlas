// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { act, createElement, StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { renderToStaticMarkup } from 'react-dom/server'
import Markdown from 'react-markdown'
import katex from 'katex'
import { MarkdownDocument } from '../src/components/markdown-document'
import { createMarkdownOptions } from '../src/lib/markdown-renderer'
import { checkMarkdownTreeBudget, isAllowedMarkdownLink } from '../src/lib/markdown-policy'
import {
  MAX_MARKDOWN_LENGTH, MAX_FORMULA_LENGTH, MAX_FORMULAS, MAX_MATH_HTML_LENGTH,
  MAX_RENDERED_DEPTH, MAX_RENDERED_NODES,
} from '../src/lib/markdown-limits'

type BudgetNode = Parameters<typeof checkMarkdownTreeBudget>[0]
const text = (value = 'x'): BudgetNode => ({ type: 'text', value })
const tree = (...children: BudgetNode[]): BudgetNode => ({ type: 'root', children })
const branch = (...children: BudgetNode[]): BudgetNode => ({ type: 'element', children })
const render = (source: string): string => renderToStaticMarkup(createElement(MarkdownDocument, { content: source }))

// These are resource checks on library-produced trees, not an untrusted wire
// decoder or a replacement HTML sanitizer. Real-source isolation is tested below
// and in markdown.test.ts through the application's actual Markdown component.
afterEach(() => {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
})

describe('application URL policy', () => {
  it.each([
    'https://example.test/path?q=1&b=2', 'HTTP://example.test',
    'https://例子.测试/path', 'mailto:x@example.test?subject=hello', '#section',
  ])('allows the explicit destination %s', (url) => {
    expect(isAllowedMarkdownLink(url)).toBe(true)
  })

  it.each([
    '', 'javascript:alert(1)', 'javascript&#58;alert(1)', 'data:text/html,evil',
    'file:///etc/passwd', 'ftp://example.test', 'irc://example.test', 'xmpp:x@example.test',
    '/api/delete', 'relative', '../admin', './asset', '?action=delete', '//example.test',
    'https:/example.test', 'https:example.test', 'https://',
    'https://user:pass@example.test', 'https://user@example.test',
    '\\example.test', 'https://example.test/a\\b',
    '\thttps://example.test', 'https://example.test\n', 'https://example.test/a b',
    'https://example.test/a\0b', 'https://example.test/\u0001', 'https://example.test/\u200b',
    'https://example.test/%00', 'https://example.test/%0A', 'https://example.test/%1f',
    'https://example.test/%7F', 'https://example.test/%5c', 'mailto:x@example.test?body=%0d%0aevil',
  ])('blocks the destination %s', (url) => {
    expect(isAllowedMarkdownLink(url)).toBe(false)
  })

  it.each(['\0', '\u0001', '\u200b', '&#9;', '&NewLine;', '%0a'])('checks direct and reference destinations before normalization: %j', (control) => {
    const url = `https://example.test/a${control}b`
    const source = `[direct](${url}) [reference][ref]\n\n[ref]: ${url}`
    const container = document.createElement('template')
    container.innerHTML = render(source)
    expect(container.content.querySelector('a, [href], [target], [referrerpolicy]')).toBeNull()
    expect(container.content.textContent).toContain('direct')
    expect(container.content.textContent).toContain('reference')
  })

  it('rebuilds external metadata but keeps fragments in the document', () => {
    const container = document.createElement('template')
    container.innerHTML = render('[external](https://example.test "title") [fragment](#section) [blocked](/api/delete "hidden title")')
    const [external, fragment] = container.content.querySelectorAll('a')
    expect(external.getAttribute('title')).toBe('title')
    expect(external.getAttribute('target')).toBe('_blank')
    expect(external.getAttribute('rel')).toBe('noopener noreferrer nofollow')
    expect(external.getAttribute('referrerpolicy')).toBe('no-referrer')
    expect(fragment.getAttribute('target')).toBeNull()
    expect(fragment.getAttribute('rel')).toBeNull()
    expect(fragment.getAttribute('referrerpolicy')).toBeNull()
    const blocked = container.content.querySelector('span')!
    expect(blocked.textContent).toBe('blocked')
    expect(blocked.hasAttribute('title')).toBe(false)
  })
})

describe('per-tree resource budgets', () => {
  it('counts the root and every descendant at the exact node limit', () => {
    const input = tree(...Array.from({ length: MAX_RENDERED_NODES - 1 }, () => text()))
    expect(checkMarkdownTreeBudget(input)).toBe(MAX_RENDERED_NODES)
    input.children!.push(text())
    expect(() => checkMarkdownTreeBudget(input)).toThrow(RangeError)
  })

  it('accumulates nodes across individually small sibling subtrees', () => {
    const input = tree(
      branch(...Array.from({ length: MAX_RENDERED_NODES / 2 }, () => text())),
      branch(...Array.from({ length: MAX_RENDERED_NODES / 2 }, () => text())),
    )
    expect(() => checkMarkdownTreeBudget(input)).toThrow(/structure too large/)
  })

  it('checks the exact depth boundary iteratively, before recursive conversion', () => {
    let input = text()
    for (let depth = 1; depth < MAX_RENDERED_DEPTH; depth++) input = branch(input)
    expect(checkMarkdownTreeBudget(tree(input))).toBe(MAX_RENDERED_DEPTH + 1)
    expect(() => checkMarkdownTreeBudget(tree(branch(input)))).toThrow(RangeError)
  })

  it('counts raw text, ordinary text and property arrays toward the character limit', () => {
    const leaf = text('c')
    const input = tree(
      { type: 'raw', value: 'x'.repeat(MAX_MATH_HTML_LENGTH - 4) },
      { type: 'element', properties: { className: ['a', 'b'] }, children: [leaf] },
    )
    // Raw nodes remain untouched for react-markdown to make visible text.
    expect(checkMarkdownTreeBudget(input)).toBe(4)
    leaf.value = 'cc'
    expect(() => checkMarkdownTreeBudget(input)).toThrow(/output too large/)
  })

  it('counts string property values as well as text', () => {
    const input = tree({ type: 'element', properties: { title: 'x'.repeat(MAX_MATH_HTML_LENGTH) } })
    expect(checkMarkdownTreeBudget(input)).toBe(2)
    input.children!.push(text())
    expect(() => checkMarkdownTreeBudget(input)).toThrow(/output too large/)
  })

  it('accumulates block and inline formulas only when preflighting Markdown', () => {
    const input = tree(...Array.from({ length: MAX_FORMULAS }, (_, index) => ({
      type: index % 2 ? 'inlineMath' : 'math', value: 'x',
    })))
    expect(checkMarkdownTreeBudget(input, true)).toBe(MAX_FORMULAS + 1)
    input.children!.push({ type: 'inlineMath', value: 'x' })
    expect(() => checkMarkdownTreeBudget(input, true)).toThrow(/Too many formulas/)
    expect(checkMarkdownTreeBudget(input)).toBe(MAX_FORMULAS + 2)
  })

  it('does not mutate library trees and starts fresh for each check', () => {
    const input = tree({ type: 'raw', value: '<script>text only</script>' }, branch(text()))
    const before = structuredClone(input)
    expect(checkMarkdownTreeBudget(input)).toBe(4)
    expect(checkMarkdownTreeBudget(input)).toBe(4)
    expect(input).toEqual(before)
  })
})

describe('budgets in the real react-markdown pipeline', () => {
  it('rejects oversized source before invoking KaTeX', () => {
    const engine = vi.spyOn(katex, 'renderToString')
    expect(() => createMarkdownOptions('x'.repeat(MAX_MARKDOWN_LENGTH + 1))).toThrow(/input too large/)
    expect(engine).not.toHaveBeenCalled()
  })

  it('rejects excessive unmatched TeX openers before tokenization, without counting escaped slashes', () => {
    const engine = vi.spyOn(katex, 'renderToString')
    for (const opener of ['\\(', '\\[']) {
      expect(() => createMarkdownOptions(opener.repeat(MAX_FORMULAS + 1))).toThrow(/delimiter openers/)
    }
    expect(() => createMarkdownOptions('\\\\('.repeat(MAX_FORMULAS + 1))).not.toThrow()
    expect(engine).not.toHaveBeenCalled()
  })

  it('preflights the exact formula-count limit before any rendering work', () => {
    const engine = vi.spyOn(katex, 'renderToString').mockReturnValue('<span>math</span>')
    const accepted = '$x$ '.repeat(MAX_FORMULAS)
    expect(render(accepted).match(/<span>math<\/span>/g)).toHaveLength(MAX_FORMULAS)
    expect(engine).toHaveBeenCalledTimes(MAX_FORMULAS)
    engine.mockClear()
    expect(() => render(accepted + '$x$')).toThrow(/Too many formulas/)
    expect(engine).not.toHaveBeenCalled()
  })

  it('rejects excessive parsed Markdown depth before invoking KaTeX', () => {
    const engine = vi.spyOn(katex, 'renderToString')
    expect(() => render('> '.repeat(MAX_RENDERED_DEPTH) + '$x$')).toThrow(/structure too large/)
    expect(engine).not.toHaveBeenCalled()
  })

  it('rejects excessive parsed Markdown nodes before invoking KaTeX', () => {
    const engine = vi.spyOn(katex, 'renderToString')
    const source = 'paragraph\n\n'.repeat(MAX_RENDERED_NODES / 2) + '$x$'
    expect(source.length).toBeLessThan(MAX_MARKDOWN_LENGTH)
    expect(() => render(source)).toThrow(/structure too large/)
    expect(engine).not.toHaveBeenCalled()
  })

  it('also checks the larger HAST produced from an accepted Markdown tree', () => {
    // Each paragraph has two mdast nodes; HAST also inserts separator text.
    const source = 'x\n\n'.repeat(7_000)
    const options = createMarkdownOptions(source)
    const afterPreflight = vi.fn()
    const afterHastBudget = vi.fn()
    options.remarkPlugins = [...options.remarkPlugins!, () => () => { afterPreflight() }]
    options.rehypePlugins = [...options.rehypePlugins!, () => () => { afterHastBudget() }]
    expect(() => renderToStaticMarkup(createElement(Markdown, options, source))).toThrow(/structure too large/)
    expect(afterPreflight).toHaveBeenCalledOnce()
    expect(afterHastBudget).not.toHaveBeenCalled()
  })

  it('allows the exact formula length and falls back before an oversized engine call', () => {
    const engine = vi.spyOn(katex, 'renderToString').mockReturnValue('<span>math</span>')
    expect(render('$' + 'x'.repeat(MAX_FORMULA_LENGTH) + '$')).toContain('<span>math</span>')
    expect(engine).toHaveBeenCalledOnce()
    engine.mockClear()
    expect(render('$' + 'x'.repeat(MAX_FORMULA_LENGTH + 1) + '$')).toContain('class="math-error"')
    expect(engine).not.toHaveBeenCalled()
  })

  it('accepts the exact generated-HTML limit and rejects the next character', () => {
    const output = '<span>' + 'x'.repeat(MAX_MATH_HTML_LENGTH - '<span></span>'.length) + '</span>'
    expect(output.length).toBe(MAX_MATH_HTML_LENGTH)
    const engine = vi.spyOn(katex, 'renderToString').mockReturnValue(output)
    expect(render('$x$').length).toBe(MAX_MATH_HTML_LENGTH + '<p></p>'.length)
    engine.mockReturnValue(output + 'x')
    expect(() => render('$x$')).toThrow(/Math output too large/)
  })

  it('accumulates generated HTML across formulas and stops before later calls', () => {
    const output = '<span>' + 'x'.repeat(MAX_MATH_HTML_LENGTH / 2) + '</span>'
    const engine = vi.spyOn(katex, 'renderToString').mockReturnValue(output)
    expect(() => render('$a$ $b$ $c$')).toThrow(/Math output too large/)
    expect(engine).toHaveBeenCalledTimes(2)
  })

  it('accumulates generated nodes across formulas and stops before later calls', () => {
    const output = '<span>x</span>'.repeat(4_000)
    const engine = vi.spyOn(katex, 'renderToString').mockReturnValue(output)
    expect(() => render('$a$ $b$ $c$ $d$')).toThrow(/Math structure too large/)
    expect(engine).toHaveBeenCalledTimes(3)
  })

  it('supplies a fresh macro object and bounded untrusted options for every formula', () => {
    const engine = vi.spyOn(katex, 'renderToString').mockReturnValue('<span>math</span>')
    render('$a$ $b$')
    const settings = engine.mock.calls.map(([, options]) => options!)
    expect(settings).toHaveLength(2)
    for (const options of settings) {
      expect(options).toMatchObject({
        trust: false, strict: 'error', throwOnError: true, output: 'htmlAndMathml',
        maxExpand: 1_000, maxSize: 20, macros: {},
      })
    }
    expect(settings[0].macros).not.toBe(settings[1].macros)
  })
})

describe('per-processing-run isolation with reused options', () => {
  it.each([
    { name: 'HTML characters', output: '<span>' + 'x'.repeat(MAX_MATH_HTML_LENGTH / 2) + '</span>' },
    { name: 'generated nodes', output: '<span>x</span>'.repeat(MAX_RENDERED_NODES / 4) },
  ])('resets $name even when the exact same options object is rendered repeatedly', ({ output }) => {
    const engine = vi.spyOn(katex, 'renderToString').mockReturnValue(output)
    const options = createMarkdownOptions('$x$')
    const document = createElement(Markdown, options, '$x$')
    const first = renderToStaticMarkup(document)
    expect(renderToStaticMarkup(document)).toBe(first)
    expect(engine).toHaveBeenCalledTimes(2)
  })

  it('resets budgets for StrictMode double rendering with identical Markdown props', async () => {
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    const value = 'x'.repeat(MAX_MATH_HTML_LENGTH / 2)
    const engine = vi.spyOn(katex, 'renderToString').mockReturnValue(`<span>${value}</span>`)
    const options = createMarkdownOptions('$x$')
    const container = document.createElement('div')
    document.body.append(container)
    const root = createRoot(container)
    try {
      await act(async () => {
        root.render(createElement(StrictMode, null, createElement(Markdown, options, '$x$')))
      })
      expect(engine).toHaveBeenCalledTimes(2)
      expect(container.querySelectorAll('span')).toHaveLength(1)
      expect(container.textContent).toBe(value)
    } finally {
      await act(async () => { root.unmount() })
      container.remove()
    }
  })

  it('recovers after a failed processing run using the same options object', () => {
    const engine = vi.spyOn(katex, 'renderToString')
      .mockReturnValueOnce('x'.repeat(MAX_MATH_HTML_LENGTH + 1))
      .mockReturnValue('<span>recovered</span>')
    const options = createMarkdownOptions('$x$')
    const document = createElement(Markdown, options, '$x$')
    expect(() => renderToStaticMarkup(document)).toThrow(/Math output too large/)
    expect(renderToStaticMarkup(document)).toBe('<p><span>recovered</span></p>')
    expect(engine).toHaveBeenCalledTimes(2)
  })
})
