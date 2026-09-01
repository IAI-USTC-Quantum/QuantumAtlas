// React hooks and helpers around pb.authStore.
//
// pb.authStore is a vanilla event emitter; we wrap it in a React state hook
// so components re-render on login / logout / token refresh.
//
// OAuth (GitHub / Gitea alike) uses a manual redirect flow (see PocketBase
// docs §"Manual code exchange"):
//   1. loginWithOAuth2(provider) fetches the provider config via
//      listAuthMethods, stashes verifier+state+returnTo in sessionStorage,
//      then navigates the WHOLE page to the provider's authorize URL with
//      redirect_uri pointing at our SPA /auth/callback route.
//   2. The provider bounces back to /auth/callback?code=...&state=... which
//      mounts the AuthCallback route. It calls completeOAuth2Login() to
//      exchange the code via pb.collection('users').authWithOAuth2Code(...).
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
// Set after a successful link-mode exchange; the dashboard reads (and
// clears) it to refetch /api/me so the new binding shows immediately
// despite the 5-minute staleTime on the me query.
export const BIND_DONE_KEY = 'qatlas_bind_done'
// Set when a link-mode exchange ended up switching the session to a
// DIFFERENT account (the identity was already bound there) instead of
// binding to the caller — the dashboard surfaces a warning.
export const BIND_SWITCHED_KEY = 'qatlas_bind_switched'

// OAuth2 provider names the server may expose on the users collection.
export type OAuth2ProviderName = 'github' | 'gitea'

// 'login' — the plain sign-in flow (creates or resumes a session).
// 'link'  — the dashboard 账号绑定 flow: the exchange is made while a
//           session is already held, so PocketBase links the OAuth2
//           identity to the signed-in record instead of creating one.
export type OAuth2FlowMode = 'login' | 'link'

type PendingOAuth = {
  provider: string
  state: string
  codeVerifier: string
  redirectURL: string
  from: string | null
  mode: OAuth2FlowMode
}

// The 409 body qatlasd's conflict hook returns when a new OAuth identity
// may match an existing account (same email, or same provider login).
export type OAuthConflictInfo = {
  code: 'oauth_conflict'
  provider: string
  login: string
  conflict: 'email' | 'username'
  existing: string
  from: string | null
}

export class OAuthConflictError extends Error {
  constructor(readonly info: OAuthConflictInfo) {
    super('OAuth sign-in matched an existing account')
    this.name = 'OAuthConflictError'
  }
}

// asOAuthConflict digs the conflict payload out of a PocketBase SDK
// error. The SDK exposes the parsed response body as err.data (which for
// our hand-written 409 is {status, message, data:{code, ...}}); tolerate
// both the nested and a flat shape.
function asOAuthConflict(err: unknown, from: string | null): OAuthConflictInfo | null {
  const body = (err as { data?: unknown } | null)?.data as
    | Record<string, unknown>
    | undefined
  if (!body) return null
  const payload =
    (body.data as Record<string, unknown> | undefined)?.code === 'oauth_conflict'
      ? (body.data as Record<string, unknown>)
      : body.code === 'oauth_conflict'
        ? body
        : null
  if (!payload) return null
  const conflict = payload.conflict === 'email' ? 'email' : 'username'
  return {
    code: 'oauth_conflict',
    provider: String(payload.provider ?? ''),
    login: String(payload.login ?? ''),
    conflict,
    existing: String(payload.existing ?? ''),
    from,
  }
}

// listLoginProviders returns the OAuth2 provider names the server has
// configured (e.g. ['github', 'gitea']); the login page renders one
// button per entry. An empty array means OAuth is entirely disabled.
export async function listLoginProviders(): Promise<string[]> {
  const methods = await pb.collection(AUTH_COLLECTION).listAuthMethods()
  return (methods.oauth2?.providers ?? []).map((p) => p.name)
}

