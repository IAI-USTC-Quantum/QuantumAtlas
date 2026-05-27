// Singleton PocketBase client.
//
// Production: SPA is served by the same PocketBase host that exposes /api/*,
// so the SDK derives its base URL from window.location.origin (default
// behavior when constructed with no argument).
//
// Development (vite dev server on a different port): set VITE_PB_URL to point
// at the live server, e.g. https://quantum-atlas.ai. CORS must allow the
// dev origin, which PocketBase does not by default — production builds are
// the supported path; dev-mode auth is best-effort.

import PocketBase from 'pocketbase'

const devURL = import.meta.env.VITE_PB_URL

export const pb = new PocketBase(devURL || undefined)

export const AUTH_COLLECTION = 'users'
