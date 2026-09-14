import type { Page } from '@playwright/test'
import {
  test, expect, deferred, labels, openPreview, selectAssetPreview, expectExactSource, controlMarkdownWorkers,
  MARKDOWN, PAPER_ID, SECOND_PAPER_ID, TITLE, SECOND_TITLE,
  type EntryPoint, type MarkdownResponse,
} from './fixtures'

const entries: EntryPoint[] = ['paper', 'assets']
const variants = [
  { language: 'en', theme: 'light', viewport: { width: 1440, height: 1050 } },
  { language: 'en', theme: 'dark', viewport: { width: 390, height: 844 } },
  { language: 'zh', theme: 'light', viewport: { width: 390, height: 844 } },
  { language: 'zh', theme: 'dark', viewport: { width: 1440, height: 1050 } },
] as const

// Deliberately independent of the production validator: these are untrusted
// Worker replies, not trees pre-approved by the renderer under test.
function treeReply(...children: unknown[]) {
  return { ok: true, treeJson: JSON.stringify({ type: 'root', children }) }
}

function headingReply(text: string) {
  return treeReply({ type: 'element', tagName: 'h1', properties: {}, children: [{ type: 'text', value: text }] })
}

const invalidTreeReplies = [
  { name: 'invalid tree JSON', reply: () => ({ ok: true, treeJson: '{"type":"root","children":[' }) },
  { name: 'raw HTML nodes', reply: () => treeReply({ type: 'raw', value: '<img data-xss="worker-raw" src="https://markdown-fixture.invalid/worker.png" onerror="window.__markdownXss=1">' }) },
  { name: 'MDX JSX nodes', reply: () => treeReply({ type: 'mdxJsxFlowElement', name: 'script', attributes: [], children: [] }) },
  ...[
    { name: 'onClick', properties: { onClick: 'window.__markdownXss=1' } },
    { name: 'dangerouslySetInnerHTML', properties: { dangerouslySetInnerHTML: { __html: '<img data-xss="worker-prop" src="https://markdown-fixture.invalid/worker-prop.png">' } } },
    { name: 'is', properties: { is: 'markdown-untrusted-element' } },
  ].map(({ name, properties }) => ({
    name: `unsafe ${name} props`,
    reply: () => treeReply({ type: 'element', tagName: 'span', properties, children: [] }),
  })),
]

const overBudgetTrees = [
  { name: '2,000,000-character serialized reply', reply: () => {
    const reply = treeReply({ type: 'text', value: 'x'.repeat(2_000_000) })
    expect(reply.treeJson.length).toBeGreaterThan(2_000_000)
    return reply
  } },
  { name: '20,000-node tree', reply: () => {
    const branch = () => ({
      type: 'element', tagName: 'span', properties: {},
      children: Array.from({ length: 10_000 }, () => ({ type: 'text', value: 'x' })),
    })
    const reply = treeReply(branch(), branch())
    // Each child array and the serialized length fit their caps; the cumulative
    // 20,003 nodes must still exceed the independent whole-tree node budget.
    expect(reply.treeJson.length).toBeLessThan(2_000_000)
    return reply
  } },
  { name: '64-level tree depth', reply: () => {
    let node: unknown = { type: 'text', value: 'Deep Worker tree' }
    for (let depth = 0; depth < 65; depth++) {
      node = { type: 'element', tagName: 'span', properties: {}, children: [node] }
    }
    const reply = treeReply(node)
    expect(reply.treeJson.length).toBeLessThan(2_000_000)
    return reply
  } },
]

async function expectBusyWorker(workers: Awaited<ReturnType<typeof controlMarkdownWorkers>>, source: string) {
  let pending: Awaited<ReturnType<typeof workers.snapshots>> = []
  await expect.poll(async () => {
    pending = (await workers.snapshots()).filter((worker) => worker.source === source && worker.terminationCalls === 0)
    return pending
  }, 'The real Markdown Worker must receive the expected source job').toHaveLength(1)
  // Return the same successful observation. A second read can race cleanup and
  // return undefined, hiding the real native Worker error behind a test TypeError.
  return pending[0]
}

