// @vitest-environment jsdom
import { afterEach, describe, expect, it, vi } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { MAX_MARKDOWN_LENGTH, renderMarkdownToTree } from '../src/lib/markdown-renderer'
import { markdownReplyToReact } from '../src/lib/markdown-react'

function renderHtml(source: string): string {
  return renderToStaticMarkup(markdownReplyToReact({ ok: true, treeJson: JSON.stringify(renderMarkdownToTree(source)) }))
}

function fragment(html: string): DocumentFragment {
  const template = document.createElement('template')
  template.innerHTML = html
  return template.content
}

function render(source: string): DocumentFragment {
  return fragment(renderHtml(source))
}

function expectInert(root: DocumentFragment): void {
  expect(root.querySelector('script, style, iframe, object, embed, form, input, button, textarea, select, img, image, video, audio, source, link, meta, base, foreignObject, use, animate, set')).toBeNull()
  for (const element of root.querySelectorAll('*')) {
    for (const attribute of element.attributes) {
      expect(attribute.name).not.toMatch(/^(?:on|src$|srcset$|xlink:href$|ping$|action$|formaction$|id$|name$)/i)
      if (attribute.name === 'style') expect(attribute.value).not.toMatch(/url\s*\(|expression|javascript:|\\|@/i)
    }
  }
}

afterEach(() => vi.unstubAllGlobals())

describe('pure Markdown and KaTeX rendering', () => {
  it('renders inline math adjacent to Chinese without spaces', () => {
    const root = render('能量$E=mc^2$守恒，向量$\\vec{x}$结束。')
    expect(root.querySelectorAll('.katex')).toHaveLength(2)
    expect(root.querySelectorAll('math')).toHaveLength(2)
    expect(root.textContent).toContain('能量')
    expect(root.textContent).toContain('守恒')
    expect(root.querySelector('annotation')?.textContent).toBe('E=mc^2')
    expect(root.querySelector('.katex-html')?.getAttribute('aria-hidden')).toBe('true')
  })

  it.each([
    '$$x^2$$', '$$\nx^2\n$$', '$$x^2\n+1$$',
    String.raw`\[x^2\]`, '\\[\nx^2\n\\]',
    'before\n$$\nx^2\n$$\nafter',
  ])('renders display math: %s', (source) => {
    const root = render(source)
    expect(root.querySelectorAll('.katex-display')).toHaveLength(1)
    expect(root.querySelector('math')?.getAttribute('display')).toBe('block')
  })

  it('supports TeX delimiters inline and next to Chinese', () => {
    const root = render(String.raw`行内\(\alpha+\beta\)邻接，显示\[\frac{a}{b}\]结束。`)
    expect(root.querySelectorAll('.katex')).toHaveLength(2)
    expect(root.querySelectorAll('.katex-display')).toHaveLength(1)
    expect(root.querySelector('mfrac')).not.toBeNull()
  })

  it('does not lose a later formula after an unmatched dollar', () => {
    const root = render('a $\nb $x$后')
    expect(root.querySelectorAll('.katex')).toHaveLength(1)
    expect(root.querySelector('annotation')?.textContent).toBe('x')
  })

  it('preserves tables, headings, nested lists, tasks, and other GFM', () => {
    const root = render('# Heading\n\n| A | B |\n| :--- | ---: |\n| $x$ | **bold** |\n\n- first\n  - child\n- [x] done\n- [ ] todo\n\n3. third\n4. fourth\n\n> quote\n\n~~old~~')
    expect(root.querySelector('h1')?.textContent).toBe('Heading')
    expect(root.querySelectorAll('table tbody td')).toHaveLength(2)
    expect(root.querySelector('td .katex')).not.toBeNull()
    expect(root.querySelector('table strong')?.textContent).toBe('bold')
    expect(root.querySelector('ul ul li')?.textContent).toBe('child')
    expect(root.querySelector('ol')?.getAttribute('start')).toBe('3')
    expect(root.querySelector('blockquote')?.textContent).toContain('quote')
    expect(root.querySelector('del')?.textContent).toBe('old')
    expect(root.textContent).toContain('[x] done')
    expect(root.querySelector('input')).toBeNull()
  })

  it.each(['tex', 'math'])('leaves all math delimiters and raw HTML in %s fenced code literal', (language) => {
    const code = String.raw`$x$ $$y$$ \(a\) \[b\] <img src="https://evil.test/x"> &amp;`
    const root = render('```' + language + '\n' + code + '\n```')
    expect(root.querySelector('.katex')).toBeNull()
    expect(root.querySelector('code')?.textContent).toBe(code + '\n')
    expect(root.querySelector('code')?.className).toBe(`language-${language}`)
    expectInert(root)
  })

  it('leaves inline, multiple-backtick, indented and tilde code alone', () => {
    const source = String.raw`inline: \(a\) and ` + '`$x$ \\(z\\)`' + '\n\n``code `$y$` \\[a\\]``\n\n    $$z$$\n\n~~~\n\\(w\\)\n~~~'
    const root = render(source)
    expect(root.querySelectorAll('.katex')).toHaveLength(1)
    expect([...root.querySelectorAll('code')].map((code) => code.textContent)).toEqual([
      '$x$ \\(z\\)', 'code `$y$` \\[a\\]', '$$z$$\n', '\\(w\\)\n',
    ])
  })

  it('preserves escaped dollar signs and escaped TeX openers', () => {
    const root = render(String.raw`\$x\$ costs \$5; \\(y\\) and \\[z\\].`)
    expect(root.querySelector('.katex')).toBeNull()
    expect(root.textContent).toBe('$x$ costs $5; \\(y\\) and \\[z\\].')
  })

  it('does not allow fence language text to inject attributes or classes', () => {
    const root = render('```x"onmouseover="alert(1)\n<x>\n```')
    expect(root.querySelector('code')?.getAttribute('class')).toBeNull()
    expectInert(root)
  })

  it('renders synchronously with no window or document and changes no source', () => {
    const source = '# 标题\r\n\r\n$x$ and `\\(y\\)`'
    const copy = source.slice()
    vi.stubGlobal('window', undefined)
    vi.stubGlobal('document', undefined)
    const html = renderHtml(source)
    expect(typeof html).toBe('string')
    expect(html).toContain('class="katex"')
    expect(source).toBe(copy)
    expect(renderHtml(source)).toBe(html)
  })

  it('produces deterministic plain serializable HAST, without raw nodes or source metadata', () => {
    const tree = renderMarkdownToTree('<b>raw</b> and $x$')
    expect(JSON.parse(JSON.stringify(tree))).toEqual(tree)
    expect(JSON.stringify(tree)).not.toMatch(/"(?:position|data|raw|mdxJsxFlowElement)":/)
    expect(renderMarkdownToTree('<b>raw</b> and $x$')).toEqual(tree)
  })

  it('leaves footnote notation literal without generated IDs or an English footnote section', () => {
    const root = render('Text[^note].\n\n[^note]: Note content.')
    expect(root.textContent).toContain('Text[^note].')
    expect(root.textContent).toContain('[^note]: Note content.')
    expect(root.querySelector('section, [id], a')).toBeNull()
  })

  it('does not retain reference links across documents', () => {
    expect(render('[x][ref]\n\n[ref]: https://example.test').querySelector('a')).not.toBeNull()
    expect(render('[x][ref]').querySelector('a')).toBeNull()
  })
})

describe('bounded and isolated untrusted TeX', () => {
  it('falls back to escaped original source and continues after invalid TeX', () => {
    const bad = String.raw`$\noSuchCommand{<img src=x onerror=alert(1)>}$`
    const root = render('Before ' + bad + ' after $y$')
    expect(root.querySelector('.math-error')?.textContent).toBe(bad)
    expect(root.querySelectorAll('.katex')).toHaveLength(1)
    expect(root.textContent).not.toContain('ParseError')
    expectInert(root)
  })

  it('does not leak gdef macros between formulas in one document', () => {
    const root = render(String.raw`$\gdef\qaleak{LEAK}\qaleak$ then $\qaleak$`)
    expect(root.querySelectorAll('.katex')).toHaveLength(1)
    expect(root.querySelector('.math-error')?.textContent).toBe(String.raw`$\qaleak$`)
  })

  it('does not leak global macro assignments across documents or built-ins', () => {
    renderHtml(String.raw`$\gdef\qaleak{LEAK}\global\let\alpha=\beta$`)
    expect(render(String.raw`$\qaleak$`).querySelector('.math-error')).not.toBeNull()
    expect(render(String.raw`$\alpha$`).querySelector('mi')?.textContent).toBe('α')
  })

  it.each([
    String.raw`\includegraphics{https://evil.test/pixel}`,
    String.raw`\href{https://evil.test}{CLICK}`,
    String.raw`\href{javascript:alert(1)}{CLICK}`,
    String.raw`\url{https://evil.test}`,
    String.raw`\htmlStyle{background-image:url(https://evil.test/pixel)}{x}`,
    String.raw`\htmlClass{evil-class}{x}`,
    String.raw`\htmlId{evil-id}{x}`,
    String.raw`\htmlData{evil=1}{x}`,
  ])('disables trust-only commands: %s', (formula) => {
    const root = render('$' + formula + '$')
    expect(root.querySelector('a, img, [id], [data-evil], .evil-class')).toBeNull()
    expectInert(root)
  })

  it('bounds recursive macro expansion nonfatally', () => {
    const root = render(String.raw`$\def\loop{\loop}\loop$ after $x$`)
    expect(root.querySelector('.math-error')).not.toBeNull()
    expect(root.querySelectorAll('.katex')).toHaveLength(1)
  })

  it('limits explicit TeX dimensions', () => {
    const root = render(String.raw`$\rule{999999em}{999999em}$`)
    const styles = [...root.querySelectorAll('[style]')].map((element) => element.getAttribute('style')).join(' ')
    expect(styles).not.toContain('999999')
    expect(styles).toContain('20em')
  })

  it.each(['$', '$$', '\\('])('falls back before rendering formulas longer than 10k: %s', (delimiter) => {
    const ending = delimiter === '\\(' ? '\\)' : delimiter
    const source = delimiter + 'x'.repeat(10_001) + ending
    const root = render(source)
    expect(root.querySelector('.katex')).toBeNull()
    expect(root.querySelector('.math-error')?.textContent?.trim()).toBe(source)
  })

  it('accepts the exact document limit and rejects oversize before parsing', () => {
    expect(MAX_MARKDOWN_LENGTH).toBe(200_000)
    expect(render('x'.repeat(MAX_MARKDOWN_LENGTH)).querySelector('p')?.textContent).toBe('x'.repeat(MAX_MARKDOWN_LENGTH))
    expect(() => renderHtml('x'.repeat(MAX_MARKDOWN_LENGTH + 1))).toThrow(RangeError)
  })
})

describe('source HTML and image isolation', () => {
  it.each([
    '<script>alert(document.domain)</script>',
    '<img src="https://evil.test/pixel" onerror="alert(1)">',
    '<svg onload="alert(1)"><a xlink:href="javascript:alert(1)">x</a></svg>',
    '<math><mtext><img src=x onerror=alert(1)></mtext></math>',
    '<iframe srcdoc="<script>alert(1)</script>"></iframe>',
    '<style>body{background:url(https://evil.test/pixel)}</style>',
    '<form action="/api/delete"><input name="x"></form>',
    '<span class="fixed inset-0" style="position:fixed" onclick="alert(1)">x</span>',
    '<!-- <img src=x onerror=alert(1)> -->',
    'prefix <script><img src=x onerror=alert(1)></script> suffix',
    '<svg><style><img src=x onerror=alert(1)></style></svg>',
    '<math><annotation-xml encoding="text/html"><script>alert(1)</script></annotation-xml></math>',
  ])('escapes original HTML as visible literal text: %s', (source) => {
    const raw = fragment(renderHtml(source))
    const root = render(source)
    expectInert(raw)
    expectInert(root)
    expect(root.querySelector('svg, math, .fixed')).toBeNull()
    expect(root.textContent).toContain(source)
  })

  it('keeps entity-encoded HTML text inert', () => {
    const root = render('&lt;img src=x onerror=alert(1)&gt; &lt;script&gt;x&lt;/script&gt;')
    expect(root.textContent).toContain('<img src=x onerror=alert(1)>')
    expectInert(root)
  })

  it.each([
    '![alt](https://evil.test/pixel)',
    '![<svg/onload=alert(1)>](//evil.test/pixel)',
    '![secret](/api/files/private)',
    '![embedded](data:image/svg+xml;base64,AAAA)',
    '![reference][img]\n\n[img]: https://evil.test/pixel',
    '[![nested](https://evil.test/pixel)](https://example.test)',
  ])('renders image alt placeholders, never automatic requests: %s', (source) => {
    const root = render(source)
    expect(root.querySelector('.markdown-image')).not.toBeNull()
    expect(root.textContent).toContain('[Image: ')
    expectInert(root)
  })
})

describe('link destination policy', () => {
  it.each([
    'https://example.test/path?q=1&other=2', 'HTTP://example.test',
    'https://例子.测试/path', 'mailto:user@example.test?subject=hello', '#section-1',
    'https&#58;//example.test/?a=1&amp;b=2',
  ])('permits explicitly allowed destinations: %s', (destination) => {
    const root = render(`[**label**](${destination} "title")`)
    const link = root.querySelector('a')!
    expect(link).not.toBeNull()
    expect(link.querySelector('strong')?.textContent).toBe('label')
    expect(link.getAttribute('title')).toBe('title')
    if (destination.startsWith('#')) {
      expect(link.getAttribute('target')).toBeNull()
    } else {
      expect(link.getAttribute('rel')).toBe('noopener noreferrer nofollow')
      expect(link.getAttribute('referrerpolicy')).toBe('no-referrer')
      expect(link.getAttribute('target')).toBe('_blank')
    }
  })

  const blocked = [
    'javascript:alert(1)', 'JAVASCRIPT:alert(1)', 'vbscript:msgbox(1)',
    'data:text/html;base64,PHNjcmlwdD4=', 'file:///etc/passwd', 'ftp://evil.test',
    '/api/delete', '/app', './relative', '../admin', 'relative', '?action=delete',
    '//evil.test/path', '\\\\evil.test', 'https:/evil.test', 'https:evil.test',
    'javascript&#58;alert(1)', 'java&#x73;cript:alert(1)',
    'javascript&colon;alert(1)', 'java&Tab;script:alert(1)',
    'java&#10;script:alert(1)', '&#x2f;&#x2f;evil.test',
    '&#47;api/delete', '&sol;&sol;evil.test',
    'https://example.test/&#9;path', 'https://example.test/&NewLine;path',
    'https://example.test/%0Apath', 'mailto:x@example.test?body=%0d%0aevil',
    'https://example.test/\u0001path', 'https://example.test/\u200bpath',
    'https://user:password@example.test', 'https://',
  ]
  it.each(blocked)('blocks disallowed, entity and control vectors: %s', (destination) => {
    const root = render(`[label](${destination})`)
    expect(root.querySelector('a[href]')).toBeNull()
    expect(root.textContent).toContain('label')
    expectInert(root)
  })

  it('protects GFM autolinks and reference links as well', () => {
    const root = render('https://example.test\n\n<mailto:x@example.test>\n\n[good][g] [bad][b]\n\n[g]: https://example.test\n[b]: /api/delete')
    expect(root.querySelectorAll('a[href]')).toHaveLength(3)
    expect([...root.querySelectorAll('a')].every((a) => a.rel === 'noopener noreferrer nofollow')).toBe(true)
  })

  it('rejects literal NUL URLs before micromark replacement can hide them', () => {
    for (const source of ['[label](https://example.test/a\0b)', '[label][ref]\n\n[ref]: https://example.test/a\0b']) {
      const root = render(source)
      expect(root.querySelector('a[href]')).toBeNull()
      expect(root.textContent).toContain('label')
    }
  })

  it('escapes injected link titles without creating attributes', () => {
    const root = render('[x](https://example.test "&quot; onmouseover=&quot;alert(1)")')
    expect(root.querySelector('a')?.hasAttribute('onmouseover')).toBe(false)
    expectInert(root)
  })
})

describe('KaTeX tree fidelity', () => {
  it('retains fcolorbox MathML borders and numerical SVG vector styles', () => {
    const root = render(String.raw`$$\fcolorbox{red}{blue}{$x$}+\vec{x}$$`)
    expect(root.querySelectorAll('.katex')).toHaveLength(1)
    expect(root.querySelector('math mpadded')?.getAttribute('style')).toContain('border:0.04em solid red')
    expect(root.querySelector('math mpadded')?.getAttribute('mathbackground')).toBe('blue')
    expect(root.querySelector('svg')?.getAttribute('style')).toContain('width:0.471em')
    expectInert(root)
  })

  it('preserves KaTeX SVG paths, styles, matrices and accessible MathML', () => {
    const root = render(String.raw`$$\sqrt{\frac{x}{y}}+\overrightarrow{AB}+\begin{pmatrix}a&b\\c&d\end{pmatrix}$$`)
    expect(root.querySelector('svg path')).not.toBeNull()
    expect(root.querySelector('svg')?.getAttribute('viewBox')).not.toBeNull()
    expect(root.querySelector('span[style]')).not.toBeNull()
    expect(root.querySelector('math msqrt')).not.toBeNull()
    expect(root.querySelector('math mtable')).not.toBeNull()
    expect(root.querySelector('annotation')?.getAttribute('encoding')).toBe('application/x-tex')
    expectInert(root)
  })

})
