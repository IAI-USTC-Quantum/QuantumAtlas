import type { Page } from '@playwright/test'

export type ProbeResult = {
  outcome: 'success' | 'rejected' | 'timeout'
  rejectionText: string | null
  openToDOMMs: number | null
  openToPaintOpportunityMs: number
  maxLongTaskMs: number
  longTaskCount: number
  totalLongTaskMs: number
  maxHeartbeatGapMs: number
  maxFrameGapMs: number
  heartbeatSamples: number
  frameSamples: number
  domElements: number
  formulaCount: number
  tableCount: number
  codeBlockCount: number
  headingCount: number
  textLength: number
  hasTerminalSentinel: boolean
  longTasksSupported: boolean
}

export async function settlePage(page: Page) {
  await page.evaluate(async () => {
    await document.fonts.ready
    await new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve())))
  })
}

export async function installProbe(page: Page, terminalSentinel: string) {
  await page.evaluate(({ terminalSentinel }) => {
    let startedAt = 0
    let endedAt = 0
    let domAt: number | null = null
    let finishing = false
    let lastHeartbeat = 0
    let lastFrame = 0
    let heartbeatId = 0
    let frameId = 0
    let timeoutId = 0
    const heartbeatGaps: number[] = []
    const frameGaps: number[] = []
    const longTasks: { start: number; duration: number }[] = []
    let resolve!: (result: ProbeResult) => void
    const result = new Promise<ProbeResult>((done) => { resolve = done })
    const longTasksSupported = PerformanceObserver.supportedEntryTypes.includes('longtask')
    const performanceObserver = new PerformanceObserver((list) => {
      for (const entry of list.getEntries()) longTasks.push({ start: entry.startTime, duration: entry.duration })
    })
    if (longTasksSupported) performanceObserver.observe({ type: 'longtask' })

    const finish = async (outcome: ProbeResult['outcome'], rejectionText: string | null = null) => {
      if (finishing) return
      finishing = true
      mutationObserver.disconnect()
      clearTimeout(timeoutId)
      domAt = outcome === 'timeout' ? null : performance.now()
      // Force style/layout so fonts used by the committed tree are discovered.
      // This models the visible viewport, not rasterization of offscreen content.
      document.querySelector('[data-testid="markdown-preview"]')?.getBoundingClientRect()
      await document.fonts.ready
      await new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done())))
      endedAt = performance.now()
      heartbeatGaps.push(endedAt - lastHeartbeat)
      frameGaps.push(endedAt - lastFrame)
      clearInterval(heartbeatId)
      cancelAnimationFrame(frameId)
      document.removeEventListener('click', onClick, true)
      // Let the PerformanceObserver deliver completed tasks without adding this
      // collection delay to the measured window. No DOM counting inside the fence.
      await new Promise<void>((done) => setTimeout(done, 60))
      for (const entry of performanceObserver.takeRecords()) longTasks.push({ start: entry.startTime, duration: entry.duration })
      performanceObserver.disconnect()
      const relevant = longTasks.filter(({ start, duration }) => start < endedAt && start + duration > startedAt)
      const root = document.querySelector('[data-testid="markdown-rendered"]')
      resolve({
        outcome, rejectionText,
        openToDOMMs: domAt === null ? null : domAt - startedAt,
        openToPaintOpportunityMs: endedAt - startedAt,
        maxLongTaskMs: Math.max(0, ...relevant.map(({ duration }) => duration)),
        longTaskCount: relevant.length,
        totalLongTaskMs: relevant.reduce((sum, { duration }) => sum + duration, 0),
        maxHeartbeatGapMs: Math.max(0, ...heartbeatGaps),
        maxFrameGapMs: Math.max(0, ...frameGaps),
        heartbeatSamples: heartbeatGaps.length,
        frameSamples: frameGaps.length,
        domElements: root?.querySelectorAll('*').length ?? 0,
        formulaCount: root?.querySelectorAll('.katex').length ?? 0,
        tableCount: root?.querySelectorAll('table').length ?? 0,
        codeBlockCount: root?.querySelectorAll('pre > code').length ?? 0,
        headingCount: root?.querySelectorAll('h1,h2,h3,h4,h5,h6').length ?? 0,
        textLength: root?.textContent?.length ?? 0,
        hasTerminalSentinel: root?.textContent?.includes(terminalSentinel) ?? false,
        longTasksSupported,
      })
    }
    const checkDOM = () => {
      if (!startedAt || finishing) return
      const preview = document.querySelector('[data-testid="markdown-preview"]')
      if (preview?.querySelector('[data-testid="markdown-rendered"]')) {
        void finish('success')
        return
      }
      const alert = preview?.querySelector('[role="alert"]')
      // All corpus inputs are within 200k. Keep explicit safety rejections visible
      // rather than misclassifying a fast fallback as fast rendering.
      const tooLarge = Array.from(preview?.querySelectorAll('[role="status"]') ?? [])
        .find((node) => /too large|too long|size limit/i.test(node.textContent ?? ''))
      const rejection = alert ?? tooLarge
      if (rejection) void finish('rejected', rejection.textContent)
    }
    const mutationObserver = new MutationObserver(checkDOM)
    const onClick = (event: MouseEvent) => {
      const target = event.target instanceof Element ? event.target.closest('button') : null
      if (startedAt || target?.textContent?.trim() !== 'Preview Markdown') return
      startedAt = performance.now()
      lastHeartbeat = startedAt
      lastFrame = startedAt
      heartbeatId = window.setInterval(() => {
        const now = performance.now()
        heartbeatGaps.push(now - lastHeartbeat)
        lastHeartbeat = now
      }, 10)
      const frame = () => {
        const now = performance.now()
        frameGaps.push(now - lastFrame)
        lastFrame = now
        frameId = requestAnimationFrame(frame)
      }
      frameId = requestAnimationFrame(frame)
      mutationObserver.observe(document.body, {
        childList: true, subtree: true, characterData: true,
        // React can reuse the rendering-status paragraph as an alert on failure.
        attributes: true, attributeFilter: ['role'],
      })
      // An operational watchdog, NOT a latency pass threshold. A wedged render
      // also has Playwright's outer test timeout because JS timers cannot preempt.
      timeoutId = window.setTimeout(() => { void finish('timeout') }, 30_000)
    }
    document.addEventListener('click', onClick, true)
    Reflect.set(window, '__markdownPerformanceProbe', { result })
  }, { terminalSentinel })
}

export async function readProbe(page: Page): Promise<ProbeResult> {
  return page.evaluate(() => Reflect.get(window, '__markdownPerformanceProbe').result)
}