async function expectRendered(page: Page) {
  const rendered = page.getByTestId('markdown-rendered')
  await expect(rendered).toBeVisible()
  await expect(rendered.getByRole('heading', { name: 'Markdown regression / 量子公式', exact: true })).toBeVisible()
  await expect(rendered.locator('strong').first()).toHaveText('bold result')
  await expect(rendered.locator('em').first()).toHaveText('emphasis')
  await expect(rendered.locator('del')).toHaveText('obsolete claim')
  await expect(rendered.locator('table')).toHaveCount(1)
  await expect(rendered.locator('table tbody tr')).toHaveCount(2)
  for (const [column, alignment] of [[1, 'right'], [2, 'center']] as const) {
    await expect.poll(() => rendered.locator(`th:nth-child(${column}), td:nth-child(${column})`)
      .evaluateAll((cells) => cells.map((cell) => getComputedStyle(cell).textAlign)),
    'GFM alignment must survive sanitization and the bundled table CSS').toEqual([alignment, alignment, alignment])
  }
  await expect(rendered.locator('blockquote')).toContainText('important context')
  await expect(rendered.locator('ul > li')).toHaveCount(2)
  await expect(rendered.locator('ol > li')).toHaveCount(2)

  // All four delimiters must become accessible MathML, with block math distinct
  // from inline math. Neither fenced code nor inline code is parsed as math.
  const tex = await rendered.locator('math annotation[encoding="application/x-tex"]').allTextContents()
  expect(tex).toEqual(expect.arrayContaining([
    'E=mc^2', 'a^2+b^2=c^2', '\\sqrt{x^2 + 1}',
    expect.stringContaining('\\int_0^1'), expect.stringContaining('\\sum_{n=1}^{N}'),
  ]))
  await expect(rendered.locator('.katex-display')).toHaveCount(2)
  // Check the actual browser DOM, not serialized tag names: React must create
  // MathML and SVG namespaces, including descendants and the surrounding HTML.
  const mathNamespaces = await rendered.locator('.katex-mathml math, .katex-mathml math *')
    .evaluateAll((nodes) => [...new Set(nodes.map((node) => node.namespaceURI))])
  expect(mathNamespaces).toEqual(['http://www.w3.org/1998/Math/MathML'])
  const svg = rendered.locator('.katex-html svg')
  expect(await svg.count(), 'The square-root fixture must exercise a real SVG').toBeGreaterThan(0)
  expect(await svg.locator('path').count(), 'The KaTeX SVG must retain its inert geometry').toBeGreaterThan(0)
  expect(await rendered.locator('.katex-html svg, .katex-html svg *').evaluateAll((nodes) =>
    [...new Set(nodes.map((node) => node.namespaceURI))],
  )).toEqual(['http://www.w3.org/2000/svg'])
  expect(await svg.evaluateAll((nodes) => nodes.every((node) => node instanceof SVGElement))).toBe(true)
  expect(await rendered.locator('.katex-html').evaluateAll((nodes) =>
    nodes.every((node) => node instanceof HTMLElement && node.namespaceURI === 'http://www.w3.org/1999/xhtml'),
  )).toBe(true)
  await expect(rendered.locator('code .katex, code math')).toHaveCount(0)
  await expect(rendered.locator('pre code')).toContainText('const price = "$99";')
  await expect(rendered.locator('pre code')).toContainText('$$not_math$$')
  await expect(rendered.locator('code').filter({ hasText: '$not_math$' }).first()).toContainText('$not_math$')
  await expect(rendered.getByRole('heading', { name: 'After invalid math', exact: true })).toHaveCount(1)
  await expect(rendered).toContainText('The document still renders.')
  await expect(page.getByTestId('markdown-preview').getByRole('status')).toHaveCount(0)
  return rendered
}

