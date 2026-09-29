// Copy the pdfjs-dist companion assets (cmaps / standard_fonts / wasm / iccs)
// from the locked node_modules copy into public/pdfjs/ so both `vite dev` and
// the production build serve them same-origin. Per plan §12.1: the module and
// the worker come from the same locked package; no CDN fallback is allowed.
//
// Generated output is gitignored (see web/.gitignore) and refreshed on
// postinstall / predev / prebuild.
import { cpSync, existsSync, mkdirSync, rmSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'

const root = fileURLToPath(new URL('..', import.meta.url))
const pkg = join(root, 'node_modules', 'pdfjs-dist')
const target = join(root, 'public', 'pdfjs')

if (!existsSync(pkg)) {
  console.error('sync-pdfjs-assets: node_modules/pdfjs-dist missing; run npm ci first')
  process.exit(1)
}

rmSync(target, { recursive: true, force: true })
mkdirSync(target, { recursive: true })
for (const dir of ['cmaps', 'standard_fonts', 'wasm', 'iccs']) {
  const src = join(pkg, dir)
  if (existsSync(src)) cpSync(src, join(target, dir), { recursive: true })
}
console.log('sync-pdfjs-assets: copied cmaps/standard_fonts/wasm/iccs to public/pdfjs/')
