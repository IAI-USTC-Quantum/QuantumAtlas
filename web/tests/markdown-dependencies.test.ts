import { readFileSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')

describe('Markdown dependency and production boundary contract', () => {
  it('locks exactly one KaTeX runtime matching the locally bundled CSS', () => {
    const manifest = JSON.parse(read('../package.json'))
    const lock = JSON.parse(read('../package-lock.json'))
    const versions = Object.entries(lock.packages).filter(([path]) => /(?:^|\/)node_modules\/katex$/.test(path))
      .map(([, pkg]) => (pkg as { version: string }).version)
    expect(versions).toEqual([manifest.dependencies.katex])
    expect(read('../node_modules/katex/dist/katex.css')).toContain(`content: "${manifest.dependencies.katex}"`)
    expect(read('../src/components/markdown-rendered.tsx')).toContain("import 'katex/dist/katex.min.css'")
  })

  it('does not retain marked, DOMPurify, mismatched math plugins or raw/MDX parsers in the graph', () => {
    const lock = JSON.parse(read('../package-lock.json'))
    expect(Object.keys(lock.packages).filter((path) => /(?:^|\/)node_modules\/(?:marked|marked-katex-extension|dompurify|remark-math|micromark-extension-math|rehype-katex|rehype-raw|@mdx-js\/mdx)$/.test(path))).toEqual([])
  })

  it('has no production HTML sink or main-thread Markdown/KaTeX parser fallback', () => {
    for (const path of ['../src/components/markdown-rendered.tsx', '../src/components/markdown-preview.tsx', '../src/lib/markdown-react.ts']) {
      const code = read(path)
      expect(code).not.toMatch(/dangerouslySetInnerHTML|\.innerHTML\s*=/)
      expect(code).not.toMatch(/from ['"](?:.*markdown-renderer|katex|remark-parse|marked)['"]/)
    }
    const renderer = read('../src/lib/markdown-renderer.ts')
    expect(renderer).toContain("from 'hast-util-from-html'")
    expect(renderer).not.toMatch(/from ['"](?:jsdom|hast-util-from-html-isomorphic|rehype-raw)['"]/)
  })
})