async function expectSafeMarkup(page: Page) {
  const rendered = page.getByTestId('markdown-rendered')
  await expect(rendered.locator('img, script, iframe, object, embed, form, input, [data-xss], .injected-math')).toHaveCount(0)
  expect(await rendered.evaluate((element) => [...element.querySelectorAll('*')].flatMap((node) =>
    [...node.attributes].filter((attribute) => /^on/i.test(attribute.name)).map((attribute) => attribute.name),
  ))).toEqual([])
  await expect(rendered).toContainText('<script>window.__markdownXss=1;alert(\'markdown-fixture\')</script>')
  await expect(rendered).toContainText('<img data-xss="raw-image"')
  await expect(rendered).toContainText('<svg data-xss="raw-svg"')
  await expect(rendered).toContainText('remote diagram')
  await expect(rendered).toContainText('local diagram')
  expect(await page.evaluate(() => Reflect.get(window, '__markdownXss') ?? 0)).toBe(0)

  const anchors = await rendered.locator('a').evaluateAll((nodes) => nodes.map((node) => ({
    href: node.getAttribute('href'), target: node.getAttribute('target'), rel: node.getAttribute('rel'),
  })))
  expect(anchors.map(({ href }) => href).sort()).toEqual([
    '#markdown-regression', 'http://example.invalid/reference',
    'https://example.invalid/reference', 'mailto:reader@example.invalid',
  ].sort())
  for (const anchor of anchors) {
    if (anchor.href === '#markdown-regression') {
      // Fragment-only navigation is allowed and stays in this document.
      expect(anchor.target).toBeNull()
      expect(anchor.rel).toBeNull()
    } else {
      expect(anchor.target).toBe('_blank')
      expect(anchor.rel?.split(/\s+/)).toEqual(expect.arrayContaining(['noopener', 'noreferrer']))
    }
  }
  // Invalid and app-relative paths retain their label, but never become active
  // navigation. Even a safe URL in a KaTeX trust command is not a trusted link.
  await expect(rendered).toContainText('Relative API link')
  await expect(rendered).toContainText('JavaScript link')
}

