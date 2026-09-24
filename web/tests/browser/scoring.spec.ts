import { test as base, expect, type Locator, type Page } from '@playwright/test'

// Synthetic UI contracts only: no backend, real credentials, LLM, or third-party
// traffic. Keep these mocks separate from the existing Markdown fixtures.
const GENERATE = '/api/search/scoring/generate'
const RANKED = '/api/search/ranked'
const CAPABILITIES = '/api/search/scoring/capabilities'
const FETCH = '/api/downloader/fetch'
const SOURCES = ['arxiv', 'openalex', 'semantic_scholar']
const CONFIRM = 'I confirm these rules for this search'
const EXECUTE = 'Search with confirmed rules'
const EXPLAIN = 'Include score explanations (off by default)'
const INERT = '<img src="https://scoring-fixture.invalid/x" onerror="window.__scoringXss=1"><script>window.__scoringXss=1</script>'
const SCORER = { language: 'qatlas-expr-v1', filter: 'year >= 2020', score: 'citations - 10' }
const GENERATED = {
  scorer: SCORER, summary: `Prefer recent papers. ${INERT}`,
  warnings: ['Missing citation counts may change ranking.'],
  scorer_hash: 'synthetic-scorer-hash', feature_version: 'fixture-v1',
  usage: { today: 2, limit: 10, llm_tokens: 123 },
}
const RANKING = {
  hits: [
    { title: 'Title-only leader', source: 'arxiv', score: 12.5,
      score_detail: { raw: 12.5 }, score_explanation: { text: INERT } },
    { title: 'Registry middle', source: 'openalex', score: 2.25,
      paper_id: 'scoring-paper-001', created: false },
    { title: 'Negative tail', source: 'semantic_scholar', score: -4.75 },
  ],
  ranking: { language: 'qatlas-expr-v1', scorer_hash: 'synthetic-scorer-hash' },
  usage: { llm_tokens: 0 }, errors: {}, remote: true,
}

type Reply = { status?: number; json: unknown }
type RequestEntry = { method: string; path: string; body?: unknown }
type ReplyHandler = () => Reply | Promise<Reply>
type Mocks = {
  remote: boolean
  generation: boolean
  requests: RequestEntry[]
  handlers: Map<string, ReplyHandler>
  posts: (path: string) => RequestEntry[]
  hold: (path: string) => { release: (reply: Reply) => Promise<void> }
}

function deferred<T>() {
  let resolve!: (value: T) => void
  const promise = new Promise<T>((done) => { resolve = done })
  return { promise, resolve }
}

