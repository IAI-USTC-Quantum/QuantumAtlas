import { useEffect, useRef, useState, type ReactNode } from 'react'
import type { PDFDocumentLoadingTask, PDFDocumentProxy, RenderTask } from 'pdfjs-dist'
import { useTranslation } from 'react-i18next'
import { ChevronLeft, ChevronRight, Loader2, RotateCcw, ZoomIn, ZoomOut } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { isCanvasRotation, type CanvasRotation } from '@/lib/reader-geometry'

// pdfjs module AND worker come from the same locked pdfjs-dist package;
// no vendor copy and no CDN fallback (plan §12.1). The worker URL import is
// resolved by Vite into a same-origin asset, satisfying worker-src 'self'.
let pdfjsPromise: Promise<typeof import('pdfjs-dist')> | null = null
async function loadPdfjs() {
  if (!pdfjsPromise) {
    pdfjsPromise = (async () => {
      const lib = await import('pdfjs-dist')
      const { default: workerUrl } = await import('pdfjs-dist/build/pdf.worker.min.mjs?url')
      lib.GlobalWorkerOptions.workerSrc = workerUrl
      return lib
    })()
  }
  return pdfjsPromise
}

const PDFJS_ASSETS = `${import.meta.env.BASE_URL}pdfjs/`

// Render budgets mirroring the reference viewer pattern (plan §12.1): cap
// page count and total canvas pixels; clamp devicePixelRatio so a 4K screen
// cannot silently multiply the pixel budget.
const MAX_PAGES = 1000
const MAX_PIXELS = 8_000_000
const MIN_SCALE = 0.4
const MAX_SCALE = 4
const SCALE_STEP = 1.2
const MAX_DPR = 2

export type PdfGeometry = {
  cssWidth: number
  cssHeight: number
  rotation: CanvasRotation
}

