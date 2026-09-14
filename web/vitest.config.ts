import { defineConfig } from 'vitest/config'

// Do not load the application Vite config: unit tests need no route generation,
// API proxy, dev credentials, or production services.
export default defineConfig({
  envDir: false,
  test: {
    environment: 'jsdom',
    include: ['tests/*.test.ts'],
    testTimeout: 5_000,
    restoreMocks: true,
  },
})
