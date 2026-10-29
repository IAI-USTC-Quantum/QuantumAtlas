import PocketBase, { BaseAuthStore } from 'pocketbase'
import { pb, AUTH_COLLECTION } from './pb'
import {
  currentMainAuthSnapshot,
  currentMainSessionToken,
  withMainSharedSessionLifecycle,
  type SharedAuthSnapshot,
  type SharedSessionLifecycle,
} from './shared-web-session'

function detached(snapshot: SharedAuthSnapshot): PocketBase {
  // SDK auth methods save automatically. Never let an in-flight request own
  // the global LocalAuthStore: an old tab may change it without taking locks.
  const store = new BaseAuthStore()
  if (!snapshot.blocked) store.save(snapshot.token, pb.authStore.record)
  return new PocketBase(pb.baseURL, store, pb.lang)
}

async function sync(lifecycle: SharedSessionLifecycle, onSharedError: () => void) {
  try { await lifecycle.syncSharedSession(currentMainSessionToken) }
  catch { onSharedError() }
}

export async function refreshSharedMainAuth(onSharedError: () => void): Promise<void> {
  // Capture before import/queue waits too: a queued bootstrap cannot adopt a
  // different tab's later principal or an already-completed logout.
  const expected = currentMainAuthSnapshot()
  await withMainSharedSessionLifecycle(async (lifecycle) => {
    const snapshot = lifecycle.authSnapshot()
    if (!lifecycle.isCurrent(expected)) return
    if (snapshot.blocked) {
      // A legacy page may have written a canceled token back after logout.
      // This is local cleanup only; a normal refresh never lifts the guard.
      pb.authStore.clear()
      return
    }
    if (!snapshot.token) return
    const isolated = detached(snapshot)
    try {
      await isolated.collection(AUTH_COLLECTION).authRefresh({
        signal: lifecycle.signal, requestKey: null,
      })
    } catch {
      // Preserve a newer principal and never clear on lifecycle cancellation.
      if (!lifecycle.signal.aborted && lifecycle.isCurrent(snapshot)) pb.authStore.clear()
      return
    }
    lifecycle.signal.throwIfAborted()
    if (!lifecycle.commitAuth(snapshot, () => {
      pb.authStore.save(isolated.authStore.token, isolated.authStore.record)
    })) return
    await sync(lifecycle, onSharedError)
  })
}

export type SharedOAuthAttempt = {
  provider: string
  codeVerifier: string
  redirectURL: string
  mode: 'login' | 'link'
  sharedAuthEpoch?: string
  sharedAuthSubject?: string | null
}

export async function exchangeSharedMainAuth<T>(
  pending: SharedOAuthAttempt,
  code: string,
  onCommitted: (previousId: string | null) => T,
  onSharedError: () => void,
): Promise<T> {
  return withMainSharedSessionLifecycle(async (lifecycle) => {
    const snapshot = lifecycle.authSnapshot()
    if (pending.sharedAuthEpoch !== snapshot.epoch ||
      (pending.mode === 'link' && (snapshot.blocked ||
        pending.sharedAuthSubject !== (pb.authStore.record?.id ?? null)))) {
      throw new Error('This sign-in was superseded. Please start sign-in again.')
    }
    const previousId = pending.mode === 'link' ? (pb.authStore.record?.id ?? null) : null
    const isolated = detached(snapshot)
    await isolated.collection(AUTH_COLLECTION).authWithOAuth2Code(
      pending.provider, code, pending.codeVerifier, pending.redirectURL, undefined,
      { signal: lifecycle.signal, requestKey: null },
    )
    lifecycle.signal.throwIfAborted()
    if (!lifecycle.commitAuth(snapshot, () => {
      pb.authStore.save(isolated.authStore.token, isolated.authStore.record)
    }, true)) {
      throw new Error('This sign-in was superseded. Please start sign-in again.')
    }
    // Freeze binding results before the shared HTTP awaits, while still in the
    // same lifecycle. Only this explicit successful OAuth may clear the guard.
    const result = onCommitted(previousId)
    await sync(lifecycle, onSharedError)
    return result
  })
}
