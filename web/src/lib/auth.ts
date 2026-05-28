// React hooks and helpers around pb.authStore.
//
// pb.authStore is a vanilla event emitter; we wrap it in a React state hook
// so components re-render on login / logout / token refresh.
//
// GitHub OAuth uses a manual redirect flow (see PocketBase docs §"Manual code
// exchange"):
//   1. loginWithGitHub() fetches provider config via listAuthMethods, stashes
//      verifier+state+returnTo in sessionStorage, then navigates the WHOLE
//      page to GitHub's authorize URL with redirect_uri pointing at our SPA
//      /auth/callback route.
//   2. GitHub bounces back to /auth/callback?code=...&state=... which mounts
//      the AuthCallback route. It calls completeOAuth2Login() to exchange
//      the code via pb.collection('users').authWithOAuth2Code(...).
//
// We avoid the SDK's popup-based authWithOAuth2() because it (a) opens an
// about:blank popup before fetching providers (visible flash + popup-blocker
// fragile) and (b) relies on a SSE listener in the opener tab plus the
// admin UI's /_/#/auth/oauth2-redirect-* hash bridge.

import { useEffect, useState } from 'react'
import { pb, AUTH_COLLECTION } from './pb'

export type AuthUser = {
  id: string
  email: string
  name?: string
  avatar?: string
  username?: string
}

export type AuthState = {
  isAuthed: boolean
  // True while the initial server-side token check is in flight. The local
  // JWT exp claim (pb.authStore.isValid) only tells us the token *would* be
  // valid; the server may have rotated its secret, deleted the user, or
  // revoked the session. Until bootstrap settles, treat auth as undetermined
  // so we don't flash protected UI for stale tokens.
  isChecking: boolean
  token: string
  user: AuthUser | null
}

// If no token is in localStorage there's nothing to verify; we're "ready"
// immediately and the gate will route to /login on first paint.
let bootstrapDone = !pb.authStore.token
let bootstrapPromise: Promise<void> | null = null
const readyListeners = new Set<() => void>()

// Dev-only escape hatch: when VITE_DEV_FAKE_AUTH=1 in `vite dev`, useAuth()
// returns a stub authed state so UI work on protected routes can proceed
// without going through GitHub OAuth (esp. useful when the dev server is
// reverse-proxied through a different origin than the production qatlas
// instance, so prod localStorage tokens don't transfer). Backend-dependent
// data (papers, search, share) will 401 — this only unblocks the chrome.
// Production builds strip this branch entirely via dead-code elimination
// because `import.meta.env.DEV` is statically `false` at build time, so it
// cannot leak past `vite dev`.
const FAKE_AUTH =
  import.meta.env.DEV && import.meta.env.VITE_DEV_FAKE_AUTH === '1'

const FAKE_USER: AuthUser = {
  id: 'dev_fake_user',
  email: 'dev@local',
  name: 'Dev User',
  username: 'dev',
}

if (FAKE_AUTH) {
  bootstrapDone = true
  console.warn(
    '[auth] VITE_DEV_FAKE_AUTH=1 — bypassing PocketBase auth. UI gates will pass; API calls will still 401.',
  )
}

function notifyReady() {
  for (const listener of readyListeners) listener()
}

export function ensureAuthBootstrap(): Promise<void> {
  if (FAKE_AUTH) return Promise.resolve()
  if (bootstrapDone) return Promise.resolve()
  if (bootstrapPromise) return bootstrapPromise
  bootstrapPromise = (async () => {
    try {
      await pb.collection(AUTH_COLLECTION).authRefresh()
    } catch {
      // Server rejected the token (revoked, expired against server clock,
      // user removed, PB secret rotated, etc). Drop it so we fall through
      // cleanly to /login instead of rendering protected views that 401.
      pb.authStore.clear()
    } finally {
      bootstrapDone = true
      notifyReady()
    }
  })()
  return bootstrapPromise
}

