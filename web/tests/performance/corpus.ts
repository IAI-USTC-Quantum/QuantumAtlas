import { createHash } from 'node:crypto'

// Do not import production parser/limits here: keep the exact corpus invariant
// across implementations. All lengths below count JavaScript UTF-16 code units.
export const CORPUS_VERSION = 'paper-responsiveness-v1'
export const TERMINAL_SENTINEL = 'BENCHMARK_DOCUMENT_COMPLETE'
const prose = 'We study a synthetic quantum material with reproducible measurements, bounded uncertainty, and a transparent method. The observations distinguish correlation from causation and compare the control with the experimental sample. 量子测量与可复现性 🧪. '
const code = '\n```typescript\nconst energy = mass * speed ** 2;\nconst literal = "$not_math$";\n```\n'
const mixedCore = String.raw`## Synthetic methods

A **controlled result** with *uncertainty* and ~~superseded estimate~~.
Inline energy $E=mc^2$; parenthesized geometry \(a^2+b^2=c^2\).

$$
\int_0^1 x^2\,dx=\frac{1}{3}
$$

| Sample | Estimate |
| :--- | ---: |
| Control | 0.125 |
| Quantum | 0.875 |

> A synthetic observation, not a published research finding.

- [x] Prepare the control
- [ ] Repeat the measurement

` + code
const denseCore = String.raw`## Dense synthetic math

The estimate is $\frac{a^2+b^2}{1+\sqrt{x}}$ and $\sqrt{x^2+y^2}$.

$$
\sum_{n=1}^{N} n=\frac{N(N+1)}{2}
$$

` + prose

export type CorpusCase = ReturnType<typeof makeCase>
function makeCase(id: string, target: number, kind: 'mixed' | 'text' | 'dense') {
  const header = '# Synthetic paper responsiveness benchmark\n\n'
  const footer = `\n\n${TERMINAL_SENTINEL}\n`
  const block = kind === 'mixed' ? mixedCore + ('\n' + prose.repeat(14) + '\n\n')
    : kind === 'dense' ? denseCore : prose.repeat(5) + '\n\n'
  const repetitions = Math.floor((target - header.length - footer.length) / block.length)
  const body = header + block.repeat(repetitions)
  // ASCII padding avoids cutting a UTF-16 surrogate, math delimiter, or Markdown
  // construct. A terminal marker detects truncation even on successful renders.
  const padLength = target - body.length - footer.length
  const padUnit = 'Supplementary synthetic prose without math. '
  const padding = padUnit.repeat(Math.ceil(padLength / padUnit.length)).slice(0, padLength)
  const markdown = body + padding + footer
  if (markdown.length !== target) throw new Error(`Corpus length mismatch: ${id}`)
  return {
    id, kind, markdown,
    dimensions: {
      utf16CodeUnits: markdown.length,
      utf8Bytes: Buffer.byteLength(markdown),
      sha256: createHash('sha256').update(markdown).digest('hex'),
      repeatedBlocks: repetitions,
      expectedFormulas: kind === 'text' ? 0 : repetitions * 3,
      expectedTables: kind === 'mixed' ? repetitions : 0,
      expectedCodeBlocks: kind === 'mixed' ? repetitions : 0,
      expectedHeadings: 1 + (kind === 'text' ? 0 : repetitions),
    },
  }
}

export const corpus = [
  makeCase('mixed-10k', 10_000, 'mixed'),
  makeCase('mixed-50k', 50_000, 'mixed'),
  makeCase('mixed-100k', 100_000, 'mixed'),
  makeCase('mixed-200k', 200_000, 'mixed'),
  makeCase('text-200k', 200_000, 'text'),
  makeCase('dense-math-50k', 50_000, 'dense'),
]
export const slowCaseIds = new Set(['mixed-50k', 'mixed-200k', 'dense-math-50k'])