const test = base.extend<{ api: Mocks }>({
  api: [async ({ context, baseURL }, use, testInfo) => {
    expect(baseURL, 'Only the existing static preview may be used').toBe('http://127.0.0.1:4177')
    const origin = baseURL!
    const record = {
      id: 'scoringuser0001', collectionId: '_pb_users_auth_', collectionName: 'users',
      email: 'scoring-fixture@example.invalid', name: 'Scoring Fixture',
      username: 'scoring-fixture', avatar: '', verified: true,
    }
    const encoded = (value: unknown) => Buffer.from(JSON.stringify(value)).toString('base64url')
    const token = `${encoded({ alg: 'HS256', typ: 'JWT' })}.${encoded({ id: record.id, exp: Math.floor(Date.now() / 1000) + 3600 })}.synthetic-invalid-signature`
    const refreshedToken = `${token}-refreshed`
    const violations: string[] = []
    const pageErrors: string[] = []
    const completed = new Map<ReplyHandler, ReturnType<typeof deferred<void>>>()
    const held: ReturnType<typeof deferred<Reply>>[] = []
    const api: Mocks = {
      remote: true, generation: true, requests: [], handlers: new Map(),
      posts: (path) => api.requests.filter((request) => request.method === 'POST' && request.path === path),
      hold(path) {
        const response = deferred<Reply>()
        const delivered = deferred<void>()
        const handler = () => response.promise
        held.push(response)
        completed.set(handler, delivered)
        api.handlers.set(path, handler)
        return { async release(reply) { response.resolve(reply); await delivered.promise } }
      },
    }
    await context.addInitScript(({ origin, token, record }) => {
      if (location.origin !== origin) return
      localStorage.clear()
      sessionStorage.clear()
      localStorage.setItem('pocketbase_auth', JSON.stringify({ token, record }))
      localStorage.setItem('i18nextLng', 'en')
      localStorage.setItem('qatlas_theme', 'light')
      document.addEventListener('securitypolicyviolation', (event) => {
        console.error(`SCORING_CSP: ${event.violatedDirective} ${event.blockedURI}`)
      })
    }, { origin, token, record })
    context.on('page', (page) => {
      page.on('pageerror', (error) => pageErrors.push(error.message))
      page.on('console', (message) => {
        if (message.text().startsWith('SCORING_CSP:')) violations.push(message.text())
      })
      page.on('dialog', async (dialog) => {
        violations.push(`Unexpected dialog: ${dialog.message()}`)
        await dialog.dismiss()
      })
      page.on('requestfailed', (request) => {
        if (['script', 'stylesheet', 'font'].includes(request.resourceType())) {
          violations.push(`Static asset failed: ${request.url()}`)
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
      const path = url.pathname
      const method = request.method()
      if (url.origin !== origin) {
        violations.push(`External request blocked: ${method} ${url.href}`)
        await route.abort('blockedbyclient')
        return
      }
      if (path.startsWith('/api/')) {
        api.requests.push({ method, path, ...(request.postData() ? { body: request.postDataJSON() } : {}) })
        if (method === 'POST' && path === '/api/collections/users/auth-refresh') {
          expect(request.headers().authorization).toBe(token)
          await route.fulfill({ json: { token: refreshedToken, record } })
          return
        }
        expect(request.headers().authorization, 'Use the refreshed synthetic session').toBe(`Bearer ${refreshedToken}`)
        const replies: Record<string, unknown> = {
          '/api/admin/whoami': { login: record.username, is_admin: false, is_user_admin: false, is_superadmin: false },
          '/api/me': { ...record, is_admin: false, is_superadmin: false },
          '/api/v1/plugins': { plugins: [{ id: 'search-remote', enabled: true, status: api.remote ? 'connected' : 'disconnected' }, { id: 'downloader', enabled: true, status: 'connected' }] },
          [FETCH]: { items: [], enqueued: 0 },
          '/api/search/backends': { remote: true, keys_enabled: true, backends: SOURCES.map((name) => ({
            name, label: name, category: 'academic', selectable: true,
            requires_key: false, user_key: false, server_ready: true, key_configured: false,
          })) },
          [CAPABILITIES]: { language: 'qatlas-expr-v1', feature_version: 'fixture-v1', generation_available: api.generation },
          [GENERATE]: GENERATED,
          [RANKED]: RANKING,
          '/api/search/multi': { results: { arxiv: [{ title: 'Ordinary multi hit', source: 'arxiv', score: 0 }] }, errors: {}, usage: { llm_tokens: 0 }, remote: true },
          '/api/search/agentic': { results: [], candidates: [], conclusion: 'Synthetic Agentic conclusion', usage: { today: 1, limit: 10 }, remote: true },
          '/api/search': { results: [], candidates: [{ title: 'Classic fallback hit', source: 'catalog', score: 0.5 }] },
          '/api/papers/scoring-paper-001': { paper_id: 'scoring-paper-001', title: 'Registry middle', status: 'ready', assets: [], acquisition: { state: 'ready', phase: 'done', active: false, events: [] } },
        }
        const expectedMethod = [FETCH, GENERATE, RANKED, '/api/search/multi', '/api/search/agentic', '/api/search'].includes(path) ? 'POST' : 'GET'
        if (Object.prototype.hasOwnProperty.call(replies, path) && method === expectedMethod) {
          const handler = api.handlers.get(path)
          try {
            const response = handler ? await handler() : { json: replies[path] }
            await route.fulfill({ status: response.status ?? 200, json: response.json })
          } finally {
            if (handler) completed.get(handler)?.resolve()
          }
          return
        }
        violations.push(`Unknown API blocked: ${method} ${path}`)
        await route.abort('blockedbyclient')
        return
      }
      const staticAsset = /^\/assets\/[^/]+\.(?:js|css|woff2?|ttf)$/.test(path)
      if (method === 'GET' && (staticAsset || /^\/(?:en|zh)\/papers\/search$/.test(path) || path === '/favicon.ico')) {
        await route.continue()
        return
      }
      violations.push(`Unknown local request blocked: ${method} ${path}`)
      await route.abort('blockedbyclient')
    })
    try {
      await use(api)
    } finally {
      // Release intercepted requests even if an assertion failed mid-flight.
      for (const response of held) response.resolve({ status: 503, json: { detail: 'Fixture teardown' } })
      await testInfo.attach('scoring-synthetic-network', {
        body: JSON.stringify({ requests: api.requests, violations, pageErrors }, null, 2), contentType: 'application/json',
      })
      expect(violations, 'No external traffic, unexpected endpoints, dialogs, or CSP violations').toEqual([])
      expect(pageErrors, 'No uncaught browser exceptions').toEqual([])
      expect(api.posts('/api/collections/users/auth-refresh')).toHaveLength(1)
    }
  }, { auto: true }],
})

async function openCustom(page: Page) {
  await page.goto('/en/papers/search')
  await page.getByRole('button', { name: 'Custom scoring', exact: true }).click()
  const editor = page.getByTestId('scorer-editor')
  await expect(editor).toBeVisible()
  await expect(editor.getByText('Loading scoring capabilities…', { exact: true })).toHaveCount(0)
  return editor
}

async function manualRules(editor: Locator) {
  await editor.getByLabel('Search topic', { exact: true }).fill('quantum error correction')
  await editor.getByRole('textbox', { name: 'Filter expression', exact: true }).fill(SCORER.filter)
  await editor.getByRole('textbox', { name: 'Score expression', exact: true }).fill(SCORER.score)
}

async function confirmAndExecute(editor: Locator) {
  await editor.getByRole('checkbox', { name: CONFIRM, exact: true }).check()
  await editor.getByRole('button', { name: EXECUTE, exact: true }).click()
}

// Dispatch two same-turn activations so a duplicate cannot hide behind network
// speed or an auto-wait for the button to become enabled again.
async function rapidClicks(button: Locator) {
  await button.evaluate((element) => {
    if (!(element instanceof HTMLButtonElement)) throw new Error('Expected button')
    element.click()
    element.click()
  })
}

async function settleUI(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))))
}

