#!/usr/bin/env node
// Local-only UI acceptance for the normal Sphinx/Furo user and admin sites.
// Usage: node .github/scripts/docs-browser.cjs web/public build/docs-browser
// Reuses web's locked Playwright; never installs packages, edits the DOM, or
// follows deployment/API links. The temporary HTTP server binds loopback only.
const assert = require('node:assert/strict');
const crypto = require('node:crypto');
const fs = require('node:fs/promises');
const http = require('node:http');
const path = require('node:path');

const MIME = {
  '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8',
  '.json': 'application/json; charset=utf-8', '.svg': 'image/svg+xml',
  '.png': 'image/png', '.jpg': 'image/jpeg', '.jpeg': 'image/jpeg',
  '.gif': 'image/gif', '.ico': 'image/x-icon', '.woff': 'font/woff',
  '.woff2': 'font/woff2', '.ttf': 'font/ttf', '.txt': 'text/plain; charset=utf-8',
};
const sha256 = bytes => crypto.createHash('sha256').update(bytes).digest('hex');
const isWithin = (root, file) => file === root || file.startsWith(root + path.sep);

async function main() {
  assert.ok(process.argv.length <= 4, 'Usage: node docs-browser.cjs [web/public] [build/docs-browser]');
  const siteRoot = path.resolve(process.argv[2] || 'web/public');
  const output = path.resolve(process.argv[3] || 'build/docs-browser');
  const screenshots = path.join(output, 'screenshots');
  await fs.mkdir(screenshots, { recursive: true });
  const report = {
    ok: false, siteRoot, output, startedAt: new Date().toISOString(),
    checks: [], searches: [], downloads: [], screenshots: [],
    failedRequests: [], pageErrors: [], consoleErrors: [], externalResources: [],
    serverFailures: [], cleanupErrors: [],
    externalPolicy: 'Real CDN/font requests are allowed, recorded, and fail the smoke if unsuccessful; no mocking or failure suppression.',
  };
  let server;
  let browser;
  let context;
  let page;
  let base;
  let stage = 'startup';
  const external = new Map();
  const pendingResponses = new Set();

  try {
    const realRoot = await fs.realpath(siteRoot);
    server = http.createServer(async (request, response) => {
      let status = 200;
      try {
        if (!['GET', 'HEAD'].includes(request.method)) {
          status = 405;
          throw new Error('Only read-only static requests are supported');
        }
        const pathname = decodeURIComponent(new URL(request.url, 'http://localhost').pathname);
        let file = path.resolve(realRoot, '.' + pathname);
        assert.ok(isWithin(realRoot, file), 'Path escapes site root');
        if ((await fs.stat(file)).isDirectory()) file = path.join(file, 'index.html');
        file = await fs.realpath(file);
        assert.ok(isWithin(realRoot, file), 'Symlink escapes site root');
        const body = await fs.readFile(file);
        response.writeHead(200, {
          'Content-Type': MIME[path.extname(file).toLowerCase()] || 'application/octet-stream',
          'Content-Length': body.length,
          'Cache-Control': 'no-store',
          'X-Content-Type-Options': 'nosniff',
        });
        response.end(request.method === 'HEAD' ? undefined : body);
      } catch (error) {
        if (status === 200) status = error.code === 'ENOENT' || error.code === 'ENOTDIR' ? 404 : 400;
        report.serverFailures.push({ method: request.method, url: request.url, status, error: String(error) });
        response.writeHead(status, { 'Content-Type': 'text/plain; charset=utf-8' });
        response.end('Static documentation resource unavailable');
      }
    });
    await new Promise((resolve, reject) => {
      server.once('error', reject);
      server.listen(0, '127.0.0.1', resolve);
    });
    base = `http://127.0.0.1:${server.address().port}`;
    report.base = base;
    const { chromium } = require('../../web/node_modules/playwright');
    report.playwright = require('../../web/node_modules/playwright/package.json').version;
    browser = await chromium.launch({ headless: true });
    report.browser = browser.version();
    context = await browser.newContext({ viewport: { width: 1440, height: 1000 }, colorScheme: 'light' });
    context.setDefaultTimeout(15000);
    context.setDefaultNavigationTimeout(30000);
    context.on('request', request => {
      if (request.url().startsWith('http') && new URL(request.url()).origin !== base) {
        external.set(request.url(), { url: request.url(), resourceType: request.resourceType() });
      }
    });
    context.on('requestfailed', request => {
      const entry = { stage, url: request.url(), error: request.failure()?.errorText };
      report.failedRequests.push(entry);
      if (external.has(request.url())) Object.assign(external.get(request.url()), { error: entry.error });
    });
    context.on('response', response => {
      const entry = { stage, url: response.url(), status: response.status() };
      if (response.status() >= 400) report.failedRequests.push(entry);
      if (external.has(response.url())) Object.assign(external.get(response.url()), { status: response.status() });
      const done = response.finished().then(error => {
        if (error) report.failedRequests.push({ ...entry, error: String(error) });
      }).catch(error => report.failedRequests.push({ ...entry, error: String(error) }));
      pendingResponses.add(done);
      done.finally(() => pendingResponses.delete(done));
    });
    page = await context.newPage();
    page.on('pageerror', error => report.pageErrors.push({ stage, error: String(error) }));
    page.on('console', message => {
      if (message.type() === 'error') report.consoleErrors.push({ stage, text: message.text(), location: message.location() });
    });

    const screenshot = async name => {
      const filename = `${name}.png`;
      await page.screenshot({ path: path.join(screenshots, filename), fullPage: false });
      report.screenshots.push({ name, path: `screenshots/${filename}`, url: page.url() });
    };
    const open = async pathname => {
      const response = await page.goto(base + pathname, { waitUntil: 'networkidle' });
      assert.equal(response?.status(), 200, `HTML must load: ${pathname}`);
      assert.match(await page.title(), /QuantumAtlas/);
      await page.locator('article#furo-main-content').waitFor({ state: 'visible' });
      assert.ok(await page.locator('link[rel="stylesheet"][href*="styles/furo.css"]').count(), 'Furo CSS must be linked');
      // Reading computed styles verifies that the normal theme was applied.
      const styled = await page.evaluate(() => [...document.styleSheets].some(sheet =>
        sheet.href?.includes('/styles/furo.css') && sheet.cssRules.length > 0));
      assert.ok(styled, `Furo stylesheet must load: ${pathname}`);
    };
    const check = async (name, action) => {
      stage = name;
      const result = { name, ok: false };
      report.checks.push(result);
      try {
        await action(result);
        result.ok = true;
      } catch (error) {
        result.error = String(error);
        if (page && !page.isClosed()) {
          try {
            result.failureState = await page.evaluate(() => ({
              url: location.href, title: document.title,
              text: document.body.innerText.slice(0, 3000),
              searchStatus: typeof Search === 'undefined' ? null : Search.status?.textContent,
            }));
            await screenshot(`failure-${name}`);
          } catch (captureError) {
            result.captureError = String(captureError);
          }
        }
      }
    };
    const localDownload = async (href, filename) => {
      const url = new URL(href, page.url());
      assert.equal(url.origin, base, 'Downloads must stay on the temporary local site');
      assert.ok(url.pathname.startsWith('/doc/_downloads/'), `Expected native Sphinx download: ${url.pathname}`);
      assert.equal(path.posix.basename(url.pathname), filename);
      const response = await context.request.get(url.href, { maxRedirects: 0 });
      assert.equal(response.status(), 200, `Download must load: ${filename}`);
      const bytes = await response.body();
      const value = JSON.parse(bytes.toString('utf8'));
      report.downloads.push({ filename, path: url.pathname, bytes: bytes.length, sha256: sha256(bytes) });
      return { value, bytes };
    };

    await check('user-home', async result => {
      await open('/doc/index.html');
      const sidebar = page.locator('.sidebar-drawer');
      assert.ok(await sidebar.isVisible(), 'Desktop navigation must be visible');
      const article = await page.locator('article').boundingBox();
      const navigation = await sidebar.boundingBox();
      assert.ok(article.width > 400 && navigation.width > 180, 'Furo content/navigation must occupy usable space');
      result.layout = await page.evaluate(() => ({ viewport: innerWidth, width: document.documentElement.scrollWidth }));
      assert.ok(result.layout.width <= result.layout.viewport + 2, 'Homepage must not overflow horizontally');
      result.navigation = await page.locator('.sidebar-tree a').evaluateAll(nodes => nodes.map(node => ({ text: node.textContent.trim(), href: node.getAttribute('href') })));
      for (const component of ['qatlas-cli', 'qatlas-search', 'qatlas-rag']) {
        assert.ok(result.navigation.some(link => link.href === `_collections/${component}/index.html`), `Missing component navigation: ${component}`);
      }
      await screenshot('user-home');
      await page.locator('.sidebar-tree a[href="_collections/qatlas-cli/index.html"]').first().click();
      await page.waitForURL('**/doc/_collections/qatlas-cli/index.html');
      assert.match(await page.locator('article h1').innerText(), /qatlas-cli/);
      result.componentNavigationClicked = true;
    });

    await check('component-pages', async result => {
      result.pages = [];
      for (const [component, files] of [
        ['qatlas-cli', ['index', 'overview']],
        ['qatlas-search', ['index', 'overview', 'scorers', 'survey-search']],
        ['qatlas-rag', ['index', 'overview']],
      ]) {
        for (const file of files) {
          const pathname = `/doc/_collections/${component}/${file}.html`;
          await open(pathname);
          const title = await page.locator('article h1').innerText();
          assert.ok(title.trim(), `Component page must contain a heading: ${pathname}`);
          if (['index', 'overview'].includes(file)) assert.ok(title.includes(component), `Wrong component content: ${pathname}`);
          result.pages.push({ path: pathname, heading: title });
        }
      }
    });

    await check('scorer-guide', async result => {
      await open('/doc/_collections/qatlas-search/scorers.html');
      assert.match(await page.locator('article h1').innerText(), /Scorer/);
      result.tables = await page.locator('article table').count();
      assert.ok(result.tables >= 4, 'Scorer Markdown tables must render as real tables');
      assert.match(await page.locator('article table').first().innerText(), /scorer/);
      await page.locator('article table').first().scrollIntoViewIfNeeded();
      await screenshot('scorers');
    });

    await check('scorer-downloads', async () => {
      await open('/doc/_collections/qatlas-search/overview.html');
      for (const filename of ['recent.json', 'classic.json', 'annual-rate.json']) {
        const link = page.locator(`article a[href$="/${filename}"]`).first();
        const { value } = await localDownload(await link.getAttribute('href'), filename);
        assert.equal(value.language, 'qatlas-expr-v1');
        assert.equal(typeof value.score, 'string');
      }
    });

    for (const query of ['Scorer', '检索']) {
      await check(`search-${query === 'Scorer' ? 'en' : 'zh'}`, async result => {
        await open('/doc/search.html?q=' + encodeURIComponent(query));
        // Sphinx 8 exposes Search as a global lexical const, not window.Search.
        await page.waitForFunction(() => typeof Search !== 'undefined' && Search.hasIndex(), null, { timeout: 30000 });
        await page.waitForFunction(() => Search.status?.textContent?.trim(), null, { timeout: 30000 });
        const links = await page.locator('#search-results li a').evaluateAll(nodes => nodes.map(node => ({ text: node.innerText, href: node.getAttribute('href') })));
        const status = await page.evaluate(() => Search.status.textContent.trim());
        assert.ok(links.length > 0, `Search must return results for ${query}`);
        const expected = query === 'Scorer' ? '_collections/qatlas-search/scorers.html' : '_collections/qatlas-rag/overview.html';
        assert.ok(links.some(link => new URL(link.href, page.url()).pathname === '/doc/' + expected), `Search must find the real component path ${expected}`);
        result.matches = links.length;
        report.searches.push({ query, status, links });
        await screenshot(`search-${query === 'Scorer' ? 'en' : 'zh'}`);
      });
    }

    await check('mermaid', async result => {
      await open('/doc/manual/index.html');
      const host = page.locator('article .mermaid').first();
      await host.scrollIntoViewIfNeeded();
      const svg = host.locator('svg');
      await svg.waitFor({ state: 'visible', timeout: 30000 });
      assert.equal(await host.getAttribute('data-processed'), 'true', 'The standard Mermaid extension must process the diagram');
      assert.equal(await host.locator('[aria-roledescription="error"], .error-icon, .error-text').count(), 0, 'Mermaid must not render an error diagram');
      result.nodes = await svg.locator('g.node').count();
      result.edges = await svg.locator('path.flowchart-link').count();
      result.box = await svg.boundingBox();
      assert.ok(result.nodes >= 5 && result.edges >= 4, 'Mermaid must render flowchart nodes and edges, not raw source');
      assert.ok(result.box.width > 100 && result.box.height > 80, 'Rendered Mermaid SVG must occupy space');
      result.labels = await svg.textContent();
      assert.match(result.labels, /PostgreSQL/);
      await screenshot('mermaid');
    });

    await check('api-explorer', async result => {
      await open('/doc/manual/server/api-explorer.html');
      const text = await page.locator('article').innerText();
      assert.match(text, /当前实例/);
      assert.match(text, /普通链接/);
      const swagger = page.locator('article a[href="/swagger/"]');
      assert.equal(await swagger.count(), 1, 'Swagger entry must remain an ordinary current-instance link');
      assert.match(await swagger.innerText(), /Swagger UI/);
      assert.equal(await swagger.getAttribute('onclick'), null);
      assert.equal(await page.locator('iframe, swagger-ui').count(), 0, 'Static reference must not embed a live API explorer');
      result.swaggerLink = { href: await swagger.getAttribute('href'), text: await swagger.innerText(), followed: false };
      const schemaLink = page.locator('article a[href$="/swagger.json"]').first();
      const schema = await localDownload(await schemaLink.getAttribute('href'), 'swagger.json');
      assert.ok(schema.value.swagger || schema.value.openapi, 'Schema download must be OpenAPI/Swagger JSON');
      result.schemaPaths = Object.keys(schema.value.paths || {}).length;
      assert.ok(result.schemaPaths > 0, 'Schema must contain real API paths');
      const source = await fs.readFile(path.resolve(__dirname, '../../internal/apidocs/swagger.json'));
      assert.equal(sha256(schema.bytes), sha256(source), 'Downloaded schema must equal the canonical repository source');
      await screenshot('api-explorer');
    });

    await check('admin-home', async result => {
      // The existing admin site intentionally has dev/index.html, not index.html.
      await open('/devdoc/dev/index.html');
      assert.match(await page.locator('article h1').innerText(), /开发文档/);
      assert.match(await page.title(), /开发文档/);
      result.navigation = await page.locator('.sidebar-tree a').evaluateAll(nodes => nodes.map(node => ({ text: node.textContent.trim(), href: node.getAttribute('href') })));
      assert.ok(result.navigation.some(link => link.href === 'development.html'), 'Admin navigation must contain development guide');
      assert.ok(result.navigation.every(link => !link.href?.includes('_collections/')), 'Admin site must not show component navigation');
      await screenshot('admin-home');
    });

    await Promise.all([...pendingResponses]);
    report.externalResources = [...external.values()];
    report.externalFailures = report.failedRequests.filter(item => new URL(item.url).origin !== base);
    if (report.externalFailures.length) {
      report.networkExplanation = 'External CDN/font loads failed. The test did not replace, block, or hide those dependencies; fix network access or deliberately change the documented dependency before accepting this run.';
    }
    report.ok = report.checks.every(check => check.ok)
      && report.failedRequests.length === 0 && report.serverFailures.length === 0
      && report.pageErrors.length === 0 && report.consoleErrors.length === 0;
  } catch (error) {
    report.error = String(error);
    report.stage = stage;
  } finally {
    // Cleanup must run even when browser launch, navigation or assertions fail.
    if (browser) {
      try { await browser.close(); } catch (error) { report.cleanupErrors.push(String(error)); }
    }
    if (server?.listening) {
      try {
        server.closeAllConnections();
        await new Promise((resolve, reject) => server.close(error => error ? reject(error) : resolve()));
      } catch (error) { report.cleanupErrors.push(String(error)); }
    }
    report.externalResources = [...external.values()];
    if (report.cleanupErrors.length) report.ok = false;
    report.finishedAt = new Date().toISOString();
    await fs.writeFile(path.join(output, 'report.json'), JSON.stringify(report, null, 2) + '\n');
    console.log(JSON.stringify({
      ok: report.ok, report: path.join(output, 'report.json'),
      checks: report.checks.map(({ name, ok, error }) => ({ name, ok, error })),
      failedRequests: report.failedRequests, pageErrors: report.pageErrors,
      consoleErrors: report.consoleErrors, error: report.error,
    }, null, 2));
    if (!report.ok) process.exitCode = 1;
  }
}

main().catch(error => { console.error(error); process.exitCode = 1; });
