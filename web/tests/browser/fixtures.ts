import { test as base, expect, type Page } from '@playwright/test'

// API/session shapes follow build/security-browser-regression.py (read-only
// reference). These are synthetic UI fixtures, never real auth/authorization,
// database, object-store, OAuth, or backend integration tests.
export const PAPER_ID = 'markdown-paper-0001'
export const SECOND_PAPER_ID = 'markdown-paper-0002'
export const TITLE = 'Markdown Fixture Paper / Markdown 回归论文'
export const SECOND_TITLE = 'Second Fixture Paper / 第二篇论文'
const STAMP = '2026-01-01T00:00:00Z'

export const MARKDOWN = String.raw`# Markdown regression / 量子公式

A **bold result**, *emphasis*, ~~obsolete claim~~, and inline $E=mc^2$.

## Four math delimiters

$$
\int_0^1 x^2\,dx = \frac{1}{3}
$$

Parenthesized: \(a^2+b^2=c^2\).

Stretchy SVG: $\sqrt{x^2 + 1}$.

\[
\sum_{n=1}^{N} n = \frac{N(N+1)}{2}
\]

| Quantity | Value |
| ---: | :---: |
| Energy | $E=mc^2$ |
| Label | 中文 & Unicode 🧪 |

> A quoted result with **important context**.

- First result
- Second result

1. Prepare
2. Measure

Inline code: ` + '`$not_math$ \\(not_math\\)`' + String.raw`.

` + '```typescript\nconst price = "$99";\nconst math = "\\\\(still_code\\\\)";\n// $$not_math$$\n```' + String.raw`

[HTTPS reference](https://example.invalid/reference)
[HTTP reference](http://example.invalid/reference)
[Email reference](mailto:reader@example.invalid)
[Relative API link](/api/me)
[Relative document](other.md)
[Fragment](#markdown-regression)
[Protocol relative](//example.invalid/escape)
[JavaScript link](javascript:alert%281%29)
[Encoded JavaScript](jav&#x61;script:alert%281%29)
[Data link](data:text/html,unsafe)

![remote diagram](https://markdown-fixture.invalid/remote.png)
![local diagram](/api/admin/assets/should-not-load/image/inline)
![data diagram](data:image/svg+xml;base64,PHN2Zy8+)

<img data-xss="raw-image" src="https://markdown-fixture.invalid/raw.png" onerror="window.__markdownXss=1">
<script>window.__markdownXss=1;alert('markdown-fixture')</script>
<svg data-xss="raw-svg" onload="window.__markdownXss=1"></svg>
<iframe src="https://markdown-fixture.invalid/frame"></iframe>

KaTeX trust commands must stay inert:

$\href{javascript:alert(1)}{click}$

$\href{https://markdown-fixture.invalid/math-link}{math-link}$

$\includegraphics{https://markdown-fixture.invalid/math.png}$

$\htmlClass{injected-math}{x}$

$\htmlStyle{background:url(https://markdown-fixture.invalid/css)}{x}$

Invalid math must not crash: $\frac{1}{$.

## After invalid math

The document still renders. Literal entities: & < > " '.
`

export type Language = 'en' | 'zh'
export type EntryPoint = 'paper' | 'assets'
export type FixtureDocument = { title: string; markdown: string }
export type MarkdownResponse = { body: string; status?: number }
export type SyntheticAccount = { id: string; admin: boolean }
export type ApiFixtures = {
  documents: Record<string, FixtureDocument>
  requests: { method: string; path: string; type: string; userId: string }[]
  deliveredAssets: string[]
  assetResponse?: (paperId: string, requestNumber: number) => Promise<MarkdownResponse> | MarkdownResponse
  beforeWhoami?: (account: SyntheticAccount) => Promise<void>
  switchAccount: (page: Page, account: SyntheticAccount) => Promise<void>
}

type Options = { admin: boolean; language: Language; theme: 'light' | 'dark' }

export function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

function paper(id: string, document: FixtureDocument) {
  return {
    paper_id: id, title: document.title, status: 'ready', authors: ['Synthetic Author'],
    arxiv_id: id === PAPER_ID ? '2601.00001' : '2601.00002',
    created_at: STAMP, updated_at: STAMP, has_pdf: false, has_md: true, image_count: 0,
    assets: [{ asset_id: 1, source: 'arxiv', arxiv_version: 1, pdf_size: 0,
      mineru_md_path: `fixture/${id}.md`, image_count: 0, fetched_at: STAMP }],
    acquisition: { state: 'ready', phase: 'done', active: false, events: [], updated_at: STAMP },
  }
}

