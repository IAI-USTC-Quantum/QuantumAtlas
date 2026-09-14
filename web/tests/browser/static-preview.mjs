// A static Vite preview, NOT the application's dev server. Never import the
// project Vite config: it reads .env files and can configure credentialed proxies.
import { existsSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

// Playwright's webServer env inherits the runner environment. Remove everything
// except platform basics before loading Vite; no API targets, PATs, fake auth,
// proxy settings, or other business credentials reach the preview runtime.
const platformKeys = new Set(['PATH', 'SystemRoot', 'SYSTEMROOT', 'WINDIR', 'TMPDIR', 'TMP', 'TEMP'])
for (const key of Object.keys(process.env)) {
  if (!platformKeys.has(key)) delete process.env[key]
}
process.env.NODE_ENV = 'production'

const root = fileURLToPath(new URL('../../', import.meta.url))
if (!existsSync(new URL('../../dist/index.html', import.meta.url))) {
  throw new Error('Missing web/dist/index.html. Run the isolated production build before browser tests.')
}

const { preview } = await import('vite')
const server = await preview({
  configFile: false,
  envFile: false,
  root,
  build: { outDir: 'dist' },
  server: { proxy: {} },
  preview: {
    host: '127.0.0.1',
    port: 4177,
    strictPort: true,
    proxy: {},
    headers: {
      'Cache-Control': 'no-store',
      // Defense in depth, including requests made by module workers. Tests also
      // fail on CSP violations, so this does not conceal renderer regressions.
      'Content-Security-Policy': [
        "default-src 'self'",
        "script-src 'self' 'unsafe-inline'",
        "style-src 'self' 'unsafe-inline'",
        "connect-src 'self'",
        "font-src 'self'",
        "img-src 'self' data: blob:",
        "worker-src 'self' blob:",
        "object-src 'none'",
        "frame-src 'none'",
        "base-uri 'none'",
        "form-action 'none'",
      ].join('; '),
    },
  },
  plugins: [{
    name: 'browser-tests-static-only',
    configurePreviewServer(previewServer) {
      previewServer.middlewares.use((request, response, next) => {
        const path = new URL(request.url ?? '/', 'http://127.0.0.1:4177').pathname
        // No API request is ever forwarded or served via the SPA fallback, even
        // if a future browser test accidentally forgets to install its mocks.
        if (/^\/(?:api|_|share|swagger)(?:\/|$)/.test(path)) {
          response.statusCode = 501
          response.setHeader('Content-Type', 'text/plain')
          response.end('Static browser preview: backend access is forbidden; mock this request.')
          return
        }
        next()
      })
    },
  }],
})
server.printUrls()

for (const signal of ['SIGTERM', 'SIGINT']) {
  process.once(signal, () => {
    server.httpServer.close(() => process.exit(0))
    server.httpServer.closeAllConnections()
  })
}