function snapshot(): AuthState {
  if (FAKE_AUTH) {
    return {
      isAuthed: true,
      isChecking: false,
      token: 'dev_fake_token',
      user: FAKE_USER,
    }
  }
  const record = pb.authStore.record as Record<string, unknown> | null
  return {
    isAuthed: bootstrapDone && pb.authStore.isValid,
    isChecking: !bootstrapDone,
    token: pb.authStore.token,
    user: record
      ? {
          id: String(record.id ?? ''),
          email: String(record.email ?? ''),
          name: record.name ? String(record.name) : undefined,
          avatar: record.avatar ? String(record.avatar) : undefined,
          username: record.username ? String(record.username) : undefined,
        }
      : null,
  }
}

export function useAuth(): AuthState {
  const [state, setState] = useState<AuthState>(snapshot)
  useEffect(() => {
    const update = () => setState(snapshot())
    const off = pb.authStore.onChange(update)
    readyListeners.add(update)
    return () => {
      off()
      readyListeners.delete(update)
    }
  }, [])
  return state
}

const PENDING_KEY = 'qatlas_oauth_pending'

type PendingOAuth = {
  provider: string
  state: string
  codeVerifier: string
  redirectURL: string
  from: string | null
}

// Kick off the GitHub OAuth redirect flow. Returns nothing meaningful — on
// success the page navigates away to github.com and never resumes here. Only
// the initial provider fetch can throw synchronously (network down, GitHub
// provider disabled on the server, etc).
export async function loginWithGitHub(from?: string): Promise<void> {
  const methods = await pb.collection(AUTH_COLLECTION).listAuthMethods()
  const provider = methods.oauth2?.providers?.find((p) => p.name === 'github')
  if (!provider) {
    throw new Error('GitHub login is not enabled on this server.')
  }
  const redirectURL = `${window.location.origin}/auth/callback`
  const pending: PendingOAuth = {
    provider: provider.name,
    state: provider.state,
    codeVerifier: provider.codeVerifier,
    redirectURL,
    from: from ?? null,
  }
  sessionStorage.setItem(PENDING_KEY, JSON.stringify(pending))
  // provider.authURL already ends with "&redirect_uri=" — just append the
  // encoded SPA callback URL and navigate. window.location.assign keeps
  // GitHub's authorize page in the back/forward history so the browser Back
  // button behaves naturally.
  window.location.assign(provider.authURL + encodeURIComponent(redirectURL))
}

export type OAuthCompletion = {
  from: string | null
}

// Exchange the OAuth2 code returned by GitHub for a PocketBase session.
// Called from /auth/callback. Throws if state mismatches, sessionStorage was
// cleared (user closed and reopened the tab mid-flow), or the server rejects
// the exchange.
export async function completeOAuth2Login(
  code: string,
  state: string,
): Promise<OAuthCompletion> {
  const raw = sessionStorage.getItem(PENDING_KEY)
  if (!raw) {
    throw new Error(
      'No pending OAuth login found. Please start the sign-in flow again.',
    )
  }
  let pending: PendingOAuth
  try {
    pending = JSON.parse(raw) as PendingOAuth
  } catch {
    sessionStorage.removeItem(PENDING_KEY)
    throw new Error('Corrupted OAuth state. Please sign in again.')
  }
  if (pending.state !== state) {
    sessionStorage.removeItem(PENDING_KEY)
    throw new Error('OAuth state mismatch. Please sign in again.')
  }
  try {
    await pb
      .collection(AUTH_COLLECTION)
      .authWithOAuth2Code(
        pending.provider,
        code,
        pending.codeVerifier,
        pending.redirectURL,
      )
  } finally {
    sessionStorage.removeItem(PENDING_KEY)
  }
  // Bootstrap already counts as resolved once we have a server-issued auth
  // record from the exchange.
  bootstrapDone = true
  notifyReady()
  return { from: pending.from }
}

export function logout() {
  pb.authStore.clear()
}