for (const entry of entries) {
  for (const variant of variants) {
    test.describe(`${entry}: ${variant.language}/${variant.theme}/${variant.viewport.width}px`, () => {
      test.use({ ...variant, locale: variant.language === 'zh' ? 'zh-CN' : 'en-US', colorScheme: variant.theme })

      test('renders secure Markdown and local math, preserves Source, supports keyboard tabs', async ({ page, api }, testInfo) => {
        const preview = await openPreview(page, entry, variant.language)
        const renderedTab = preview.getByRole('tab', { name: labels[variant.language].rendered, exact: true })
        const sourceTab = preview.getByRole('tab', { name: labels[variant.language].source, exact: true })
        await expect(renderedTab).toHaveAttribute('aria-selected', 'true')
        await expect(sourceTab).toHaveAttribute('aria-selected', 'false')
        await expect(sourceTab).toBeEnabled()
        await expectRendered(page)
        await expectSafeMarkup(page)
        await expect(page.locator('html')).toHaveClass(new RegExp(`\\b${variant.theme}\\b`))

        const loadedMathFonts = await page.evaluate(async () => {
          await document.fonts.ready
          return [...document.fonts].filter((font) => font.family.includes('KaTeX') && font.status === 'loaded').map((font) => font.family)
        })
        expect(loadedMathFonts.length, 'KaTeX must use successfully loaded bundled fonts').toBeGreaterThan(0)
        expect(api.requests.some(({ path, type }) => type === 'font' && path.startsWith('/assets/'))).toBe(true)

        // Screenshot evidence is attached under test-results, not an implicitly
        // generated golden baseline. The DOM/layout assertions remain portable.
        const dialog = page.getByRole('dialog')
        const bounds = await dialog.boundingBox()
        expect(bounds).not.toBeNull()
        expect(bounds!.x).toBeGreaterThanOrEqual(-1)
        expect(bounds!.x + bounds!.width).toBeLessThanOrEqual(variant.viewport.width + 1)
        await testInfo.attach(`rendered-${entry}-${variant.language}-${variant.theme}-${variant.viewport.width}`, {
          body: await page.screenshot({ fullPage: true, animations: 'disabled' }), contentType: 'image/png',
        })

        await renderedTab.focus()
        await page.keyboard.press('ArrowRight')
        await expect(sourceTab).toBeFocused()
        await page.keyboard.press('Enter') // Works with automatic or manual tab activation.
        await expect(sourceTab).toHaveAttribute('aria-selected', 'true')
        await expectExactSource(page, variant.language, MARKDOWN)
        await page.keyboard.press('ArrowLeft')
        await expect(renderedTab).toBeFocused()
        await page.keyboard.press('Enter')
        await expect(renderedTab).toHaveAttribute('aria-selected', 'true')
        await expectRendered(page)
      })
    })
  }

  test.describe(`${entry}: lifecycle and failures`, () => {
    test('loading is distinct from an empty successful Markdown response', async ({ page, api }) => {
      const pending = deferred<MarkdownResponse>()
      api.assetResponse = () => pending.promise
      try {
        const preview = await openPreview(page, entry, 'en')
        await expect(page.getByRole('dialog').getByRole('status')).toBeVisible()
        await expect(page.getByRole('dialog').getByRole('alert')).toHaveCount(0)
        pending.resolve({ body: '' })
        await expect(page.getByRole('dialog').getByRole('status')).toHaveCount(0)
        await expect(preview).toBeVisible()
        await expect(preview).toContainText('This document is empty.')
        await expectExactSource(page, 'en', '')
        await preview.getByRole('tab', { name: 'Rendered', exact: true }).click()
        await expect(page.getByRole('dialog').getByRole('status')).toHaveCount(0)
        await expect(page.getByRole('dialog').getByRole('alert')).toHaveCount(0)
      } finally {
        pending.resolve({ body: '' })
      }
    })

    test('fetch failure is not a perpetual loading indicator', async ({ page, api }) => {
      api.assetResponse = () => ({ status: 503, body: 'Synthetic Markdown fetch unavailable' })
      await openPreview(page, entry, 'en')
      await expect(page.getByRole('dialog').getByRole('alert')).toBeVisible()
      await expect(page.getByRole('dialog').getByRole('alert')).toContainText('503')
      await expect(page.getByRole('dialog').getByRole('status')).toHaveCount(0)
      await expect(page.getByTestId('markdown-rendered')).toHaveCount(0)
    })

    test('Worker startup failure leaves exact Source available', async ({ page, context }) => {
      await context.addInitScript(() => {
        window.Worker = new Proxy(window.Worker, {
          construct() { throw new Error('Synthetic Worker startup failure') },
        })
      })
      const preview = await openPreview(page, entry, 'en')
      await expect(preview.getByRole('alert')).toBeVisible()
      await expect(preview.getByRole('status')).toHaveCount(0)
      await expectExactSource(page, 'en', MARKDOWN)
    })

    for (const scenario of [...invalidTreeReplies, ...overBudgetTrees]) {
      test(`rejects ${scenario.name} from a Worker and preserves exact Source`, async ({ page }) => {
        const workers = await controlMarkdownWorkers(page)
        const preview = await openPreview(page, entry, 'en')
        const worker = await expectBusyWorker(workers, MARKDOWN)
        await workers.reply(worker.id, scenario.reply())
        await expect(preview.getByRole('alert')).toBeVisible()
        await expect(preview.getByRole('status')).toHaveCount(0)
        await expect(preview.getByTestId('markdown-rendered')).toHaveCount(0)
        await expect(preview.locator('[data-xss], [is], script, img, iframe')).toHaveCount(0)
        expect(await page.evaluate(() => Reflect.get(window, '__markdownXss') ?? 0)).toBe(0)
        await expect.poll(async () => (await workers.snapshots()).find(({ id }) => id === worker.id)?.terminationCalls,
          'An invalid tree must stop its native Worker before switching to Source').toBeGreaterThan(0)
        await expect(preview.getByRole('tab', { name: 'Source', exact: true })).toBeEnabled()
        await expectExactSource(page, 'en', MARKDOWN)
      })
    }

    test('an unsafe Worker-supplied href becomes inert rendered text and preserves Source', async ({ page }) => {
      const workers = await controlMarkdownWorkers(page)
      const preview = await openPreview(page, entry, 'en')
      const worker = await expectBusyWorker(workers, MARKDOWN)
      await workers.reply(worker.id, treeReply({
        type: 'element', tagName: 'a', properties: { href: 'javascript:window.__markdownXss=1' },
        children: [{ type: 'text', value: 'Unsafe Worker link' }],
      }))
      const rendered = preview.getByTestId('markdown-rendered')
      await expect(rendered).toBeVisible()
      const label = rendered.getByText('Unsafe Worker link', { exact: true })
      await expect(label).toBeVisible()
      expect(await label.evaluate((element) => element.tagName)).toBe('SPAN')
      await expect(rendered.locator('a, [href], [target]')).toHaveCount(0)
      await expect(preview.getByRole('alert')).toHaveCount(0)
      await expect(preview.getByRole('status')).toHaveCount(0)
      const originalURL = page.url()
      await label.click()
      expect(page.url()).toBe(originalURL)
      expect(await page.evaluate(() => Reflect.get(window, '__markdownXss') ?? 0)).toBe(0)
      // The unchanged automatic fixture also rejects external or unexpected
      // local/API requests, including any triggered by this inert label.
      await expect.poll(async () => (await workers.snapshots()).find(({ id }) => id === worker.id)?.terminationCalls).toBeGreaterThan(0)
      await expectExactSource(page, 'en', MARKDOWN)
    })

    test('unresponsive Worker times out without losing Source or accepting a stale reply', async ({ page }) => {
      const workers = await controlMarkdownWorkers(page)
      const preview = await openPreview(page, entry, 'en')
      await expect(preview.getByRole('status')).toBeVisible()
      const worker = await expectBusyWorker(workers, MARKDOWN)
      await expect(preview.getByRole('tab', { name: 'Source', exact: true })).toBeEnabled()
      // Keep Rendered selected: switching to Source intentionally cancels work.
      await expect(preview.getByRole('alert')).toBeVisible({ timeout: 12_000 })
      await expect(preview.getByRole('status')).toHaveCount(0)
      await expect.poll(async () => (await workers.snapshots()).find(({ id }) => id === worker.id)?.terminationCalls,
        'Timeout must call native Worker.terminate(), not merely hide the spinner').toBeGreaterThan(0)
      const stopped = (await workers.snapshots()).find(({ id }) => id === worker.id)!
      expect(worker.postedAt).not.toBeNull()
      expect(stopped.terminatedAt).not.toBeNull()
      // Measure in the browser's monotonic clock, not navigation/test duration.
      // A fast Worker startup error must never satisfy this five-second timeout.
      expect(stopped.terminatedAt! - worker.postedAt!, 'The parse deadline must actually elapse before termination').toBeGreaterThanOrEqual(4_900)
      await workers.reply(worker.id, headingReply('Stale reply after timeout'), true)
      await expect(preview.getByRole('alert')).toBeVisible()
      await expect(preview.getByTestId('markdown-rendered')).toHaveCount(0)
      await expect(preview).not.toContainText('Stale reply after timeout')
      await expectExactSource(page, 'en', MARKDOWN)
    })

    test('Source is immediately usable and terminates the busy Worker on tab switch', async ({ page }) => {
      const workers = await controlMarkdownWorkers(page)
      const preview = await openPreview(page, entry, 'en')
      await expect(preview.getByRole('status')).toBeVisible()
      const worker = await expectBusyWorker(workers, MARKDOWN)
      await expectExactSource(page, 'en', MARKDOWN)
      await expect.poll(async () => (await workers.snapshots()).find(({ id }) => id === worker.id)?.terminationCalls,
        'Switching to Source must terminate the running parser').toBeGreaterThan(0)
      await workers.reply(worker.id, headingReply('Stale reply after tab switch'), true)
      await expect(preview.getByRole('tab', { name: 'Source', exact: true })).toHaveAttribute('aria-selected', 'true')
      await expect(preview.getByTestId('markdown-rendered')).toHaveCount(0)
      await expectExactSource(page, 'en', MARKDOWN)
    })

    test('closing the dialog terminates its Worker and an old reply cannot replace a reopened preview', async ({ page }) => {
      const workers = await controlMarkdownWorkers(page)
      await openPreview(page, entry, 'en')
      const oldWorker = await expectBusyWorker(workers, MARKDOWN)
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect.poll(async () => (await workers.snapshots()).find(({ id }) => id === oldWorker.id)?.terminationCalls,
        'Unmounting the preview must terminate the parser').toBeGreaterThan(0)
      // Reopen in this same browser document so the obsolete callback survives.
      if (entry === 'assets') await selectAssetPreview(page, 'en', PAPER_ID)
      else await page.getByRole('button', { name: 'Preview Markdown', exact: true }).click()
      const preview = page.getByTestId('markdown-preview')
      const newWorker = await expectBusyWorker(workers, MARKDOWN)
      expect(newWorker.id).not.toBe(oldWorker.id)
      await workers.reply(newWorker.id, headingReply('Fresh Worker tree'))
      await expect(preview.getByTestId('markdown-rendered').getByRole('heading', { name: 'Fresh Worker tree', exact: true })).toBeVisible()
      await expect.poll(async () => (await workers.snapshots()).find(({ id }) => id === newWorker.id)?.terminationCalls,
        'A successful reply must also release its disposable Worker').toBeGreaterThan(0)
      await workers.reply(oldWorker.id, headingReply('Obsolete Worker tree'), true)
      expect((await workers.snapshots()).find(({ id }) => id === oldWorker.id)?.replies).toBe(1)
      await expect(preview.getByTestId('markdown-rendered').getByRole('heading', { name: 'Fresh Worker tree', exact: true })).toBeVisible()
      await expect(preview).not.toContainText('Obsolete Worker tree')
      await expect(preview.getByRole('alert')).toHaveCount(0)
      await expectExactSource(page, 'en', MARKDOWN)
    })

    test('oversized Markdown stays readable without starting a parser Worker', async ({ page, context, api }) => {
      const oversized = '# Large document\n' + 'Synthetic plain text.\n'.repeat(10_000)
      expect(oversized.length).toBeGreaterThan(200_000)
      api.documents[PAPER_ID].markdown = oversized
      await context.addInitScript(() => {
        Reflect.set(window, '__markdownWorkerStarts', 0)
        window.Worker = class extends Worker {
          constructor(url: string | URL, options?: WorkerOptions) {
            Reflect.set(window, '__markdownWorkerStarts', Number(Reflect.get(window, '__markdownWorkerStarts')) + 1)
            super(url, options)
          }
        }
      })
      const preview = await openPreview(page, entry, 'en')
      await expect(preview.getByRole('status')).toContainText('200,000-character')
      await expectExactSource(page, 'en', oversized)
      expect(await page.evaluate(() => Reflect.get(window, '__markdownWorkerStarts'))).toBe(0)
    })
  })
}

