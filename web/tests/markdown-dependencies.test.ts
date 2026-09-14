import { existsSync, readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'
import ts from 'typescript'

const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

const productionFiles = [
  '../src/components/markdown-document.tsx',
  '../src/components/markdown-rendered.tsx',
  '../src/components/markdown-preview.tsx',
  '../src/lib/markdown-renderer.ts',
  '../src/lib/markdown-policy.ts',
  '../src/lib/markdown-limits.ts',
  '../src/lib/remark-math-compat.ts',
]

describe('Markdown dependency and production boundary contract', () => {
  it('pins and directly renders the official react-markdown component', () => {
    const manifest = JSON.parse(read('../package.json'))
    const lock = JSON.parse(read('../package-lock.json'))
    const installed = JSON.parse(read('../node_modules/react-markdown/package.json'))
    expect(manifest.dependencies['react-markdown']).toBe('10.1.0')
    expect(installed.version).toBe('10.1.0')
    const versions = Object.entries(lock.packages)
      .filter(([path]) => /(?:^|\/)node_modules\/react-markdown$/.test(path))
      .map(([, pkg]) => (pkg as { version: string }).version)
    expect(versions).toEqual(['10.1.0'])
    const document = read('../src/components/markdown-document.tsx')
    expect(document).toMatch(/import Markdown\b[^\n]*from ['"]react-markdown['"]/)
    expect(document).toMatch(/<Markdown\b/)
  })

  it('locks exactly one KaTeX runtime matching the locally bundled CSS', () => {
    const manifest = JSON.parse(read('../package.json'))
    const lock = JSON.parse(read('../package-lock.json'))
    const versions = Object.entries(lock.packages).filter(([path]) => /(?:^|\/)node_modules\/katex$/.test(path))
      .map(([, pkg]) => (pkg as { version: string }).version)
    expect(versions).toEqual([manifest.dependencies.katex])
    expect(read('../node_modules/katex/dist/katex.css')).toContain(`content: "${manifest.dependencies.katex}"`)
    expect(read('../src/components/markdown-rendered.tsx')).toContain("import 'katex/dist/katex.min.css'")
  })

  it('does not retain legacy engines, mismatched math plugins or raw/MDX parsers in the graph', () => {
    const lock = JSON.parse(read('../package-lock.json'))
    expect(Object.keys(lock.packages).filter((path) => /(?:^|\/)node_modules\/(?:marked|marked-katex-extension|dompurify|remark-math|micromark-extension-math|rehype-katex|rehype-raw|@mdx-js\/mdx)$/.test(path))).toEqual([])
  })

  it('has no production HTML sink or parallel hand-built Markdown/JSX pipeline', () => {
    for (const path of productionFiles) {
      const code = read(path)
      expect(code).not.toMatch(/dangerouslySetInnerHTML|\.innerHTML\s*=/)
      expect(code).not.toMatch(/from ['"](?:hast-util-to-jsx-runtime|react\/jsx-runtime|marked|dompurify|rehype-raw|@mdx-js\/mdx)['"]/)
      const source = ts.createSourceFile(path, code, ts.ScriptTarget.Latest, true, ts.ScriptKind.TSX)
      const independentPipelines: string[] = []
      function visit(node: ts.Node) {
        if ((ts.isCallExpression(node) || ts.isNewExpression(node)) && ts.isIdentifier(node.expression)
          && ['unified', 'toJsxRuntime', 'Worker'].includes(node.expression.text)) {
          independentPipelines.push(node.expression.text)
        }
        ts.forEachChild(node, visit)
      }
      visit(source)
      expect(independentPipelines, path).toEqual([])
    }
    const renderer = read('../src/lib/markdown-renderer.ts')
    expect(renderer).toContain("from 'hast-util-from-html'")
    expect(renderer).toContain('katex.renderToString(')
    expect(renderer).not.toMatch(/from ['"](?:jsdom|hast-util-from-html-isomorphic|rehype-raw)['"]/)
  })

  it('removes the Worker protocol and decoder instead of leaving an unused rendering path', () => {
    for (const path of [
      '../src/lib/markdown.worker.ts', '../src/lib/markdown-protocol.ts',
      '../src/lib/markdown-react.ts', '../src/lib/markdown-tree.ts',
    ]) {
      expect(existsSync(new URL(path, import.meta.url)), path).toBe(false)
    }
  })
})