test('generation requires review; explicit execution posts exact rules and renders one safe ordered list', async ({ page, api }) => {
  const editor = await openCustom(page)
  const execute = editor.getByRole('button', { name: EXECUTE, exact: true })
  await expect(execute).toBeDisabled()
  await editor.getByLabel('Search topic', { exact: true }).fill('  quantum error correction  ')
  await editor.getByRole('textbox', { name: 'Scoring requirements', exact: true }).fill('  Prefer recent papers and citations  ')
  expect(api.posts(GENERATE)).toHaveLength(0)
  expect(api.posts(RANKED)).toHaveLength(0)
  await editor.getByRole('button', { name: 'Generate scoring rules', exact: true }).click()
  const summary = editor.getByRole('region', { name: 'AI-generated explanation (not validation)', exact: true })
  await expect(summary).toContainText(GENERATED.summary)
  await expect(summary).toContainText(GENERATED.warnings[0])
  await expect(summary).toContainText('fixture-v1 · hash: synthetic-scorer-hash')
  await expect(editor.getByText('Today 2/10 · LLM tokens: 123', { exact: true })).toBeVisible()
  await expect(editor.getByRole('textbox', { name: 'Filter expression', exact: true })).toHaveValue(SCORER.filter)
  await expect(editor.getByRole('textbox', { name: 'Score expression', exact: true })).toHaveValue(SCORER.score)
  await expect(execute).toBeDisabled()
  await expect(editor.getByRole('checkbox', { name: CONFIRM, exact: true })).not.toBeChecked()
  await expect(editor.getByRole('checkbox', { name: EXPLAIN, exact: true })).not.toBeChecked()
  expect(api.posts(GENERATE)).toEqual([{ method: 'POST', path: GENERATE, body: { query: 'quantum error correction', requirements: 'Prefer recent papers and citations' } }])
  expect(api.posts(RANKED)).toHaveLength(0)
  await confirmAndExecute(editor)
  const results = page.getByRole('region', { name: 'Custom scoring results', exact: true })
  const list = results.getByTestId('scorer-hits')
  await expect(list).toHaveJSProperty('tagName', 'OL')
  const items = list.locator(':scope > li')
  await expect(items).toHaveCount(3)
  for (const [index, hit] of RANKING.hits.entries()) {
    await expect(items.nth(index)).toContainText(`#${index + 1}`)
    await expect(items.nth(index)).toContainText(hit.title)
    await expect(items.nth(index)).toContainText(`score: ${hit.score.toFixed(3)}`)
  }
  await expect(items.nth(1).getByRole('link', { name: 'Registry middle', exact: true })).toHaveAttribute('href', '/en/papers/scoring-paper-001')
  await expect(results.getByText('Possible matches', { exact: true })).toHaveCount(0)
  await items.first().getByText('Score details and explanation (JSON)', { exact: true }).click()
  const explanation = items.first().getByTestId('score-explanation')
  await expect(explanation).toHaveText(JSON.stringify({ score_detail: RANKING.hits[0].score_detail, score_explanation: RANKING.hits[0].score_explanation }, null, 2))
  await expect(explanation.locator('*')).toHaveCount(0)
  await expect(editor.locator('img, script, iframe, svg[data-xss]')).toHaveCount(0)
  expect(await page.evaluate(() => Reflect.get(window, '__scoringXss'))).toBeUndefined()
  expect(api.posts(RANKED)).toEqual([{ method: 'POST', path: RANKED, body: { text: 'quantum error correction', sources: SOURCES, scorer: SCORER, explain: false } }])
})

test('topic, requirements, filter, score, source and explanation edits each revoke confirmation', async ({ page, api }) => {
  const editor = await openCustom(page)
  await manualRules(editor)
  const confirm = editor.getByRole('checkbox', { name: CONFIRM, exact: true })
  const execute = editor.getByRole('button', { name: EXECUTE, exact: true })
  const changes = [
    () => editor.getByLabel('Search topic', { exact: true }).fill('edited topic'),
    () => editor.getByRole('textbox', { name: 'Scoring requirements', exact: true }).fill('edited requirements'),
    () => editor.getByRole('textbox', { name: 'Filter expression', exact: true }).fill('year >= 2022'),
    () => editor.getByRole('textbox', { name: 'Score expression', exact: true }).fill('citations - 20'),
    () => page.getByRole('checkbox', { name: 'openalex', exact: true }).uncheck(),
    () => editor.getByRole('checkbox', { name: EXPLAIN, exact: true }).check(),
  ]
  for (const change of changes) {
    await confirm.check()
    await expect(execute).toBeEnabled()
    await change()
    await expect(confirm).not.toBeChecked()
    await expect(execute).toBeDisabled()
    expect(api.posts(RANKED)).toHaveLength(0)
  }
  await confirmAndExecute(editor)
  await expect(page.getByTestId('scorer-hits')).toBeVisible()
  expect(api.posts(RANKED)[0].body).toEqual({
    text: 'edited topic', sources: ['arxiv', 'semantic_scholar'],
    scorer: { language: 'qatlas-expr-v1', filter: 'year >= 2022', score: 'citations - 20' }, explain: true,
  })
  await editor.getByRole('textbox', { name: 'Score expression', exact: true }).fill('0')
  await expect(page.getByTestId('scorer-hits')).toHaveCount(0)
  await expect(confirm).not.toBeChecked()
})

test('rapid duplicate generation and execution clicks send one POST while pending', async ({ page, api }) => {
  const editor = await openCustom(page)
  await manualRules(editor)
  await editor.getByRole('textbox', { name: 'Scoring requirements', exact: true }).fill('Rank by citations')
  const generation = api.hold(GENERATE)
  await rapidClicks(editor.getByRole('button', { name: 'Generate scoring rules', exact: true }))
  await expect.poll(() => api.posts(GENERATE).length).toBe(1)
  await expect(editor.getByRole('button', { name: 'Generating rules…', exact: true })).toBeDisabled()
  await generation.release({ json: GENERATED })
  await expect(editor.getByRole('region', { name: 'AI-generated explanation (not validation)' })).toBeVisible()
  expect(api.posts(GENERATE)).toHaveLength(1)
  await editor.getByRole('checkbox', { name: CONFIRM, exact: true }).check()
  const execution = api.hold(RANKED)
  await rapidClicks(editor.getByRole('button', { name: EXECUTE, exact: true }))
  await expect.poll(() => api.posts(RANKED).length).toBe(1)
  await expect(editor.getByRole('button', { name: 'Searching with rules…', exact: true })).toBeDisabled()
  await execution.release({ json: RANKING })
  await expect(page.getByTestId('scorer-hits')).toBeVisible()
  await page.evaluate(() => {
    window.dispatchEvent(new Event('focus'))
    document.dispatchEvent(new Event('visibilitychange'))
    window.dispatchEvent(new Event('online'))
  })
  await settleUI(page)
  expect(api.posts(GENERATE)).toHaveLength(1)
  expect(api.posts(RANKED)).toHaveLength(1)
})

