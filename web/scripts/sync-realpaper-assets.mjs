// Sync the real-paper parse artifacts (arXiv 1605.01488) from the local
// analysis workspace into this repo. Plan §12.5 mock data must come from a
// REAL parse of a REAL paper, not hand-typed bboxes — the committed copies
// (pdf + both middle.json + markdown) keep the repo reproducible; this
// script refreshes them from the canonical workspace.
//
// Layout produced (committed):
//   public/fixtures/realpaper/1605.01488.pdf        — source PDF served to the reader
//   tests/fixtures/realpaper/local/middle.json      — mineru 4.0.9 local parse (pinned, plan §11)
//   tests/fixtures/realpaper/local/markdown.md      — same parse, markdown export
//   tests/fixtures/realpaper/remote/middle.json     — remote engine 3.4.4 parse (2nd revision)
//
// NOT committed (large): remote/images/* — block figures. Pass --images to
// copy them into public/fixtures/realpaper/images/ (gitignored) for local
// eyeballing. Without them the reader stays in bbox-overlay/no-image mode,
// which is its normal behavior for image blocks (content is empty in the
// middle JSON; only frames are drawn) — nothing degrades functionally.
//
// Missing source workspace (e.g. CI, fresh clone): the script prints a hint
// and exits 0 — the committed copies already carry everything the build and
// tests need.
import { cpSync, existsSync, mkdirSync, readFileSync, statSync } from 'node:fs'
import { createHash } from 'node:crypto'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'

const root = fileURLToPath(new URL('..', import.meta.url))
const source = process.env.REALPAPER_DIR ?? '/home/timidly/qatlas-dev/realpaper'

const PDF_SHA256 = 'd41ff6f92ffb6e610435cef1d7cdb88c80aa796cccd982faf5c1b31c88977eaf'
const PDF_NAME = '1605.01488.pdf'

function sha256(file) {
  return createHash('sha256').update(readFileSync(file)).digest('hex')
}

function copy(from, to, { verify } = {}) {
  mkdirSync(join(root, to, '..'), { recursive: true })
  cpSync(from, join(root, to))
  if (verify) {
    const got = sha256(join(root, to))
    if (got !== verify) {
      throw new Error(`${to}: sha256 ${got} != pinned ${verify} — refusing to update fixtures`)
    }
  }
  console.log(`sync-realpaper-assets: ${to} (${statSync(join(root, to)).size} B)`)
}

if (!existsSync(source)) {
  console.log(
    `sync-realpaper-assets: source workspace ${source} not found — skipping. ` +
      'The committed copies under public/fixtures/realpaper and tests/fixtures/realpaper stay as-is; ' +
      'set REALPAPER_DIR to refresh them.',
  )
  process.exit(0)
}

copy(join(source, PDF_NAME), join('public', 'fixtures', 'realpaper', PDF_NAME), { verify: PDF_SHA256 })
copy(join(source, 'local', 'middle.json'), join('tests', 'fixtures', 'realpaper', 'local', 'middle.json'))
copy(join(source, 'local', 'markdown.md'), join('tests', 'fixtures', 'realpaper', 'local', 'markdown.md'))
copy(join(source, 'remote', 'middle.json'), join('tests', 'fixtures', 'realpaper', 'remote', 'middle.json'))

const imagesFrom = join(source, 'remote', 'images')
if (process.argv.includes('--images')) {
  if (existsSync(imagesFrom)) {
    copy(imagesFrom, join('public', 'fixtures', 'realpaper', 'images'))
  } else {
    console.log('sync-realpaper-assets: --images requested but remote/images/ is absent; staying in no-image mode')
  }
} else if (existsSync(imagesFrom)) {
  console.log(
    'sync-realpaper-assets: remote/images/ available — rerun with --images to copy block figures ' +
      '(gitignored; the reader works without them in bbox-overlay mode)',
  )
}
