import type { Page } from '@playwright/test'
import {
  test, expect, deferred, labels, openPreview, selectAssetPreview, expectExactSource,
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

    test('uses react-markdown even when Worker construction is unavailable', async ({ page, context }) => {
      await context.addInitScript(() => {
        window.Worker = new Proxy(window.Worker, {
          construct() { throw new Error('Workers deliberately unavailable') },
        })
      })
      await openPreview(page, entry, 'en')
      const rendered = await expectRendered(page)
      await expect(rendered).toHaveAttribute('data-renderer', 'react-markdown')
      await expectExactSource(page, 'en', MARKDOWN)
    })

    // Real source inputs, not forged Worker replies: this renderer no longer has
    // a Worker trust boundary or an interruptible five-second parsing deadline.
    for (const scenario of [
      { name: 'formula count', source: '$x$ '.repeat(201) },
      { name: 'unmatched TeX opener count', source: '\\('.repeat(10_000) },
      { name: 'Markdown depth', source: '> '.repeat(70) + 'Deep quote' },
      { name: 'Markdown node count', source: '# Heading\n\n'.repeat(10_000) },
      { name: 'expanded math output', source: (String.raw`$\sqrt{\frac{x}{y}}+\overrightarrow{AB}+\begin{pmatrix}a&b\\c&d\end{pmatrix}$` + '\n\n').repeat(200) },
    ]) {
      test(`${scenario.name} guard keeps Source and recovers on a new document`, async ({ page, api }) => {
        expect(scenario.source.length).toBeLessThanOrEqual(200_000)
        api.documents[PAPER_ID].markdown = scenario.source
        const preview = await openPreview(page, entry, 'en')
        await expect(preview.getByRole('alert')).toBeVisible()
        await expect(preview.getByRole('status')).toHaveCount(0)
        await expect(preview.getByTestId('markdown-rendered')).toHaveCount(0)
        await expectExactSource(page, 'en', scenario.source)
        await page.keyboard.press('Escape')
        await expect(page.getByRole('dialog')).toHaveCount(0)
        api.documents[PAPER_ID].markdown = MARKDOWN
        // Stay on the same page: a full reload would conceal stale boundary state.
        if (entry === 'assets') await selectAssetPreview(page, 'en', PAPER_ID)
        else await page.getByRole('button', { name: 'Preview Markdown', exact: true }).click()
        await expectRendered(page)
        await expectExactSource(page, 'en', MARKDOWN)
      })
    }

    test('oversized Markdown stays readable without invoking the renderer', async ({ page, api }) => {
      const oversized = '# Large document\n' + 'Synthetic plain text.\n'.repeat(10_000)
      expect(oversized.length).toBeGreaterThan(200_000)
      api.documents[PAPER_ID].markdown = oversized
      const preview = await openPreview(page, entry, 'en')
      await expect(preview.getByRole('status')).toContainText('200,000-character')
      await expect(preview.getByTestId('markdown-rendered')).toHaveCount(0)
      await expectExactSource(page, 'en', oversized)
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