for (const language of ['en', 'zh'] as const) {
  test.describe(`non-admin ${language}`, () => {
    test.use({ admin: false, language })
    test('neither route exposes previews or requests admin assets', async ({ page, api }) => {
      await page.goto(`/${language}/papers/${PAPER_ID}`)
      await expect(page.locator('main h1')).toHaveText(TITLE)
      await expect.poll(() => api.requests.some(({ path }) => path === '/api/admin/whoami')).toBe(true)
      await expect(page.getByRole('button', { name: labels[language].paperPreview, exact: true })).toHaveCount(0)
      await expect(page.getByTestId('markdown-preview')).toHaveCount(0)
      await page.goto(`/${language}/admin/assets`)
      await expect(page.locator('main').getByRole('alert')).toContainText(language === 'zh'
        ? '此页面仅对服务器管理员开放。' : 'This page is restricted to server administrators.')
      await expect(page.locator('main input')).toHaveCount(0)
      await expect(page.getByTestId('markdown-preview')).toHaveCount(0)
      expect(api.requests.filter(({ path }) => path.startsWith('/api/admin/assets'))).toEqual([])
    })
  })
}

for (const entry of entries) {
  test(`${entry}: switching to another non-admin account clears the open preview and old content`, async ({ page, api }) => {
    const oldMarker = 'ACCOUNT_A_MARKDOWN_MUST_NOT_LEAK'
    const oldMarkdown = `# Admin-only fixture\n\n${oldMarker}\n`
    api.documents[PAPER_ID].markdown = oldMarkdown
    await openPreview(page, entry, 'en')
    await expectExactSource(page, 'en', oldMarkdown)
    await expect(page.getByRole('dialog')).toContainText(oldMarker)

    const nextAccount = { id: 'markdownuser002', admin: false }
    const permissions = deferred<void>()
    api.beforeWhoami = (account) => account.id === nextAccount.id ? permissions.promise : Promise.resolve()
    const sameDocument = await page.evaluate(() => {
      Reflect.set(window, '__accountSwitchDocument', 'preserve-this-page')
      return location.href
    })
    const permissionResponse = page.waitForResponse((response) =>
      new URL(response.url()).pathname === '/api/admin/whoami' && response.request().method() === 'GET')
    try {
      await api.switchAccount(page, nextAccount)
      await expect.poll(() => api.requests.some(({ path, userId }) =>
        path === '/api/admin/whoami' && userId === nextAccount.id)).toBe(true)
      // While the new account's whoami is still pending, neither the previous
      // role nor its already-rendered/cached Markdown may remain visible.
      await expect(page.getByTestId('markdown-preview')).toHaveCount(0)
      await expect(page.getByRole('dialog')).toHaveCount(0)
      await expect(page.getByText(oldMarker, { exact: false })).toHaveCount(0)

      permissions.resolve()
      await (await permissionResponse).finished()
      if (entry === 'assets') {
        await expect(page.locator('main').getByRole('alert')).toContainText('This page is restricted to server administrators.')
        await expect(page.locator('main input')).toHaveCount(0)
      } else {
        await expect(page.locator('main h1')).toHaveText(TITLE)
        await expect(page.getByRole('button', { name: 'Preview Markdown', exact: true })).toHaveCount(0)
      }
      await expect(page.getByTestId('markdown-rendered')).toHaveCount(0)
      await expect(page.getByTestId('markdown-source')).toHaveCount(0)
      await expect(page.getByText(oldMarker, { exact: false })).toHaveCount(0)
      expect(api.requests.filter(({ userId, path }) =>
        userId === nextAccount.id && path.startsWith('/api/admin/assets'))).toEqual([])
      // A reload would drop the cache and give this regression a false pass.
      expect(page.url()).toBe(sameDocument)
      expect(await page.evaluate(() => Reflect.get(window, '__accountSwitchDocument'))).toBe('preserve-this-page')
    } finally {
      permissions.resolve()
      // If a broken query key never issued whoami, avoid leaving a rejected
      // waiter unhandled during failure teardown.
      await permissionResponse.catch(() => undefined)
    }
  })

  test(`${entry}: delayed old document cannot overwrite the newly selected document`, async ({ page, api }) => {
    const oldResponse = deferred<MarkdownResponse>()
    const oldRequested = deferred<void>()
    const oldText = '# Obsolete document\n\nThis late response must never replace document two.\n'
    const newText = api.documents[SECOND_PAPER_ID].markdown
    api.assetResponse = (id) => {
      if (id === PAPER_ID) {
        oldRequested.resolve()
        return oldResponse.promise
      }
      return { body: newText }
    }
    try {
      await openPreview(page, entry, 'en')
      await oldRequested.promise
      await page.keyboard.press('Escape')
      await expect(page.getByRole('dialog')).toHaveCount(0)
      if (entry === 'assets') {
        await selectAssetPreview(page, 'en', SECOND_PAPER_ID)
      } else {
        // Use the browser history API (not page.goto, which destroys the old
        // document). This exercises the real router with an in-flight fetch and
        // the same mounted paper-detail component receiving new route params.
        await page.evaluate((path) => {
          history.pushState({}, '', path)
          dispatchEvent(new PopStateEvent('popstate', { state: history.state }))
        }, `/en/papers/${SECOND_PAPER_ID}`)
        await expect(page.locator('main h1')).toHaveText(SECOND_TITLE)
        await page.getByRole('button', { name: 'Preview Markdown', exact: true }).click()
      }
      await expect(page.getByTestId('markdown-rendered').getByRole('heading', { name: 'Second document', exact: true })).toBeVisible()
      oldResponse.resolve({ body: oldText })
      await expect.poll(() => api.deliveredAssets.includes(PAPER_ID)).toBe(true)
      // Let browser fetch consumers and React paint after the late delivery;
      // no fixed sleeps or networkidle (the old request is deliberately held).
      await page.evaluate(() => new Promise<void>((resolve) => {
        requestAnimationFrame(() => requestAnimationFrame(() => resolve()))
      }))
      await expectExactSource(page, 'en', newText)
      await page.getByRole('tab', { name: 'Rendered', exact: true }).click()
      await expect(page.getByTestId('markdown-rendered').getByRole('heading', { name: 'Second document', exact: true })).toBeVisible()
      await expect(page.getByTestId('markdown-preview')).not.toContainText('Obsolete document')
      await expectExactSource(page, 'en', newText)
    } finally {
      oldResponse.resolve({ body: oldText })
    }
  })
}