export function PdfCanvas({
  bytes,
  loading,
  error,
  page,
  onPageChange,
  rotation,
  onRotate,
  overlay,
  onGeometry,
}: {
  bytes: ArrayBuffer | null
  loading: boolean
  error: string
  page: number
  onPageChange: (page: number) => void
  rotation: CanvasRotation
  onRotate: (rotation: CanvasRotation) => void
  overlay?: ReactNode
  onGeometry?: (geometry: PdfGeometry) => void
}) {
  const { t } = useTranslation('reader')
  const canvasRef = useRef<HTMLCanvasElement>(null)
  const [doc, setDoc] = useState<PDFDocumentProxy | null>(null)
  const [numPages, setNumPages] = useState(0)
  const [scale, setScale] = useState(1)
  const [renderError, setRenderError] = useState('')

  // Load (or tear down) the document whenever the source bytes change.
  // Transfering the buffer to pdfjs detaches it, so we clone defensively:
  // a cached query must stay re-usable across StrictMode double effects.
  useEffect(() => {
    let dead = false
    let loadingTask: PDFDocumentLoadingTask | null = null
    setDoc(null)
    setNumPages(0)
    setRenderError('')
    if (!bytes) return
    void (async () => {
      const lib = await loadPdfjs()
      if (dead) return
      loadingTask = lib.getDocument({
        data: bytes.slice(0),
        useSystemFonts: false,
        stopAtErrors: true,
        maxImageSize: MAX_PIXELS,
        canvasMaxAreaInBytes: MAX_PIXELS * 4,
        cMapUrl: `${PDFJS_ASSETS}cmaps/`,
        cMapPacked: true,
        standardFontDataUrl: `${PDFJS_ASSETS}standard_fonts/`,
        wasmUrl: `${PDFJS_ASSETS}wasm/`,
        iccUrl: `${PDFJS_ASSETS}iccs/`,
      })
      const loaded = await loadingTask.promise
      if (dead) {
        void loadingTask.destroy()
        return
      }
      if (loaded.numPages > MAX_PAGES) {
        void loadingTask.destroy()
        setRenderError(t('pdf.tooManyPages', { count: loaded.numPages, max: MAX_PAGES }))
        return
      }
      setDoc(loaded)
      setNumPages(loaded.numPages)
    })().catch((e: unknown) => {
      if (!dead) setRenderError(e instanceof Error ? e.message : String(e))
    })
    return () => {
      dead = true
      void loadingTask?.destroy()
    }
  }, [bytes, t])

  // Reset the page when the document identity changes so a stale page
  // number from the previous paper cannot leak into the new one.
  useEffect(() => {
    if (doc && page > doc.numPages) onPageChange(1)
  }, [doc, page, onPageChange])

  // Render the current page onto the canvas. Every re-run cancels the
  // in-flight RenderTask first; the pixel budget shrinks the effective
  // scale when the wanted viewport would explode past MAX_PIXELS.
  useEffect(() => {
    if (!doc || !canvasRef.current) return
    let cancelled = false
    let task: RenderTask | undefined
    const canvas = canvasRef.current
    delete canvas.dataset.rendered
    void (async () => {
      try {
        const pdfPage = await doc.getPage(page)
        if (cancelled) return
        const dpr = Math.min(window.devicePixelRatio || 1, MAX_DPR)
        const wanted = pdfPage.getViewport({ scale: scale * dpr, rotation })
        const factor = Math.min(1, Math.sqrt(MAX_PIXELS / (wanted.width * wanted.height)))
        const viewport = pdfPage.getViewport({ scale: scale * dpr * factor, rotation })
        canvas.width = Math.floor(viewport.width)
        canvas.height = Math.floor(viewport.height)
        canvas.style.width = `${Math.floor(viewport.width / dpr)}px`
        canvas.style.height = `${Math.floor(viewport.height / dpr)}px`
        const context = canvas.getContext('2d')
        if (!context) throw new Error(t('pdf.noCanvas2d'))
        task = pdfPage.render({ canvas, canvasContext: context, viewport })
        await task.promise
        if (cancelled) return
        canvas.dataset.rendered = String(page)
        onGeometry?.({
          cssWidth: canvas.clientWidth,
          cssHeight: canvas.clientHeight,
          rotation: isCanvasRotation(rotation) ? rotation : 0,
        })
      } catch (e) {
        if (!cancelled) setRenderError(e instanceof Error ? e.message : String(e))
      }
    })()
    return () => {
      cancelled = true
      task?.cancel()
    }
    // onGeometry is deliberately excluded: it is a parent callback that
    // must not trigger re-renders, only receive geometry reports.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [doc, page, scale, rotation, t])

  const zoom = (direction: 1 | -1) =>
    setScale((current) =>
      direction === 1
        ? Math.min(MAX_SCALE, current * SCALE_STEP)
        : Math.max(MIN_SCALE, current / SCALE_STEP),
    )

  const rotate = () => onRotate(((rotation + 90) % 360) as CanvasRotation)

  const busy = loading || (!doc && !error && !renderError)

  return (
    <div className="flex min-h-0 flex-1 flex-col gap-2" data-testid="pdf-canvas">
      <div className="flex flex-wrap items-center gap-1.5" data-testid="pdf-toolbar">
        <Button
          variant="outline"
          size="icon-sm"
          aria-label={t('pdf.prevPage')}
          disabled={!doc || page <= 1}
          onClick={() => onPageChange(page - 1)}
        >
          <ChevronLeft className="size-4" />
        </Button>
        <span className="flex items-center gap-1 text-sm tabular-nums text-muted-foreground">
          <label className="sr-only" htmlFor="pdf-page-input">
            {t('pdf.pageInput')}
          </label>
          <input
            id="pdf-page-input"
            type="number"
            min={1}
            max={numPages || 1}
            value={page}
            disabled={!doc}
            onChange={(e) => {
              const next = Number(e.target.value)
              if (Number.isInteger(next) && next >= 1 && next <= numPages) onPageChange(next)
            }}
            className="h-7 w-14 rounded-md border border-border bg-transparent px-2 text-center tabular-nums"
            data-testid="pdf-page-input"
          />
          <span>/ {numPages || '—'}</span>
        </span>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label={t('pdf.nextPage')}
          disabled={!doc || page >= numPages}
          onClick={() => onPageChange(page + 1)}
        >
          <ChevronRight className="size-4" />
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label={t('pdf.zoomOut')}
          disabled={!doc}
          onClick={() => zoom(-1)}
        >
          <ZoomOut className="size-4" />
        </Button>
        <span className="w-12 text-center text-sm tabular-nums text-muted-foreground">
          {Math.round(scale * 100)}%
        </span>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label={t('pdf.zoomIn')}
          disabled={!doc}
          onClick={() => zoom(1)}
        >
          <ZoomIn className="size-4" />
        </Button>
        <Button
          variant="outline"
          size="icon-sm"
          aria-label={t('pdf.rotate')}
          disabled={!doc}
          onClick={rotate}
        >
          <RotateCcw className="size-4" />
        </Button>
      </div>

      {(error || renderError) && (
        <p role="alert" className="text-sm text-destructive" data-testid="pdf-error">
          {error || renderError}
        </p>
      )}

      <div className="relative min-h-0 flex-1 overflow-auto rounded-xl border border-border bg-muted/30 p-3">
        {busy && !error && !renderError && (
          <p role="status" className="flex items-center gap-2 text-sm text-muted-foreground">
            <Loader2 className="size-4 animate-spin" />
            {t('pdf.loading')}
          </p>
        )}
        <div className="relative w-fit" data-testid="pdf-page-surface">
          <canvas
            ref={canvasRef}
            aria-label={t('pdf.canvasLabel')}
            className={doc ? 'block rounded-md shadow-sm' : 'hidden'}
            style={{ maxWidth: 'none' }}
          />
          {doc && <div className="pointer-events-none absolute inset-0">{overlay}</div>}
        </div>
      </div>
    </div>
  )
}
