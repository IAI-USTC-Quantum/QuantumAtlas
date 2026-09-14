// @vitest-environment jsdom
import { describe, expect, it } from 'vitest'
import { renderToStaticMarkup } from 'react-dom/server'
import { markdownReplyToReact } from '../src/lib/markdown-react'
import { decodeMarkdownReply, validateMarkdownTree } from '../src/lib/markdown-tree'
import { MAX_RENDERED_DEPTH, MAX_RENDERED_NODES, MAX_RENDERED_TREE_LENGTH } from '../src/lib/markdown-protocol'

const text = { type: 'text', value: 'safe' }
const element = (tagName: string, properties = {}, children: unknown[] = [text]) => ({ type: 'element', tagName, properties, children })
const tree = (child: unknown) => ({ type: 'root', children: [child] })
const reply = (child: unknown) => ({ ok: true, treeJson: JSON.stringify(tree(child)) })
function dom(child: unknown) {
  const template = document.createElement('template')
  template.innerHTML = renderToStaticMarkup(markdownReplyToReact(reply(child)))
  return template.content
}

describe('independent Worker tree trust boundary', () => {
  it.each(['raw', 'comment', 'doctype', 'mdxFlowExpression', 'mdxTextExpression', 'mdxJsxFlowElement', 'mdxjsEsm'])('rejects node type %s without evaluating or reinterpreting it', (type) => {
    expect(() => markdownReplyToReact(reply({ type, value: '<img src=x onerror=alert(1)>', children: [] }))).toThrow()
  })

  it.each(['script', 'style', 'iframe', 'frame', 'object', 'embed', 'form', 'input', 'button', 'textarea', 'select', 'img', 'image', 'picture', 'source', 'video', 'audio', 'track', 'link', 'meta', 'base', 'foreignObject', 'annotation-xml', 'use', 'animate', 'animateMotion', 'animateTransform', 'set', 'x-custom'])('rejects active/media/unknown element %s', (tag) => {
    expect(() => markdownReplyToReact(reply(element(tag)))).toThrow()
  })

  it.each(['onClick', 'onclick', 'onError', 'dangerouslySetInnerHTML', 'innerHTML', 'is', 'ref', 'key', 'children', 'src', 'srcSet', 'srcDoc', 'xLinkHref', 'ping', 'action', 'formAction', 'id', 'name', '__proto__', 'constructor', 'data-evil', 'ariaLabel'])('rejects unexpected prop %s instead of spreading it', (key) => {
    const properties = JSON.parse(`{${JSON.stringify(key)}:"malicious"}`)
    expect(() => markdownReplyToReact(reply(element('span', properties)))).toThrow()
  })

  it.each([null, [], { type: 'element' }, { type: 'root', children: 'x' }, { type: 'root', children: [null] },
    tree({ type: 'text', value: {} }), tree(element('span', { style: { backgroundImage: 'url(https://evil.test)' } }))])('rejects malformed shapes: %j', (value) => {
    expect(() => validateMarkdownTree(value)).toThrow()
  })

  it.each(['hr', 'br'])('rejects malformed void %s children before React allocation', (tag) => {
    expect(() => markdownReplyToReact(reply(element(tag)))).toThrow(/Void/)
    expect(() => markdownReplyToReact(reply(element(tag, {}, [])))).not.toThrow()
  })

  it('drops all metadata and unselected top-level fields', () => {
    const input = tree({ ...element('span'), data: { hName: 'script', hProperties: { onClick: 'evil' } }, position: {}, dangerouslySetInnerHTML: 'evil' })
    expect(validateMarkdownTree(input)).toEqual(tree(element('span')))
  })

  it('cannot switch out of SVG/MathML through an HTML integration point', () => {
    for (const node of [element('math', {}, [element('mtext', {}, [element('a', { href: 'https://evil.test' })])]),
      element('svg', {}, [element('a', { href: 'https://evil.test' })]), element('math', {}, [element('mi', { href: 'https://evil.test' })])]) {
      expect(() => markdownReplyToReact(reply(node))).toThrow()
    }
  })

  it.each(['javascript:alert(1)', 'javascript&#58;alert(1)', 'java\tscript:alert(1)', '/api/delete', 'relative', '//evil.test',
    'https://example.test/%0d', '\thttps://example.test', 'https://example.test\n', 'https://user:pass@example.test', 'https://example.test/\u200b'])('independently blocks href %s', (href) => {
    const root = dom(element('a', { href, target: '_self', rel: ['opener'] }))
    expect(root.querySelector('a[href]')).toBeNull()
    expect(root.textContent).toBe('safe')
  })

  it('rebuilds external metadata and keeps fragments in-document', () => {
    const link = dom(element('a', { href: 'https://example.test', target: 'evil', rel: ['opener'], referrerPolicy: 'unsafe-url' })).querySelector('a')!
    expect(link.target).toBe('_blank')
    expect(link.rel).toBe('noopener noreferrer nofollow')
    expect(link.getAttribute('referrerpolicy')).toBe('no-referrer')
    const fragment = dom(element('a', { href: '#x', target: 'evil', rel: 'opener', referrerPolicy: 'unsafe-url' })).querySelector('a')!
    expect(fragment.getAttribute('target')).toBeNull()
    expect(fragment.getAttribute('rel')).toBeNull()
    expect(fragment.getAttribute('referrerpolicy')).toBeNull()
  })

  it('keeps only known layout classes, never application utility classes', () => {
    const root = dom(element('span', { className: ['fixed', 'inset-0', 'markdown-image', 'mathnormal'] }))
    expect(root.querySelector('.fixed, .inset-0')).toBeNull()
    expect(root.querySelector('span')?.className).toBe('markdown-image mathnormal')
  })

  it('removes resource CSS/paint without dropping numerical KaTeX layout', () => {
    const root = dom(element('span', { style: 'height:1em;vertical-align:-0.2em;background-image:url(https://evil.test);color:expression(alert(1));--evil:url(x);position:fixed;behavior:url(x);border-color:red' }, [element('svg', {}, [element('path', { d: 'M0 0 L1 1', fill: 'url(https://evil.test)' }, [])])]))
    const style = root.querySelector('span')!.getAttribute('style')!
    expect(style).toContain('height:1em')
    expect(style).toContain('vertical-align:-0.2em')
    expect(style).toContain('border-color:red')
    expect(style).not.toMatch(/fixed|url|expression|--evil/)
    expect(root.querySelector('path')?.hasAttribute('fill')).toBe(false)
  })

  it.each(['color:v\\61r(--x)', 'width:calc(100vw)', 'height:1em!important', 'color:var(--x)', 'width:expression(x)', 'background-color:url(https://evil.test)'])('rejects functional/escaped CSS: %s', (style) => {
    expect(dom(element('span', { style })).querySelector('[style]')).toBeNull()
  })

  it('is stable under repeated validation without mutating the input', () => {
    const value = tree(element('a', { href: 'https://example.test', target: '_self' }))
    const before = JSON.stringify(value)
    const clean = validateMarkdownTree(value)
    expect(validateMarkdownTree(clean)).toEqual(clean)
    expect(JSON.stringify(value)).toBe(before)
  })
})