test('late generation and ranked responses cannot overwrite edits or revive stale results', async ({ page, api }) => {
  const editor = await openCustom(page)
  await manualRules(editor)
  await editor.getByRole('textbox', { name: 'Scoring requirements', exact: true }).fill('Initial requirements')
  const generation = api.hold(GENERATE)
  await editor.getByRole('button', { name: 'Generate scoring rules', exact: true }).click()
  await expect.poll(() => api.posts(GENERATE).length).toBe(1)
  await editor.getByRole('textbox', { name: 'Scoring requirements', exact: true }).fill('New requirements')
  await editor.getByRole('textbox', { name: 'Score expression', exact: true }).fill('42')
  await generation.release({ json: { ...GENERATED, summary: 'STALE generation', scorer: { ...SCORER, score: '-999' } } })
  await settleUI(page)
  await expect(editor.getByRole('textbox', { name: 'Score expression', exact: true })).toHaveValue('42')
  await expect(editor.getByText('STALE generation', { exact: true })).toHaveCount(0)
  await expect(editor.getByRole('checkbox', { name: CONFIRM, exact: true })).not.toBeChecked()
  const cancelledGeneration = api.hold(GENERATE)
  await editor.getByRole('button', { name: 'Generate scoring rules', exact: true }).click()
  await expect.poll(() => api.posts(GENERATE).length).toBe(2)
  await editor.getByRole('button', { name: 'Cancel request', exact: true }).click()
  await cancelledGeneration.release({ json: { ...GENERATED, summary: 'CANCELLED generation' } })
  await settleUI(page)
  await expect(editor.getByText('CANCELLED generation', { exact: true })).toHaveCount(0)
  await expect(editor.getByRole('textbox', { name: 'Score expression', exact: true })).toHaveValue('42')
  const execution = api.hold(RANKED)
  await confirmAndExecute(editor)
  await expect.poll(() => api.posts(RANKED).length).toBe(1)
  await editor.getByLabel('Search topic', { exact: true }).fill('New topic while executing')
  await execution.release({ json: { ...RANKING, hits: [{ title: 'STALE ranked hit', source: 'arxiv', score: 999 }] } })
  await settleUI(page)
  await expect(page.getByTestId('scorer-hits')).toHaveCount(0)
  await expect(editor.getByRole('button', { name: EXECUTE, exact: true })).toBeDisabled()
  api.handlers.delete(RANKED)
  await confirmAndExecute(editor)
  await expect(page.getByTestId('scorer-hits')).toBeVisible()
  await expect(page.getByText('STALE ranked hit', { exact: true })).toHaveCount(0)
  expect(api.posts(RANKED)).toHaveLength(2)
  expect(api.posts(RANKED)[1].body).toMatchObject({ text: 'New topic while executing', scorer: { score: '42' } })
})

test('structured 422 validation, 429 quota and 503 generation failures remain actionable', async ({ page, api }) => {
  const editor = await openCustom(page)
  await manualRules(editor)
  api.handlers.set(RANKED, () => ({ status: 422, json: { detail: { code: 'invalid_expression', message: 'Unknown score feature', field: 'scorer.score', position: 0 } } }))
  await confirmAndExecute(editor)
  const alert = editor.getByRole('alert')
  await expect(alert).toContainText('422: Unknown score feature')
  await expect(alert).toContainText('invalid_expression · Field: scorer.score · Position: 0')
  expect(api.posts(RANKED)).toHaveLength(1)
  await editor.getByRole('textbox', { name: 'Scoring requirements', exact: true }).fill('Prefer recent papers')
  await expect(alert).toHaveCount(0)
  api.handlers.set(GENERATE, () => ({ status: 429, json: { detail: { code: 'quota_exhausted', message: 'No generation quota remains' }, usage: { today: 10, limit: 10 } } }))
  await editor.getByRole('button', { name: 'Generate scoring rules', exact: true }).click()
  await expect(alert).toContainText('Daily limit reached')
  await expect(alert).toContainText('429: No generation quota remains')
  await expect(alert).toContainText("You have reached today's agentic-search quota (10/10)")
  api.handlers.set(GENERATE, () => ({ status: 503, json: { detail: { code: 'generation_unavailable', message: 'Generation provider unavailable' } } }))
  await editor.getByRole('button', { name: 'Generate scoring rules', exact: true }).click()
  await expect(alert).toContainText('Scoring request failed')
  await expect(alert).toContainText('503: Generation provider unavailable')
  await expect(alert).toContainText('generation_unavailable')
  await expect(editor.getByRole('textbox', { name: 'Score expression', exact: true })).toHaveValue(SCORER.score)
  expect(api.posts(GENERATE)).toHaveLength(2)
  api.handlers.delete(RANKED)
  await confirmAndExecute(editor)
  await expect(page.getByTestId('scorer-hits')).toBeVisible()
  await expect(alert).toHaveCount(0)
})

test('real structured DSL positions render as JSON instead of object interpolation', async ({ page, api }) => {
  const editor = await openCustom(page)
  await manualRules(editor)
  const position = { line: 1, column: 0, end_line: 1, end_column: 7 }
  api.handlers.set(RANKED, () => ({ status: 422, json: { detail: {
    code: 'invalid_expression', message: 'Unknown score feature', field: 'scorer.score', position,
  } } }))
  await confirmAndExecute(editor)
  const alert = editor.getByRole('alert')
  await expect(alert).toContainText(`Position: ${JSON.stringify(position)}`)
  await expect(alert).not.toContainText('[object Object]')
  await expect(alert).toContainText('Field: scorer.score')
  await expect(alert).toContainText('Scoring request failed')
  await expect(alert.locator('script, img, iframe')).toHaveCount(0)
  expect(api.posts(RANKED)).toHaveLength(1)
})

