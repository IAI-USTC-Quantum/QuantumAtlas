// Regression guard for the pdf.js legacy-build policy (mess archive:
// pdfjs-tohex). pdf.js ≥5.4.624 standard build unconditionally calls
// Uint8Array.prototype.toHex() (Chrome 140+ only) and v6 standard also needs
// Map.getOrInsertComputed / Math.sumPrecise / Promise.try; on older browsers
// the PDF area goes blank with "n.toHex is not a function". The ONLY official
// fix is running BOTH the main thread and the worker from the legacy build
// (Babel + core-js). This test fails when either side drifts back to the
// standard build — the class of regression jujuleaf fixed in b896582, where a
// later asset-sync step overwrote the good legacy worker with the standard one.
//
// A local/new headless Chrome cannot reproduce the toHex crash (native since
// Chrome 140), which is exactly why these structural assertions exist.
import { readFileSync, existsSync } from 'node:fs'
import { describe, expect, it } from 'vitest'

const read = (path: string) => readFileSync(new URL(path, import.meta.url), 'utf8')
const exists = (path: string) => existsSync(new URL(path, import.meta.url))
const size = (path: string) => read(path).length

// The marker literal exists only in the transpiled legacy build (core-js
// global key); the standard build never contains it.
const LEGACY_MARKER = 'core-js_shared__'

describe('pdf.js runs on the legacy build (toHex-safe)', () => {
  it('loads the main thread module from pdfjs-dist/legacy/build', () => {
    const pdfCanvas = read('../src/components/reader/PdfCanvas.tsx')
    expect(pdfCanvas, 'PdfCanvas must import the legacy main module')
      .toContain("import('pdfjs-dist/legacy/build/pdf.mjs')")
    expect(pdfCanvas).not.toContain("import('pdfjs-dist/build/pdf.mjs')")
    expect(pdfCanvas).not.toMatch(/import\('pdfjs-dist'\)/)
  })

  it('loads the worker from pdfjs-dist/legacy/build via the ?url import', () => {
    const pdfCanvas = read('../src/components/reader/PdfCanvas.tsx')
    expect(pdfCanvas, 'PdfCanvas must load the legacy worker')
      .toContain('pdfjs-dist/legacy/build/pdf.worker.min.mjs?url')
    expect(pdfCanvas).not.toContain('pdfjs-dist/build/pdf.worker.min.mjs?url')
  })

  it('node_modules ships a distinct legacy build (core-js present, larger)', () => {
    for (const rel of [
      '../node_modules/pdfjs-dist/legacy/build/pdf.mjs',
      '../node_modules/pdfjs-dist/legacy/build/pdf.worker.min.mjs',
    ]) {
      expect(exists(rel), `${rel} must exist`).toBe(true)
      expect(read(rel), `${rel} must embed core-js`).toContain(LEGACY_MARKER)
    }
    // The standard build must NOT carry the marker — otherwise the marker
    // above could not discriminate the two builds and the guard is void.
    for (const rel of [
      '../node_modules/pdfjs-dist/build/pdf.mjs',
      '../node_modules/pdfjs-dist/build/pdf.worker.min.mjs',
    ]) {
      expect(read(rel), `${rel} must be the marker-free standard build`)
        .not.toContain(LEGACY_MARKER)
    }
    const legacy = size('../node_modules/pdfjs-dist/legacy/build/pdf.worker.min.mjs')
    const standard = size('../node_modules/pdfjs-dist/build/pdf.worker.min.mjs')
    expect(legacy, 'legacy worker carries the core-js payload and must be larger').toBeGreaterThan(standard)
  })

  it('the asset sync script guards the legacy build before copying', () => {
    const script = read('../scripts/sync-pdfjs-assets.mjs')
    expect(script).toContain(LEGACY_MARKER)
    expect(script).toMatch(/legacy\/build\/pdf\.worker\.min\.mjs/)
  })
})
