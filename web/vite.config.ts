import { fileURLToPath } from 'node:url'
import { defineConfig, loadEnv } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { TanStackRouterVite } from '@tanstack/router-plugin/vite'

// `vite.config.ts` runs in node, where `process.env` does NOT auto-load
// .env files (that's the `import.meta.env` mechanism, client-side only).
// loadEnv reads .env / .env.development / .env.development.local in the
// usual vite precedence order so dev-only vars (VITE_DEV_API_TARGET etc.)
// are visible to the proxy/server config below.
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '')
  const proxyTarget = env.VITE_DEV_API_TARGET
  // dev-only opt-in: 把 /api/rag/* 单独反代到本机 sidecar（绕开
  // VITE_DEV_API_TARGET 指向的远端 qatlasd）。两种典型场景：
  //   1. 远端 qatlasd 没设 QATLAS_RAG_SIDECAR_URL — /api/rag/* 404 →
  //      SPA 看不到 "语义" toggle；
  //   2. 想在 dev 时改 sidecar 行为 / 看真 query trace，本地起 sidecar
  //      直接调，不来回打远端。
  // sidecar 自己监听 /healthz 和 /search（不是 /api/rag/healthz），
  // 所以 rewrite ^/api/rag → ''。
  const ragSidecarTarget = env.VITE_DEV_RAG_SIDECAR
  // dev-only: a server-side system PAT (QATLAS_SYSTEM_PAT on the target
  // qatlasd) so the proxied /api/* reads carry a real bearer. Without
  // it every read endpoint 401s now that the backend locks reads behind
  // authGuard. Injected only on /api (NOT /_ PocketBase admin or /share)
  // and only in `vite dev` — `vite build` never reads this branch, so the
  // token can't leak into a production bundle. Keep it in
  // .env.development.local (gitignored via *.local).
  const apiPat = env.VITE_DEV_API_PAT
  const apiAuthHeaders = apiPat ? { Authorization: `Bearer ${apiPat}` } : undefined
  const allowedHosts = env.VITE_DEV_ALLOWED_HOSTS
    ? env.VITE_DEV_ALLOWED_HOSTS.split(',').map((h) => h.trim())
    : ['localhost', '127.0.0.1']
  const proxy = proxyTarget
    ? {
        // RAG 反代必须放在 /api 之前 —— vite proxy 用 declaration 顺序
        // 首匹配，/api 会先吃掉 /api/rag/*。仅在 VITE_DEV_RAG_SIDECAR
        // 配置时挂载。
        ...(ragSidecarTarget
          ? {
              '/api/rag': {
                target: ragSidecarTarget,
                changeOrigin: true,
                secure: false,
                rewrite: (path: string) => path.replace(/^\/api\/rag/, ''),
              },
            }
          : {}),
        '/api': {
          target: proxyTarget,
          changeOrigin: true,
          // Accept self-signed certs (Caddy `tls internal`, dev
          // qatlasd with --tls-cert, etc.). Does not affect
          // production builds.
          secure: false,
          ws: true,
          ...(apiAuthHeaders ? { headers: apiAuthHeaders } : {}),
        },
        '/_': {
          target: proxyTarget,
          changeOrigin: true,
          secure: false,
        },
        '/share': {
          target: proxyTarget,
          changeOrigin: true,
          secure: false,
        },
        // Swagger UI lives at /swagger/ on qatlasd. Without this, vite's
        // SPA fallback returns index.html, then TanStack Router has no
        // matching route and bounces to /{lang}, which looks like "the
        // page jumped back".
        '/swagger': {
          target: proxyTarget,
          changeOrigin: true,
          secure: false,
        },
      }
    : undefined

  return {
    // base defaults to '/', matching the Go server's apis.Static mount at
    // /{path...}. No need to set it explicitly.
    plugins: [
      TanStackRouterVite({ target: 'react', autoCodeSplitting: true }),
      react(),
      tailwindcss(),
    ],
    resolve: {
      alias: {
        '@': fileURLToPath(new URL('./src', import.meta.url)),
      },
    },
    server: {
      // dev-only: vite 5+ rejects non-localhost Host headers by default.
      // Allow additional hosts when a reverse proxy fronts this dev
      // server (e.g. previewing from a VPN / public IP). Does not
      // affect `vite build` / `vite preview`.
      allowedHosts,
      // dev-only: vite serves only the SPA bundle, so /api/* would
      // fall back to index.html and the SPA's fetch would crash with
      // "Unexpected token '<', \"<!doctype \" is not valid JSON".
      // VITE_DEV_API_TARGET must point at a live qatlasd instance
      // (local or remote) — see web/README.md "Dev workflow" section
      // for setup. Without it, /api/*, /_/* and /share/* requests have
      // nowhere to go.
      proxy,
    },
  }
})