// Kick off the OAuth redirect flow for the given provider ('github' or
// 'gitea'). Returns nothing meaningful — on success the page navigates
// away to the provider and never resumes here. Only the initial provider
// fetch can throw synchronously (network down, provider disabled on the
// server, etc).
export async function loginWithOAuth2(
  providerName: OAuth2ProviderName,
  from?: string,
  mode: OAuth2FlowMode = 'login',
): Promise<void> {
  const methods = await pb.collection(AUTH_COLLECTION).listAuthMethods()
  const provider = methods.oauth2?.providers?.find((p) => p.name === providerName)
  if (!provider) {
    throw new Error(`${providerName} login is not enabled on this server.`)
  }
  const redirectURL = `${window.location.origin}/auth/callback`
  const pending: PendingOAuth = {
    provider: provider.name,
    state: provider.state,
    codeVerifier: provider.codeVerifier,
    redirectURL,
    from: from ?? null,
    mode,
  }
  sessionStorage.setItem(PENDING_KEY, JSON.stringify(pending))
  // provider.authURL already ends with "&redirect_uri=" — just append the
  // encoded SPA callback URL and navigate. window.location.assign keeps
  // the provider's authorize page in the back/forward history so the
  // browser Back button behaves naturally.
  window.location.assign(provider.authURL + encodeURIComponent(redirectURL))
}

// Thin named wrappers — most call sites care about one specific provider.
export const loginWithGitHub = (from?: string) => loginWithOAuth2('github', from)
export const loginWithGitea = (from?: string) => loginWithOAuth2('gitea', from)

// linkProvider starts the dashboard binding flow: the same OAuth2 round
// trip, but because the session token stays in pb.authStore the code
// exchange links the identity to the signed-in account.
export function linkProvider(provider: OAuth2ProviderName, from: string): Promise<void> {
  return loginWithOAuth2(provider, from, 'link')
}

export type OAuthCompletion = {
  from: string | null
  // link mode only: true when the OAuth identity was already bound to a
  // DIFFERENT account and PocketBase switched the session to it instead
  // of binding — the caller must not present this as a successful bind.
  switchedAccount?: boolean
}

// Exchange the OAuth2 code returned by the provider for a PocketBase
// session. Called from /auth/callback. Throws OAuthConflictError when the
// server reports a possible match with an existing account (see
// internal/auth/oauth_conflict.go), and plain errors for everything else
// (state mismatch, cleared sessionStorage, rejected exchange).
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
  // For the link flow, remember who is binding so we can detect the
  // identity-owned-by-another-account case (PocketBase then signs us in
  // as that account rather than linking).
  const prevRecordId =
    pending.mode === 'link' ? (pb.authStore.record?.id ?? null) : null
  try {
    await pb
      .collection(AUTH_COLLECTION)
      .authWithOAuth2Code(
        pending.provider,
        code,
        pending.codeVerifier,
        pending.redirectURL,
      )
  } catch (e) {
    const conflict = asOAuthConflict(e, pending.from)
    if (conflict) {
      throw new OAuthConflictError(conflict)
    }
    throw e
  } finally {
    sessionStorage.removeItem(PENDING_KEY)
  }
  // Bootstrap already counts as resolved once we have a server-issued auth
  // record from the exchange.
  bootstrapDone = true
  notifyReady()
  if (pending.mode === 'link') {
    const nowRecordId = pb.authStore.record?.id ?? null
    if (prevRecordId && nowRecordId && nowRecordId !== prevRecordId) {
      sessionStorage.setItem(BIND_SWITCHED_KEY, '1')
    } else {
      sessionStorage.setItem(BIND_DONE_KEY, '1')
    }
  }
  return {
    from: pending.from,
    switchedAccount:
      pending.mode === 'link' &&
      prevRecordId != null &&
      pb.authStore.record?.id != null &&
      pb.authStore.record.id !== prevRecordId,
  }
}

export function logout() {
  pb.authStore.clear()
}
