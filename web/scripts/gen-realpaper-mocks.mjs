// Generate the reader mock fixtures from the REAL parse artifacts of arXiv
// 1605.01488 (plan §12.5: mock data comes from real mineru output, never
// hand-typed bboxes). Reads the raw docvortex.middle 2.0 JSON — from the
// canonical workspace (REALPAPER_DIR, default /home/timidly/qatlas-dev/
// realpaper) or, when that is absent, from the committed copies under
// tests/fixtures/realpaper/ — and emits the slim app fixtures consumed by
// src/mocks/reader-mock-seed.ts:
//
//   src/mocks/fixtures/realpaper-local.middle.json   (pr_local, current)
//   src/mocks/fixtures/realpaper-remote.middle.json  (pr_remote)
//
// Transform rules (kept identical for both parses):
//   - pages[].blocks[] is flattened in document order; public block number
//     = raw block.index + 1 (mineru indexes from 0, plan §5.1).
//   - content: raw equation blocks carry a LaTeX string; other blocks carry
//     a content[] tree. Both flatten to ONE display string (inline math
//     wrapped in $…$ so equations stay recognizable in the block list).
//   - bbox kept verbatim ([x0,y0,x1,y1], top-left origin, [0,1] page
//     fractions — the coordinates reader-geometry.ts already assumes).
//   - artifact_sha256 = sha256 of the RAW middle.json, so the mock parse
//     revisions point at the exact committed artifacts.
//
// Deterministic: same inputs → byte-identical output (stable key order,
// no timestamps). tests/reader-realpaper.test.ts guards committed fixtures
// against drift away from the committed raw artifacts.
import { createHash } from 'node:crypto'
import { existsSync, readFileSync, writeFileSync, mkdirSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'

// --- pure transform (unit-tested) ---------------------------------------------

export function flattenContent(node) {
  if (typeof node === 'string') return node
  if (Array.isArray(node)) return node.map(flattenEntry).join('')
  return ''
}

function flattenEntry(entry) {
  if (entry === null || typeof entry !== 'object') return ''
  const inner = flattenContent(entry.content)
  if (entry.type === 'equation_inline' && inner) return `$${inner}$`
  return inner
}

export function transformMiddle(raw, provenance) {
  if (!raw || raw.schema !== 'docvortex.middle') {
    throw new Error(`not a docvortex.middle document: ${raw?.schema}`)
  }
  const blocks = []
  for (const page of raw.pages ?? []) {
    for (const block of page.blocks ?? []) {
      blocks.push({
        page_idx: page.page_idx,
        index: block.index + 1, // public 1-based block number (§5.1)
        type: block.type,
        content: flattenContent(block.content),
        bbox: block.bbox ?? null,
      })
    }
  }
  return {
    _provenance: provenance,
    schema: raw.schema,
    schema_version: raw.schema_version,
    producer: raw.metadata?.producer ?? null,
    pages: raw.pages?.length ?? 0,
    blocks,
  }
}

// --- script driver (guarded so unit tests can import the pure transform;
// path resolution stays INSIDE the driver because vitest's jsdom
// environment gives import.meta.url a non-file URL) -----------------------------

function readRaw(variant) {
  const workspace = process.env.REALPAPER_DIR ?? '/home/timidly/qatlas-dev/realpaper'
  const root = fileURLToPath(new URL('..', import.meta.url))
  const candidates = [
    join(workspace, variant, 'middle.json'),
    join(root, 'tests', 'fixtures', 'realpaper', variant, 'middle.json'),
  ]
  const path = candidates.find((p) => existsSync(p))
  if (!path) {
    throw new Error(`raw middle.json for "${variant}" not found in ${candidates.join(' or ')}`)
  }
  return { path, raw: JSON.parse(readFileSync(path, 'utf8')) }
}

function emit(variant, outName) {
  const root = fileURLToPath(new URL('..', import.meta.url))
  const { path, raw } = readRaw(variant)
  const artifactSha = createHash('sha256').update(readFileSync(path)).digest('hex')
  const slim = transformMiddle(raw, {
    variant,
    raw_middle_json_sha256: artifactSha,
    source_pdf_sha256: 'd41ff6f92ffb6e610435cef1d7cdb88c80aa796cccd982faf5c1b31c88977eaf',
  })
  const out = join(root, 'src', 'mocks', 'fixtures', outName)
  mkdirSync(join(root, 'src', 'mocks', 'fixtures'), { recursive: true })
  writeFileSync(out, `${JSON.stringify(slim, null, 1)}\n`)
  console.log(
    `gen-realpaper-mocks: ${outName} — ${slim.pages} pages, ${slim.blocks.length} blocks ` +
      `(producer ${slim.producer?.name} ${slim.producer?.version}, artifact ${artifactSha.slice(0, 12)}…)`,
  )
}

function main() {
  emit('local', 'realpaper-local.middle.json')
  emit('remote', 'realpaper-remote.middle.json')
}

const invokedAsScript = process.argv[1]?.endsWith('gen-realpaper-mocks.mjs')
if (invokedAsScript) main()
