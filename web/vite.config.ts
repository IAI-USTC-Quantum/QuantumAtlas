import { fileURLToPath } from 'node:url'
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import { TanStackRouterVite } from '@tanstack/router-plugin/vite'

export default defineConfig({
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
    // Allow the EasyTier mesh peers and Alibaba's public IP so a reverse
    // proxy can preview this dev server from outside. Does not affect
    // `vite build` / `vite preview`.
    allowedHosts: [
      '47.102.36.175',
      '10.144.18.66',
      '10.144.18.88',
      'localhost',
      '127.0.0.1',
    ],
  },
})