export const test = base.extend<Options & { api: ApiFixtures }>({
  admin: [true, { option: true }],
  language: ['en', { option: true }],
  theme: ['light', { option: true }],
  api: [async ({ context, baseURL, admin, language, theme }, use, testInfo) => {
    expect(baseURL, 'Never run these fixtures against another server').toBe('http://127.0.0.1:4177')
    const origin = baseURL!
    const violations: string[] = []
    const pageErrors: string[] = []
    const failedAssets: string[] = []
    const seenDialogs: string[] = []
    const assetCounts = new Map<string, number>()
    const record = {
      id: 'markdownuser001', collectionId: '_pb_users_auth_', collectionName: 'users',
      email: 'markdown-fixture@example.invalid', name: 'Markdown Fixture User',
      username: 'markdown-fixture', avatar: '', verified: true,
    }
    const encoded = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url')
    const syntheticToken = (id: string) => `${encoded({ alg: 'HS256', typ: 'JWT' })}.${encoded({ id, exp: Math.floor(Date.now() / 1000) + 3600 })}.synthetic-invalid-signature`
    const token = syntheticToken(record.id)
    const refreshedToken = `${token}-refreshed`
    let activeRecord = record
    let activeAdmin = admin
    let activeToken = refreshedToken
    const api: ApiFixtures = {
      documents: {
        [PAPER_ID]: { title: TITLE, markdown: MARKDOWN },
        [SECOND_PAPER_ID]: { title: SECOND_TITLE, markdown: '# Second document\n\nFresh $y=2$.\n' },
      },
      requests: [],
      deliveredAssets: [],
      async switchAccount(page, account) {
        // Simulate the PocketBase cross-tab session notification in this same
        // document; do not reload (which would discard the vulnerable cache).
        activeRecord = { ...record, id: account.id, username: account.id,
          name: `Fixture account ${account.id}`, email: `${account.id}@example.invalid` }
        activeAdmin = account.admin
        activeToken = `${syntheticToken(account.id)}-switched`
        await page.evaluate(({ token, record }) => {
          const key = 'pocketbase_auth'
          const oldValue = localStorage.getItem(key)
          const newValue = JSON.stringify({ token, record })
          localStorage.setItem(key, newValue)
          dispatchEvent(new StorageEvent('storage', {
            key, oldValue, newValue, storageArea: localStorage, url: location.href,
          }))
        }, { token: activeToken, record: activeRecord })
      },
    }
    await context.addInitScript(({ origin, token, record, language, theme }) => {
      if (location.origin !== origin) return
      localStorage.clear()
      sessionStorage.clear()
      localStorage.setItem('pocketbase_auth', JSON.stringify({ token, record }))
      localStorage.setItem('qatlas_theme', theme)
      localStorage.setItem('i18nextLng', language)
      document.addEventListener('securitypolicyviolation', (event) => {
        console.error(`BROWSER_REGRESSION_CSP: ${event.violatedDirective} ${event.blockedURI}`)
      })
    }, { origin, token, record, language, theme })

    context.on('page', (page) => {
      page.on('pageerror', (error) => pageErrors.push(error.message))
      page.on('console', (message) => {
        if (message.text().startsWith('BROWSER_REGRESSION_CSP:')) violations.push(message.text())
      })
      page.on('dialog', async (dialog) => {
        seenDialogs.push(`${dialog.type()}: ${dialog.message()}`)
        await dialog.dismiss()
      })
      page.on('requestfailed', (request) => {
        if (['script', 'stylesheet', 'font'].includes(request.resourceType())) {
          failedAssets.push(`${request.url()}: ${request.failure()?.errorText}`)
        }
      })
      page.on('response', (response) => {
        if (response.status() >= 400 && ['script', 'stylesheet', 'font'].includes(response.request().resourceType())) {
          failedAssets.push(`${response.status()} ${response.url()}`)
        }
      })
    })
    await context.routeWebSocket('**/*', (socket) => {
      violations.push(`Unexpected WebSocket: ${socket.url()}`)
      socket.close()
    })
    await context.route('**/*', async (route) => {
      const request = route.request()
      const url = new URL(request.url())
      const method = request.method()
      const entry = { method, path: url.pathname, type: request.resourceType(), userId: activeRecord.id }
      api.requests.push(entry)
      if (url.origin !== origin) {
        violations.push(`External request blocked: ${method} ${url.href}`)
        await route.abort('blockedbyclient')
        return
      }
      const json = async (body: unknown) => route.fulfill({ status: 200, json: body })
      if (url.pathname.startsWith('/api/')) {
        if (method === 'POST' && url.pathname === '/api/collections/users/auth-refresh') {
          expect(request.headers().authorization).toBe(activeRecord.id === record.id ? token : activeToken)
          await json({ token: activeToken, record: activeRecord })
          return
        }
        // Protect the fixture boundary too: the UI must use the current
        // synthetic session explicitly, not a real cookie/PAT/dev bypass or
        // the previous account's token after an in-place account change.
        expect(request.headers().authorization).toBe(`Bearer ${activeToken}`)
        if (method === 'GET' && url.pathname === '/api/admin/whoami') {
          const account = { id: activeRecord.id, admin: activeAdmin }
          const login = activeRecord.username
          await api.beforeWhoami?.(account)
          await json({ login, is_admin: account.admin, is_user_admin: account.admin, is_superadmin: account.admin })
          return
        }
        if (method === 'GET' && url.pathname === '/api/me') {
          await json({ ...activeRecord, github_login: '', gitea_login: '', github_bound: false,
            gitea_bound: false, is_admin: activeAdmin, is_superadmin: activeAdmin, created: STAMP })
          return
        }
        if (method === 'GET' && url.pathname === '/api/admin/assets/search' && activeAdmin) {
          expect(url.searchParams.get('q')).toBeTruthy()
          await json({ papers: Object.entries(api.documents).map(([id, doc]) => paper(id, doc)) })
          return
        }
        for (const [id, document] of Object.entries(api.documents)) {
          if (method === 'GET' && url.pathname === `/api/papers/${id}`) {
            await json(paper(id, document))
            return
          }
          if (method === 'GET' && url.pathname === `/api/admin/assets/${id}` && activeAdmin) {
            await json({ paper_id: id, title: document.title, assets: [{
              kind: 'markdown', object_key: `fixture/${id}.md`,
              size: Buffer.byteLength(document.markdown), content_type: 'text/markdown', presign_supported: false,
            }] })
            return
          }
          if (method === 'GET' && url.pathname === `/api/admin/assets/${id}/markdown/inline` && activeAdmin) {
            const count = (assetCounts.get(id) ?? 0) + 1
            assetCounts.set(id, count)
            const response = await api.assetResponse?.(id, count) ?? { body: document.markdown }
            await route.fulfill({ status: response.status ?? 200,
              contentType: 'text/markdown; charset=utf-8', body: response.body })
            api.deliveredAssets.push(id)
            return
          }
        }
        violations.push(`Unknown API blocked: ${method} ${url.pathname}${url.search}`)
        await route.abort('blockedbyclient')
        return
      }
      // Static build files and the two actual route families only. In particular,
      // a local Markdown image cannot silently hit a real app/API endpoint.
      const staticAsset = /^\/assets\/[^/]+\.(?:js|css|woff2?|ttf)$/.test(url.pathname)
      const appDocument = /^\/(?:en|zh)\/(?:papers\/markdown-paper-000[12]|admin\/assets)\/?$/.test(url.pathname)
      if (method === 'GET' && (staticAsset || appDocument || url.pathname === '/favicon.ico')) {
        await route.continue()
        return
      }
      violations.push(`Unknown local request blocked: ${method} ${url.pathname}`)
      await route.abort('blockedbyclient')
    })

    await use(api)

    await testInfo.attach('synthetic-network-log', {
      body: JSON.stringify({ requests: api.requests, violations, pageErrors, failedAssets, seenDialogs }, null, 2),
      contentType: 'application/json',
    })
    expect(violations, 'No external HTTP, unexpected local/API requests, WebSockets, or CSP violations').toEqual([])
    expect(pageErrors, 'No uncaught browser exceptions').toEqual([])
    expect(failedAssets, 'All JS/CSS/local font assets must load').toEqual([])
    expect(seenDialogs, 'Markdown must not execute browser dialogs').toEqual([])
    expect(api.requests.some(({ path }) => path === '/api/collections/users/auth-refresh'),
      'The production auth bootstrap must run against our fixture, not dev fake auth').toBe(true)
  }, { auto: true }],
})

