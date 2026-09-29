// Copy the pdfjs-dist companion assets (cmaps / standard_fonts / wasm / iccs)
// from the locked node_modules copy into public/pdfjs/ so both `vite dev` and
// the production build serve them same-origin. Per plan §12.1: the module and
// the worker come from the same locked package; no CDN fallback is allowed.
//
// The data assets are shared by the modern and legacy builds and live at the
// package root; the JS entry points are NOT copied here — PdfCanvas.tsx imports
// pdfjs-dist/legacy/build/* directly (see the toHex compatibility note there).
//
// This script therefore also GUARDS that legacy build: pdf.js ≥5.4.624
// standard unconditionally calls Uint8Array.prototype.toHex() (Chrome 140+),
// and v6 standard additionally needs Map.getOrInsertComputed / Math.sumPrecise
// / Promise.try. Only the legacy build (Babel + core-js) is safe on older
// browsers. If a pdfjs-dist upgrade ever drops the legacy build, or something
// re-points the app at the standard build, this fails the install/dev/build
// instead of silently shipping a white-screen PDF (the jujuleaf b896582 class
// of regression: a later "sync" overwriting the good legacy worker).
//
// Generated output is gitignored (see web/.gitignore) and refreshed on
// postinstall / predev / prebuild.
import { cpSync, existsSync, mkdirSync, readFileSync, rmSync, statSync } from 'node:fs'
import { fileURLToPath } from 'node:url'
import { join } from 'node:path'

const root = fileURLToPath(new URL('..', import.meta.url))
const pkg = join(root, 'node_modules', 'pdfjs-dist')
const target = join(root, 'public', 'pdfjs')

if (!existsSync(pkg)) {
  console.error('sync-pdfjs-assets: node_modules/pdfjs-dist missing; run npm ci first')
  process.exit(1)
}

// --- legacy-build guard ------------------------------------------------------
// core-js is bundled into (only) the legacy build; "core-js_shared__" is its
// stable global-key literal and does not appear in the standard build.
const LEGACY_MARKER = 'core-js_shared__'
for (const rel of ['legacy/build/pdf.mjs', 'legacy/build/pdf.worker.min.mjs']) {
  const file = join(pkg, rel)
  if (!existsSync(file)) {
    console.error(`sync-pdfjs-assets: ${rel} missing — pdfjs-dist no longer ships a legacy build; DO NOT fall back to the standard build (toHex blank-PDF regression)`)
    process.exit(1)
  }
  if (!readFileSync(file, 'utf8').includes(LEGACY_MARKER)) {
    console.error(`sync-pdfjs-assets: ${rel} lacks the core-js marker — it does not look like the transpiled legacy build`)
    process.exit(1)
  }
}
// The legacy worker must stay clearly larger than the standard one (core-js
// payload). A shrinking delta means the wrong directory is being picked.
const workerLegacy = statSync(join(pkg, 'legacy/build/pdf.worker.min.mjs')).size
const workerStandard = statSync(join(pkg, 'build/pdf.worker.min.mjs')).size
if (workerLegacy <= workerStandard) {
  console.error(`sync-pdfjs-assets: legacy worker (${workerLegacy} B) is not larger than standard (${workerStandard} B) — wrong build layout?`)
  process.exit(1)
}

// --- companion data assets (shared modern/legacy, package root) --------------
rmSync(target, { recursive: true, force: true })
mkdirSync(target, { recursive: true })
for (const dir of ['cmaps', 'standard_fonts', 'wasm', 'iccs']) {
  const src = join(pkg, dir)
  if (existsSync(src)) cpSync(src, join(target, dir), { recursive: true })
}
console.log(
  `sync-pdfjs-assets: legacy build verified (worker ${workerLegacy} B > standard ${workerStandard} B); copied cmaps/standard_fonts/wasm/iccs to public/pdfjs/`,
)