describe('bounded wire decoder, before JSX allocation', () => {
  it.each([undefined, null, [], { ok: false }, { ok: 'true', treeJson: '{}' }, { ok: true, treeJson: {} },
    { ok: true, treeJson: '{broken' }, { ok: true, html: '<script>x</script>' }])('rejects malformed/legacy reply %j', (value) => {
    expect(() => decodeMarkdownReply(value)).toThrow()
  })

  it('checks serialized size before JSON parsing', () => {
    expect(() => decodeMarkdownReply({ ok: true, treeJson: ' '.repeat(MAX_RENDERED_TREE_LENGTH + 1) })).toThrow(RangeError)
  })

  it('bounds total nodes including text and root at the exact threshold', () => {
    const value = { type: 'root', children: Array.from({ length: MAX_RENDERED_NODES - 1 }, () => text) }
    expect(validateMarkdownTree(value).children).toHaveLength(MAX_RENDERED_NODES - 1)
    value.children.push(text)
    expect(() => validateMarkdownTree(value)).toThrow(RangeError)
  })

  it('bounds depth, also terminating cyclic inputs', () => {
    let node: unknown = text
    for (let i = 1; i < MAX_RENDERED_DEPTH; i++) node = element('span', {}, [node])
    expect(() => validateMarkdownTree(tree(node))).not.toThrow()
    expect(() => validateMarkdownTree(tree(element('span', {}, [node])))).toThrow(RangeError)
    const cycle = element('span')
    cycle.children = [cycle]
    expect(() => validateMarkdownTree(tree(cycle))).toThrow(RangeError)
  })
})
