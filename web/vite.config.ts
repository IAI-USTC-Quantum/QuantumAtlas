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
  const allowedHosts = env.VITE_DEV_ALLOWED_HOSTS
    ? env.VITE_DEV_ALLOWED_HOSTS.split(',').map((h) => h.trim())
    : ['localhost', '127.0.0.1']
  const proxy = proxyTarget
    ? {
        '/api': {
          target: proxyTarget,
          changeOrigin: true,
          // Accept self-signed certs (Caddy `tls internal`, dev
          // qatlasd with --tls-cert, etc.). Does not affect
          // production builds.
          secure: false,
          ws: true,
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