export const labels = {
  en: { rendered: 'Rendered', source: 'Source', preview: 'Preview', paperPreview: 'Preview Markdown' },
  zh: { rendered: '渲染预览', source: '原文', preview: '预览', paperPreview: '预览 Markdown' },
} as const

export async function openPreview(page: Page, entry: EntryPoint, language: Language, id = PAPER_ID) {
  if (entry === 'paper') {
    await page.goto(`/${language}/papers/${id}`)
    await page.getByRole('button', { name: labels[language].paperPreview, exact: true }).click()
  } else {
    await page.goto(`/${language}/admin/assets`)
    await page.locator('main input').fill('fixture')
    await selectAssetPreview(page, language, id)
  }
  await expect(page.locator('html')).toHaveAttribute('lang', language)
  await expect(page.getByRole('dialog')).toBeVisible()
  return page.getByTestId('markdown-preview')
}

export async function selectAssetPreview(page: Page, language: Language, id: string) {
  const card = page.getByRole('button', { name: new RegExp(id) })
  if (await card.getAttribute('aria-expanded') !== 'true') await card.click()
  await page.getByRole('button', { name: labels[language].preview, exact: true }).click()
}

export async function expectExactSource(page: Page, language: Language, markdown: string) {
  const preview = page.getByTestId('markdown-preview')
  await preview.getByRole('tab', { name: labels[language].source, exact: true }).click()
  const source = preview.getByTestId('markdown-source')
  await expect(source).toBeVisible()
  expect(await source.evaluate((element) => element.tagName)).toBe('PRE')
  await expect.poll(() => source.textContent(), 'Source must preserve every character and trailing newline').toBe(markdown)
  await expect(source.locator('*')).toHaveCount(0)
}

export { expect }