test('429 busy and provider rate limits are not mislabeled as exhausted daily quota', async ({ page, api }) => {
  const editor = await openCustom(page)
  await manualRules(editor)
  const alert = editor.getByRole('alert')
  api.handlers.set(RANKED, () => ({ status: 429, json: { detail: {
    code: 'busy', message: 'Too many scoring requests in flight; retry later',
  } } }))
  await confirmAndExecute(editor)
  await expect(alert).toContainText('Scoring request failed')
  await expect(alert).toContainText('429: Too many scoring requests in flight')
  await expect(alert).not.toContainText('Daily limit reached')
  await expect(alert).not.toContainText("You have reached today's agentic-search quota")

  await editor.getByRole('textbox', { name: 'Scoring requirements', exact: true }).fill('Prefer recent papers')
  // Failed generation can include informational daily usage without being a
  // daily quota failure: busy/rate_limited must take precedence over that block.
  api.handlers.set(GENERATE, () => ({ status: 429, json: {
    detail: { code: 'rate_limited', message: 'Generation provider is temporarily rate limited' },
    usage: { today: 1, limit: 10, llm_tokens: 0 },
  } }))
  await editor.getByRole('button', { name: 'Generate scoring rules', exact: true }).click()
  await expect(alert).toContainText('Scoring request failed')
  await expect(alert).toContainText('429: Generation provider is temporarily rate limited')
  await expect(alert).not.toContainText('Daily limit reached')
  await expect(alert).not.toContainText("You have reached today's agentic-search quota")

  // The explicit quota code identifies exhaustion even when usage is absent.
  api.handlers.set(GENERATE, () => ({ status: 429, json: {
    detail: { code: 'quota_exceeded', message: 'Daily generation quota exhausted' },
  } }))
  await editor.getByRole('button', { name: 'Generate scoring rules', exact: true }).click()
  await expect(alert).toContainText('Daily limit reached')
  await expect(alert).toContainText('429: Daily generation quota exhausted')
  expect(api.posts(GENERATE)).toHaveLength(2)
  expect(api.posts(RANKED)).toHaveLength(1)
})

test('Chinese narrow-screen manual DSL works when generation_available is false', async ({ page, api }) => {
  api.generation = false
  await page.setViewportSize({ width: 390, height: 844 })
  await page.goto('/zh/papers/search')
  await page.getByRole('button', { name: '自定义评分', exact: true }).click()
  const editor = page.getByTestId('scorer-editor')
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh')
  await expect(editor.getByText('AI 规则生成未配置或不可用。仍可手动输入 DSL 规则；普通搜索不受影响。', { exact: true })).toBeVisible()
  await editor.getByLabel('搜索主题', { exact: true }).fill('量子纠错')
  await editor.getByRole('textbox', { name: '评分需求', exact: true }).fill('手动规则')
  await editor.getByRole('textbox', { name: '评分表达式', exact: true }).fill(SCORER.score)
  await expect(editor.getByRole('textbox', { name: '筛选表达式', exact: true })).toHaveValue('true')
  await editor.getByRole('textbox', { name: '筛选表达式', exact: true }).fill('')
  await expect(editor.getByRole('checkbox', { name: '我确认本次搜索使用这些规则', exact: true })).toBeDisabled()
  await editor.getByRole('textbox', { name: '筛选表达式', exact: true }).fill('true')
  await expect(editor.getByRole('button', { name: '生成评分规则', exact: true })).toBeDisabled()
  await expect(editor.getByRole('button', { name: '按已确认规则搜索', exact: true })).toBeDisabled()
  await editor.getByRole('checkbox', { name: '我确认本次搜索使用这些规则', exact: true }).check()
  await editor.getByRole('button', { name: '按已确认规则搜索', exact: true }).click()
  await expect(page.getByRole('region', { name: '自定义评分结果', exact: true })).toBeVisible()
  expect(api.posts(GENERATE)).toHaveLength(0)
  expect(api.posts(RANKED)).toEqual([{ method: 'POST', path: RANKED, body: { text: '量子纠错', sources: SOURCES, scorer: { ...SCORER, filter: 'true' }, explain: false } }])
})

