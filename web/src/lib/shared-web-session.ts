// Optional same-origin bridge. Other QAtlas deployments make no requests and
// retain their original local-only logout behavior unless explicitly enabled.
const CLIENT_URL = '/pursuit-session/client.js'
export const SHARED_SESSION_ERROR =
  'Pursuit session synchronization failed. Your main login has been kept; please retry.'
export const SHARED_LOGOUT_ERROR =
  'Sign-out was not confirmed by the shared session service. You are still signed in; please retry.'

export interface SharedAuthSnapshot {
  token: string
  epoch: string
  blocked: boolean
}

export interface SharedSessionLifecycle {
  readonly signal: AbortSignal
  authSnapshot(): SharedAuthSnapshot
  isCurrent(snapshot: SharedAuthSnapshot): boolean
  commitAuth(snapshot: SharedAuthSnapshot, save: () => void, explicitOAuth?: boolean): boolean
  syncSharedSession(getToken: () => string): Promise<boolean>
  logoutSharedSession(clearLocalAuth: () => void): Promise<void>
}

export interface SharedWebSessionClient {
  syncSharedSession(getToken: () => string): Promise<boolean>
  logoutSharedSession(clearLocalAuth: () => void): Promise<void>
  withSharedSessionLifecycle<T>(action: (lifecycle: SharedSessionLifecycle) => Promise<T>): Promise<T>
}

export function sharedWebSessionEnabled(): boolean {
  const configured = import.meta.env.VITE_SHARED_WEB_SESSION_ORIGIN
  return typeof configured === 'string' && configured !== '' &&
    configured === window.location.origin
}

// Deliberately do not capture pb.authStore.token before import/lock/context
// awaits. The default store is shared across tabs; its current value wins.
function rawMainSessionToken(): string {
  const raw = window.localStorage.getItem('pocketbase_auth')
  if (!raw) return ''
  try {
    const stored: unknown = JSON.parse(raw)
    if (stored && typeof stored === 'object' && 'token' in stored &&
      typeof stored.token === 'string') return stored.token
  } catch {
    // Malformed local state is not authorization.
  }
  return ''
}

export function currentMainAuthSnapshot(): SharedAuthSnapshot {
  const token = rawMainSessionToken()
  const raw = window.localStorage.getItem('qatlas_shared_auth_guard_v1')
  if (!raw) return { token, epoch: 'initial', blocked: false }
  try {
    const guard: unknown = JSON.parse(raw)
    if (guard && typeof guard === 'object' && 'epoch' in guard && 'blocked' in guard &&
      typeof guard.epoch === 'string' && typeof guard.blocked === 'boolean') {
      return { token, epoch: guard.epoch, blocked: guard.blocked }
    }
  } catch { /* Corruption is cancellation, not a grant. */ }
  return { token, epoch: 'invalid', blocked: true }
}

export function sharedMainAuthBlocked(): boolean {
  return sharedWebSessionEnabled() && currentMainAuthSnapshot().blocked
}

export function currentMainSessionToken(): string {
  return sharedMainAuthBlocked() ? '' : rawMainSessionToken()
}

export async function withMainSharedSessionLifecycle<T>(
  action: (lifecycle: SharedSessionLifecycle) => Promise<T>,
): Promise<T> {
  const bridge = await client()
  if (!bridge) throw new Error('Shared web sessions are not enabled.')
  return bridge.withSharedSessionLifecycle(action)
}

async function client(): Promise<SharedWebSessionClient | null> {
  if (!sharedWebSessionEnabled()) return null
  // The configured value is ONLY an exact-origin opt-in, never an import or
  // token destination. Do not replace this fixed path with the env value.
  return await import(/* @vite-ignore */ CLIENT_URL) as SharedWebSessionClient
}

export async function syncMainSharedSession(): Promise<void> {
  const bridge = await client()
  if (bridge) await bridge.syncSharedSession(currentMainSessionToken)
}

export async function logoutMainSharedSession(clearLocalAuth: () => void): Promise<void> {
  const bridge = await client()
  if (bridge) await bridge.logoutSharedSession(clearLocalAuth)
  else clearLocalAuth()
}

export function isSharedSessionBootstrap(dest: string): boolean {
  // This is backend HTML, not a TanStack SPA route. Only this fixed return
  // path needs a whole-document load; all existing from handling is retained.
  return sharedWebSessionEnabled() && dest === '/pursuit-session/'
}
