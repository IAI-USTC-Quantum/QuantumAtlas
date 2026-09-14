// @vitest-environment node
import { describe, expect, it } from 'vitest'
import { unified } from 'unified'
import remarkParse from 'remark-parse'
import remarkGfm from 'remark-gfm'
import remarkRehype from 'remark-rehype'
import type { Root, RootContent } from 'mdast'
import remarkMathCompat from '../src/lib/remark-math-compat'

type MathNode = Extract<RootContent, { type: 'math' | 'inlineMath' }>
const parser = unified().use(remarkParse).use(remarkGfm).use(remarkMathCompat)

function mathNodes(source: string): Array<MathNode> {
  const found: Array<MathNode> = []
  function walk(node: Root | RootContent) {
    if (node.type === 'inlineMath' || node.type === 'math') found.push(node)
    if ('children' in node) for (const child of node.children) walk(child)
  }
  walk(parser.parse(source))
  return found
}

function isDisplay(node: MathNode): boolean {
  return node.type === 'math' || node.data?.mathDisplay === true
}

function original(source: string, node: MathNode): string {
  return source.slice(node.position!.start.offset, node.position!.end.offset)
}

describe('remark math compatibility syntax', () => {
  it('parses single-dollar and TeX inline formulas adjacent to Chinese', () => {
    const nodes = mathNodes(String.raw`能量$E=mc^2$守恒，向量\(\alpha+\beta\)结束。`)
    expect(nodes.map((node) => node.value)).toEqual(['E=mc^2', String.raw`\alpha+\beta`])
    expect(nodes.every((node) => node.type === 'inlineMath' && !isDisplay(node))).toBe(true)
  })

  it.each([
    '$$x^2$$', '$$\nx^2\n$$', '$$x^2\n+1$$',
    String.raw`\[x^2\]`, '\\[\nx^2\n\\]',
    'before\n$$\nx^2\n$$\nafter',
  ])('parses paired block display syntax: %s', (source) => {
    const nodes = mathNodes(source)
    expect(nodes).toHaveLength(1)
    expect(nodes[0].type).toBe('math')
    expect(isDisplay(nodes[0])).toBe(true)
  })

  it('retains display mode without invalid block nodes in phrasing content', () => {
    const nodes = mathNodes(String.raw`行内\(a\)邻接，显示\[\frac{a}{b}\]结束，美元$$x^2$$结尾。`)
    expect(nodes.map((node) => node.type)).toEqual(['inlineMath', 'inlineMath', 'inlineMath'])
    expect(nodes.map(isDisplay)).toEqual([false, true, true])
    expect(nodes[1].data?.hProperties?.className).toContain('math-display')
    expect(nodes[2].data?.hProperties?.className).toContain('math-display')
  })

  it.each(['\n', '\r\n', '\r'])('does not let an unmatched inline dollar consume a later formula across %j', (newline) => {
    const source = `a $${newline}b $x$后`
    const nodes = mathNodes(source)
    expect(nodes.map((node) => node.value)).toEqual(['x'])
    expect(original(source, nodes[0])).toBe('$x$')
  })

  it('also keeps TeX inline math on one source line', () => {
    const nodes = mathNodes('a \\(bad\nb \\(x\\)后')
    expect(nodes.map((node) => node.value)).toEqual(['x'])
  })

  it('leaves inline, multiple-backtick, fenced and indented code opaque', () => {
    const content = String.raw`$x$ $$y$$ \(a\) \[b\]`
    const source = '`' + content + '`\n\n``code `' + content + '` ``\n\n```tex\n' + content + '\n```\n\n    ' + content + '\n\n~~~\n' + content + '\n~~~'
    expect(mathNodes(source)).toEqual([])
  })

  it('leaves backslash-escaped openers literal and does not turn entities into syntax', () => {
    expect(mathNodes(String.raw`\$x\$ costs \$5; \\(y\\) and \\[z\\]. &#36;x&#36; &#92;(a&#92;)`)).toEqual([])
  })

  it('keeps escaped closing delimiters inside the current formula', () => {
    const nodes = mathNodes(String.raw`$a\$b$ \(a\\)b\) \[a\\]b\] $$a\$$b$$`)
    expect(nodes.map((node) => node.value)).toEqual([
      String.raw`a\$b`, String.raw`a\\)b`, String.raw`a\\]b`, String.raw`a\$$b`,
    ])
  })

  it.each([
    '$\\bad{}$', '$$\\bad{}$$', '$$\r\n  \\bad{}\r\n$$',
    String.raw`\( \bad{} \)`, '\\[\r\n  \\bad{}\r\n\\]',
  ])('keeps exact delimiter spelling and source offsets for render fallback: %s', (formula) => {
    const source = '中文😀前缀\r\n\r\n' + formula + '\r\n\r\n后缀'
    const [node] = mathNodes(source)
    expect(original(source, node)).toBe(formula)
    expect(node.value).toBe(String.raw`\bad{}`)
  })

  it('does not promote unmatched display openers into math-to-end-of-file', () => {
    expect(mathNodes('$$ no closing\ntext')).toEqual([])
    expect(mathNodes('\\[ no closing\ntext')).toEqual([])
  })

  it('coexists with GFM table and emphasis syntax', () => {
    const source = '| A | B |\n| --- | --- |\n| $x$ | \\(y\\) |\n\n**bold $z$**'
    expect(mathNodes(source).map((node) => node.value)).toEqual(['x', 'y', 'z'])
  })

  it.each([
    ['> $$\n> x^2\n> +1$$', 'x^2\n+1'],
    ['> \\[\n> x^2\n> +1\\]', 'x^2\n+1'],
    ['- $$\n  x^2\n  +1$$', 'x^2\n+1'],
    ['- item\n\n  \\[\n  x^2\n  +1\\]', 'x^2\n+1'],
    ['   $$\n  x^2\n+1$$  \t\n', 'x^2\n+1'],
  ])('lets micromark own block container prefixes: %s', (source, value) => {
    const nodes = mathNodes(source)
    expect(nodes).toHaveLength(1)
    expect(nodes[0].type).toBe('math')
    expect(nodes[0].value).toBe(value)
  })

  it('keeps internal line endings, tabs and entity spellings unmodified', () => {
    const source = '$$\r\na\t+b\r\n&copy;\r\n$$'
    const [node] = mathNodes(source)
    expect(node.value).toBe('a\t+b\r\n&copy;')
    expect(original(source, node)).toBe(source)
  })

  it('supports a paired display with blank lines without parsing TeX as Markdown', () => {
    const source = '$$\na\n\n# b\n- c\n$$'
    const [node] = mathNodes(source)
    expect(node.type).toBe('math')
    expect(node.value).toBe('a\n\n# b\n- c')
  })

  it('keeps display spans with trailing prose in phrasing context, including multiline', () => {
    const nodes = mathNodes('$$a\n+b$$中文\n\n显示\\[a\n+b\\]后')
    expect(nodes.map((node) => node.type)).toEqual(['inlineMath', 'inlineMath'])
    expect(nodes.map(isDisplay)).toEqual([true, true])
    expect(nodes.map((node) => node.value)).toEqual(['a\n+b', 'a\n+b'])
  })

  it('preserves immediately adjacent formulas and escaped-dollar boundaries', () => {
    const nodes = mathNodes(String.raw`$x$$y$ \\ $z$ \$$q$ \\\(w\)`)
    expect(nodes.map((node) => node.value)).toEqual(['x', 'y', 'z', 'q', 'w'])
  })

  it('leaves unsupported long dollar runs as text rather than activating stock math', () => {
    expect(mathNodes('$$$x$$$\n\n$$$$\nx\n$$$$')).toEqual([])
  })

  it('does not parse math inside Markdown link destinations, titles or HTML attributes', () => {
    const source = String.raw`[label](https://example.test/\(path\) "$title$") <span title="$x$ \(y\)">text</span>`
    expect(mathNodes(source)).toEqual([])
  })

  it('handles delimiter-heavy malformed input without emitting partial math nodes', () => {
    // Deterministic small syntax stress test, not a replacement for Worker time
    // and document-size budgets. Every emitted node must have a real pair.
    const alphabet = ['$', '\\', '(', ')', '[', ']', '\n', '\r\n', ' ', '`', 'x', '> ', '- ', '\t']
    let state = 0x12345678
    for (let sample = 0; sample < 150; sample++) {
      let source = ''
      for (let i = 0; i < 40; i++) {
        state = (Math.imul(state, 1664525) + 1013904223) >>> 0
        source += alphabet[state % alphabet.length]
      }
      for (const node of mathNodes(source)) {
        const raw = original(source, node)
        const delimiters = raw.startsWith('\\(') ? ['\\(', '\\)']
          : raw.startsWith('\\[') ? ['\\[', '\\]']
          : isDisplay(node) ? ['$$', '$$'] : ['$', '$']
        expect(raw.startsWith(delimiters[0])).toBe(true)
        expect(raw.endsWith(delimiters[1])).toBe(true)
        if (!isDisplay(node)) expect(raw).not.toMatch(/[\r\n]/)
      }
    }
  })

  it('retains standard math hooks when handed to remark-rehype', () => {
    const processor = parser().use(remarkRehype)
    const tree = processor.runSync(processor.parse(String.raw`前\[x\]后`))
    expect(tree.children[0]).toMatchObject({
      type: 'element', tagName: 'p', children: [
        { type: 'text', value: '前' },
        {
          type: 'element', tagName: 'code',
          properties: { className: ['language-math', 'math-display'] },
          children: [{ type: 'text', value: 'x' }],
        },
        { type: 'text', value: '后' },
      ],
    })
  })

  it('keeps ordinary remark processors isolated from the compatibility plugin', () => {
    const standard = unified().use(remarkParse)
    expect((standard.parse('$$x$$').children[0] as { type: string }).type).toBe('paragraph')
    expect(mathNodes('$$x$$')[0].type).toBe('math')
    expect((standard.parse('$$x$$').children[0] as { type: string }).type).toBe('paragraph')
  })
})