test('offline remote disables custom and Agentic without breaking classic fallback', async ({ page, api }) => {
  api.remote = false
  await page.goto('/en/papers/search')
  await expect.poll(() => api.requests.some((entry) => entry.path === '/api/v1/plugins')).toBe(true)
  await expect(page.getByRole('button', { name: 'Custom scoring', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Agentic search', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: 'Classic search', exact: true })).toHaveAttribute('aria-pressed', 'true')
  await page.getByRole('main').locator('input[name="q"]').fill('fallback topic')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Classic fallback hit', exact: true })).toBeVisible()
  expect(api.posts('/api/search')).toEqual([{ method: 'POST', path: '/api/search', body: { text: 'fallback topic' } }])
  expect(api.requests.filter((entry) => [CAPABILITIES, GENERATE, RANKED, '/api/search/multi', '/api/search/agentic'].includes(entry.path))).toEqual([])
})

async function openAgentic(page: Page) {
  await page.goto('/en/papers/search')
  await page.getByRole('button', { name: 'Agentic search', exact: true }).click()
  await page.getByRole('main').locator('input[name="q"]').fill('agentic topic')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
}

const AGENTIC = '/api/search/agentic'
const EMPTY_AGENTIC = { results: [], candidates: [], conclusion: null, usage: { today: 1, limit: 10 } }

test('Agentic shows progress while the first request is pending', async ({ page, api }) => {
  const pending = api.hold(AGENTIC)
  await openAgentic(page)
  await expect.poll(() => api.posts(AGENTIC).length).toBe(1)
  await expect(page.getByRole('status')).toContainText('Loading…')
  await pending.release({ json: { ...EMPTY_AGENTIC, conclusion: 'Completed Agentic search' } })
  await expect(page.getByText('Completed Agentic search', { exact: true })).toBeVisible()
  await expect(page.getByRole('status')).toHaveCount(0)
})

for (const status of [401, 500, 502, 503, 504]) {
  test(`Agentic displays HTTP ${status} errors instead of a blank result area`, async ({ page, api }) => {
    api.handlers.set(AGENTIC, () => ({ status, json: { detail: `Search failed (${status})` } }))
    await openAgentic(page)
    await expect(page.getByRole('alert')).toContainText(`Search failed (${status})`)
    expect(api.posts(AGENTIC)).toHaveLength(1)
  })
}

test('Agentic preserves the daily quota message', async ({ page, api }) => {
  api.handlers.set(AGENTIC, () => ({ status: 429, json: { detail: 'Quota exhausted', usage: { today: 10, limit: 10 } } }))
  await openAgentic(page)
  await expect(page.getByRole('alert')).toContainText("You have reached today's agentic-search quota (10/10)")
  expect(api.posts(AGENTIC)).toHaveLength(1)
})

test('Agentic shows an empty-result message and preserves usage and conclusion', async ({ page, api }) => {
  api.handlers.set(AGENTIC, () => ({ json: { ...EMPTY_AGENTIC, conclusion: 'No relevant papers found' } }))
  await openAgentic(page)
  await expect(page.getByText('No matching papers. Try a different query, a DOI, or an arXiv id.', { exact: true })).toBeVisible()
  await expect(page.getByText('No relevant papers found', { exact: true })).toBeVisible()
  await expect(page.getByText('Today 1/10', { exact: true })).toBeVisible()
})

test('Agentic surfaces partial backend failures without hiding successful candidates', async ({ page, api }) => {
  api.handlers.set(AGENTIC, () => ({ json: {
    ...EMPTY_AGENTIC, candidates: [{ title: 'Surviving candidate', source: 'arxiv', score: 0.5 }],
    errors: { openalex: 'Backend timed out' },
  } }))
  await openAgentic(page)
  await expect(page.getByRole('alert')).toContainText('Backend timed out')
  await expect(page.getByRole('alert')).toContainText('openalex')
  await expect(page.getByRole('heading', { name: 'Surviving candidate', exact: true })).toBeVisible()
  await expect(page.getByText('No matching papers. Try a different query, a DOI, or an arXiv id.', { exact: true })).toHaveCount(0)
})

test('Agentic waits for the backend selection instead of issuing an unpinned extra request', async ({ page, api }) => {
  const catalog = api.hold('/api/search/backends')
  await openAgentic(page)
  await expect(page.getByRole('main')).toContainText('Loading')
  expect(api.posts(AGENTIC)).toHaveLength(0)
  await catalog.release({ json: { remote: true, keys_enabled: true, backends: SOURCES.map((name) => ({
    name, label: name, category: 'academic', selectable: true,
    requires_key: false, user_key: false, server_ready: true, key_configured: false,
  })) } })
  await expect(page.getByText('Synthetic Agentic conclusion', { exact: true })).toBeVisible()
  expect(api.posts(AGENTIC)).toEqual([{ method: 'POST', path: AGENTIC, body: { text: 'agentic topic', sources: SOURCES } }])
})

test('Agentic does not fan out to all backends when none are selectable', async ({ page, api }) => {
  api.handlers.set('/api/search/backends', () => ({ json: { remote: true, keys_enabled: true, backends: [] } }))
  await openAgentic(page)
  await expect(page.getByText('Select at least one search backend before searching.', { exact: true })).toBeVisible()
  expect(api.posts(AGENTIC)).toHaveLength(0)
})

const DOI_SEARCH_INPUTS = [
  '10.1109/TAC.2010.2050710',
  'DOI: 10.1109/TAC.2010.2050710',
  'https://doi.org/10.1109%2FTAC.2010.2050710?utm_source=test#section',
]

for (const mode of ['classic', 'multi', 'agentic'] as const) {
  for (const input of DOI_SEARCH_INPUTS) {
    test(`${mode}: DOI input ${input} uses identity fields without starting downloads`, async ({ page, api }) => {
      api.remote = mode !== 'classic'
      await page.goto('/en/papers/search')
      if (mode === 'classic') {
        await expect(page.getByRole('button', { name: 'Agentic search', exact: true })).toBeDisabled()
      } else {
        await expect(page.getByRole('checkbox', { name: 'openalex', exact: true })).toBeVisible()
      }
      if (mode === 'agentic') await page.getByRole('button', { name: 'Agentic search', exact: true }).click()
      await page.getByRole('main').locator('input[name="q"]').fill(input)
      await page.getByRole('button', { name: 'Search', exact: true }).click()
      const endpoint = mode === 'classic' ? '/api/search' : mode === 'multi' ? '/api/search/multi' : AGENTIC
      await expect.poll(() => api.posts(endpoint).length).toBe(1)
      expect(api.posts(endpoint)[0].body).toEqual({
        doi: '10.1109/tac.2010.2050710', ...(mode === 'classic' ? {} : { sources: SOURCES }),
      })
      await expect(page.getByTestId('doi-search-hint')).toContainText('Exact DOI lookup: 10.1109/tac.2010.2050710')
      if (mode !== 'classic') await expect(page.getByTestId('doi-search-hint')).toContainText('OpenAlex or Semantic Scholar')
      for (const other of ['/api/search', '/api/search/multi', AGENTIC].filter((path) => path !== endpoint)) {
        expect(api.posts(other)).toHaveLength(0)
      }
      expect(api.posts(FETCH)).toHaveLength(0)
      expect(api.posts(GENERATE)).toHaveLength(0)
      expect(api.posts(RANKED)).toHaveLength(0)
    })
  }
}

test('switching from DOI to a topic mentioning it restores text search and clears the hint', async ({ page, api }) => {
  await page.goto('/en/papers/search')
  await expect(page.getByRole('checkbox', { name: 'openalex', exact: true })).toBeVisible()
  const input = page.getByRole('main').locator('input[name="q"]')
  await input.fill('10.1109/tac.2010.2050710')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByTestId('doi-search-hint')).toBeVisible()
  await expect.poll(() => api.posts('/api/search/multi').length).toBe(1)
  const topic = 'papers related to 10.1109/tac.2010.2050710'
  await input.fill(topic)
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect.poll(() => api.posts('/api/search/multi').length).toBe(2)
  expect(api.posts('/api/search/multi')[1].body).toEqual({ text: topic, sources: SOURCES })
  await expect(page.getByTestId('doi-search-hint')).toHaveCount(0)
  expect(api.posts(FETCH)).toHaveLength(0)
})

test('Chinese DOI hint is visible and custom scoring does not silently issue identity searches', async ({ page, api }) => {
  await page.goto('/zh/papers/search')
  await expect(page.getByRole('checkbox', { name: 'openalex', exact: true })).toBeVisible()
  await page.getByRole('main').locator('input[name="q"]').fill('https://doi.org/10.1234/Example')
  await page.getByRole('button', { name: '搜索', exact: true }).click()
  await expect(page.getByTestId('doi-search-hint')).toContainText('DOI 精确检索：10.1234/example')
  await expect.poll(() => api.posts('/api/search/multi').length).toBe(1)
  await page.getByRole('button', { name: '自定义评分', exact: true }).click()
  await expect(page.getByTestId('doi-search-hint')).toHaveCount(0)
  await expect(page.getByTestId('scorer-editor')).toBeVisible()
  expect(api.posts('/api/search/multi')).toHaveLength(1)
  expect(api.posts(AGENTIC)).toHaveLength(0)
  expect(api.posts(GENERATE)).toHaveLength(0)
  expect(api.posts(RANKED)).toHaveLength(0)
  expect(api.posts(FETCH)).toHaveLength(0)
})

test('ordinary multi and Agentic retain their endpoints and custom mode does not auto-execute', async ({ page, api }) => {
  await page.goto('/en/papers/search')
  await expect(page.getByRole('checkbox', { name: 'arxiv', exact: true })).toBeVisible()
  await page.getByRole('main').locator('input[name="q"]').fill('ordinary topic')
  await page.getByRole('button', { name: 'Search', exact: true }).click()
  await expect(page.getByRole('heading', { name: 'Ordinary multi hit', exact: true })).toBeVisible()
  expect(api.posts('/api/search/multi')).toEqual([{ method: 'POST', path: '/api/search/multi', body: { text: 'ordinary topic', sources: SOURCES } }])
  await page.getByRole('button', { name: 'Agentic search', exact: true }).click()
  await expect(page.getByText('Synthetic Agentic conclusion', { exact: true })).toBeVisible()
  expect(api.posts('/api/search/agentic')).toEqual([{ method: 'POST', path: '/api/search/agentic', body: { text: 'ordinary topic', sources: SOURCES } }])
  await page.getByRole('button', { name: 'Custom scoring', exact: true }).click()
  const editor = page.getByTestId('scorer-editor')
  await expect(editor.getByLabel('Search topic', { exact: true })).toHaveValue('ordinary topic')
  await editor.getByRole('textbox', { name: 'Score expression', exact: true }).fill('1')
  await editor.getByRole('checkbox', { name: CONFIRM, exact: true }).check()
  await page.getByRole('button', { name: 'Classic search', exact: true }).click()
  await expect(editor).toHaveCount(0)
  await expect(page.getByRole('heading', { name: 'Ordinary multi hit', exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Custom scoring', exact: true }).click()
  await expect(editor.getByRole('checkbox', { name: CONFIRM, exact: true })).not.toBeChecked()
  await expect(editor.getByRole('button', { name: EXECUTE, exact: true })).toBeDisabled()
  expect(api.posts(GENERATE)).toHaveLength(0)
  expect(api.posts(RANKED)).toHaveLength(0)
  expect(api.posts('/api/search')).toHaveLength(0)
})

const DOWNLOAD_HITS = [
  { title: 'Download Alpha', arxiv_id: '2401.12345', source: 'arxiv', score: 1 },
  { title: 'Download Beta', doi: '10.1234/beta', source: 'openalex', score: 0.9 },
  { title: 'Title only', source: 'fixture', score: 0 },
  { title: 'Generic web link', url: 'https://example.invalid/paper', source: 'web', score: 0 },
]

function downloadFixtures(api: Mocks) {
  api.handlers.set('/api/search/multi', () => ({ json: {
    results: { arxiv: DOWNLOAD_HITS, openalex: [{ ...DOWNLOAD_HITS[0], title: 'Duplicate Alpha', arxiv_id: undefined, url: 'https://arxiv.org/abs/2401.12345v2' }] },
    errors: {}, usage: { llm_tokens: 0 }, remote: true,
  } }))
  for (const path of ['/api/search', AGENTIC]) api.handlers.set(path, () => ({ json: {
    ...EMPTY_AGENTIC, results: [{ paper_id: 'scoring-paper-001', created: false, hit: DOWNLOAD_HITS[0] }], candidates: DOWNLOAD_HITS.slice(1),
  } }))
  api.handlers.set(RANKED, () => ({ json: { ...RANKING, hits: DOWNLOAD_HITS } }))
  api.handlers.set(FETCH, () => {
    const body = api.posts(FETCH).at(-1)?.body as { items: string[] }
    return { json: { items: body.items.map((input) => ({ input, kind: 'arxiv', created: false })), enqueued: body.items.length } }
  })
}

for (const mode of ['classic', 'multi', 'agentic', 'custom'] as const) {
  test(`${mode}: render/search never downloads; explicit selection submits identifiers only`, async ({ page, api }) => {
    downloadFixtures(api)
    api.remote = mode !== 'classic'
    if (mode === 'custom') {
      const editor = await openCustom(page)
      await manualRules(editor)
      await confirmAndExecute(editor)
    } else {
      await page.goto('/en/papers/search')
      if (mode === 'agentic') await page.getByRole('button', { name: 'Agentic search', exact: true }).click()
      await page.getByRole('main').locator('input[name="q"]').fill('download fixture')
      await page.getByRole('button', { name: 'Search', exact: true }).click()
    }
    const alpha = page.getByRole('checkbox', { name: 'Select for download: Download Alpha', exact: true })
    const beta = page.getByRole('checkbox', { name: 'Select for download: Download Beta', exact: true })
    await expect(alpha).not.toBeChecked()
    await expect(beta).not.toBeChecked()
    await expect(page.getByRole('checkbox', { name: 'Select for download: Title only', exact: true })).toBeDisabled()
    await expect(page.getByRole('checkbox', { name: 'Select for download: Generic web link', exact: true })).toBeDisabled()
    const submit = page.getByRole('button', { name: 'Download selected', exact: true })
    await expect(submit).toBeDisabled()
    expect(api.posts(FETCH)).toHaveLength(0)
    await alpha.check()
    if (mode === 'multi') {
      await page.getByRole('tab', { name: 'openalex' }).click()
      await expect(page.getByRole('checkbox', { name: 'Select for download: Duplicate Alpha', exact: true })).toBeChecked()
      await expect(page.getByTestId('download-selection-count')).toHaveText('1 selected')
    }
    expect(api.posts(FETCH)).toHaveLength(0)
    await submit.click()
    await expect(page.getByRole('status')).toContainText('1 accepted by downloader')
    expect(api.posts(FETCH)).toEqual([{ method: 'POST', path: FETCH, body: { items: ['2401.12345'] } }])
    await expect(submit).toBeDisabled()
  })
}

test('download selection resets on query, source, mode and custom result edits', async ({ page, api }) => {
  downloadFixtures(api)
  await page.goto('/en/papers/search')
  const search = async (query: string) => {
    await page.getByRole('main').locator('input[name="q"]').fill(query)
    await page.getByRole('button', { name: 'Search', exact: true }).click()
  }
  const alpha = page.getByRole('checkbox', { name: 'Select for download: Download Alpha', exact: true })
  await search('first')
  await alpha.check()
  await search('second')
  await expect(alpha).not.toBeChecked()
  await alpha.check()
  await page.getByRole('checkbox', { name: 'openalex', exact: true }).uncheck()
  await expect(alpha).not.toBeChecked()
  await alpha.check()
  await page.getByRole('button', { name: 'Agentic search', exact: true }).click()
  await expect(alpha).not.toBeChecked()
  await alpha.check()
  await page.getByRole('button', { name: 'Classic search', exact: true }).click()
  await expect(alpha).not.toBeChecked()
  await page.getByRole('button', { name: 'Custom scoring', exact: true }).click()
  const editor = page.getByTestId('scorer-editor')
  await manualRules(editor)
  await confirmAndExecute(editor)
  await alpha.check()
  await editor.getByRole('textbox', { name: 'Score expression', exact: true }).fill('42')
  await expect(alpha).toHaveCount(0)
  await confirmAndExecute(editor)
  await expect(alpha).not.toBeChecked()
  expect(api.posts(FETCH)).toHaveLength(0)
})

test('download pending guard and partial failures retry only failed selected identifiers', async ({ page, api }) => {
  downloadFixtures(api)
  await openAgentic(page)
  const alpha = page.getByRole('checkbox', { name: 'Select for download: Download Alpha', exact: true })
  const beta = page.getByRole('checkbox', { name: 'Select for download: Download Beta', exact: true })
  await alpha.check()
  await beta.check()
  const pending = api.hold(FETCH)
  await rapidClicks(page.getByRole('button', { name: 'Download selected', exact: true }))
  await expect.poll(() => api.posts(FETCH).length).toBe(1)
  await expect(page.getByRole('button', { name: 'Submitting downloads…', exact: true })).toBeDisabled()
  await expect(alpha).toBeDisabled()
  await pending.release({ json: { items: [
    { input: '2401.12345', kind: 'arxiv', created: false },
    { input: '10.1234/beta', kind: 'doi', created: false, error: 'Temporary fixture failure' },
  ], enqueued: 1 } })
  await expect(page.getByRole('alert')).toContainText('Download Beta: Temporary fixture failure')
  await expect(alpha).not.toBeChecked()
  await expect(alpha).toBeDisabled()
  await expect(beta).toBeChecked()
  api.handlers.set(FETCH, () => ({ status: 403, json: { detail: 'papers:write required' } }))
  await page.getByRole('button', { name: 'Retry failed (1)', exact: true }).click()
  await expect(page.getByRole('alert')).toContainText('papers:write required')
  expect(api.posts(FETCH).at(-1)?.body).toEqual({ items: ['10.1234/beta'] })
  downloadFixtures(api)
  await page.getByRole('button', { name: 'Retry failed (1)', exact: true }).click()
  await expect(page.getByRole('alert')).toHaveCount(0)
  await expect(beta).not.toBeChecked()
  expect(api.posts(FETCH).map((request) => request.body)).toEqual([
    { items: ['2401.12345', '10.1234/beta'] }, { items: ['10.1234/beta'] }, { items: ['10.1234/beta'] },
  ])
})

test('download batches enforce the existing 50-identifier limit', async ({ page, api }) => {
  downloadFixtures(api)
  api.handlers.set(AGENTIC, () => ({ json: { ...EMPTY_AGENTIC, candidates: Array.from({ length: 51 }, (_, index) => ({
    title: `Batch ${index}`, arxiv_id: `2401.${String(index).padStart(5, '0')}`, source: 'arxiv', score: 1,
  })) } }))
  await openAgentic(page)
  const boxes = page.getByRole('checkbox', { name: /^Select for download: Batch/ })
  await expect(boxes).toHaveCount(51)
  for (const box of await boxes.all()) await box.check()
  await expect(page.getByRole('button', { name: 'Download selected', exact: true })).toBeDisabled()
  await expect(page.getByText('Select at most 50 unique papers per submission.', { exact: true })).toBeVisible()
  expect(api.posts(FETCH)).toHaveLength(0)
  await boxes.last().uncheck()
  await page.getByRole('button', { name: 'Download selected', exact: true }).click()
  await expect(page.getByRole('status')).toContainText('50 accepted by downloader')
  expect((api.posts(FETCH)[0].body as { items: string[] }).items).toHaveLength(50)
})

test('Chinese download controls remain disabled when downloader is unavailable', async ({ page, api }) => {
  downloadFixtures(api)
  api.handlers.set('/api/v1/plugins', () => ({ json: { plugins: [{ id: 'search-remote', enabled: true, status: 'connected' }] } }))
  await page.goto('/zh/papers/search')
  await page.getByRole('main').locator('input[name="q"]').fill('量子')
  await page.getByRole('button', { name: '搜索', exact: true }).click()
  await expect(page.getByRole('checkbox', { name: '选择下载：Download Alpha', exact: true })).toBeDisabled()
  await expect(page.getByRole('button', { name: '下载所选论文', exact: true })).toBeDisabled()
  await expect(page.getByTestId('download-selection-count')).toHaveText('已选择 0 篇')
  expect(api.posts(FETCH)).toHaveLength(0)
})
