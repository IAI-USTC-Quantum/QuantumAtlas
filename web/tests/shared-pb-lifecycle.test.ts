import PocketBase from 'pocketbase'
import { act, createElement } from 'react'
import { createRoot } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import type { SharedWebSessionClient } from '../src/lib/shared-web-session'

// Exact pinned protocol-client bytes and the ACTUAL installed PocketBase SDK.
// Only browser locks and HTTP responses are deterministic offline doubles.
vi.mock('/pursuit-session/client.js', async () => {
  // Keep standalone CI independent of another checkout or private task layout.
  // The test-only fixture is never included in the production UI bundle.
  const { readFile } = await import('node:fs/promises')
  const { createHash } = await import('node:crypto')
  const { resolve } = await import('node:path')
  const source = await readFile(resolve(process.cwd(), 'tests/fixtures/shared-session-client.mjs'), 'utf8')
  const metadata = JSON.parse(await readFile(resolve(process.cwd(), 'tests/fixtures/shared-session-client.source.json'), 'utf8')) as { sha256: string }
  if (createHash('sha256').update(source).digest('hex') !== metadata.sha256) throw new Error('Shared protocol fixture changed without its pinned digest.')
  const url = `data:text/javascript;base64,${Buffer.from(source).toString('base64')}`
  return await import(/* @vite-ignore */ url) as SharedWebSessionClient
})
vi.mock('sonner', () => ({ toast: { error: vi.fn() } }))

function deferred<T>() {
  let resolve!: (value: T) => void
  let reject!: (reason?: unknown) => void
  const promise = new Promise<T>((yes, no) => { resolve = yes; reject = no })
  return { promise, resolve, reject }
}
function token(id: string, version = 'initial') {
  // Synthetic local test JWT, never accepted by any real server.
  return `${btoa('{"alg":"none"}')}.${btoa(JSON.stringify({ id, exp: 4102444800, version }))}.test-signature`
}
const record = (id: string) => ({ id, collectionName: 'users', collectionId: 'users', email: `${id}@test.invalid` })
const authResponse = (id: string, version = 'refreshed') => new Response(JSON.stringify({
  token: token(id, version), record: record(id),
}), { status: 200, headers: { 'Content-Type': 'application/json' } })
const guard = () => JSON.parse(localStorage.getItem('qatlas_shared_auth_guard_v1') ?? '{"epoch":"initial","blocked":false}') as { epoch: string; blocked: boolean }

let paths: string[]
let handler: (path: string, init: RequestInit) => Response | Promise<Response>
beforeEach(() => {
  vi.resetModules()
  localStorage.clear()
  sessionStorage.clear()
  vi.stubEnv('VITE_DEV_FAKE_AUTH', '0')
  vi.stubEnv('VITE_SHARED_WEB_SESSION_ORIGIN', window.location.origin)
  vi.stubEnv('VITE_PB_URL', window.location.origin)
  let queue = Promise.resolve<unknown>(undefined)
  vi.stubGlobal('navigator', { locks: {
    request: (name: string, options: { signal: AbortSignal }, action: () => Promise<unknown>) => {
      expect(name).toBe('qatlas-web-session-v1')
      const result = queue.then(() => { options.signal.throwIfAborted(); return action() })
      queue = result.catch(() => {})
      return result
    },
  } })
  paths = []
  handler = (path) => {
    if (path.startsWith('/pursuit-session/')) return new Response(null, { status: 204 })
    throw new Error(`Unexpected offline request: ${path}`)
  }
  vi.stubGlobal('fetch', vi.fn((url: string | URL | Request, init: RequestInit = {}) => {
    const path = new URL(String(url), window.location.origin).pathname
    paths.push(path)
    return Promise.resolve(handler(path, init))
  }))
})
afterEach(() => {
  vi.useRealTimers()
  vi.unstubAllEnvs()
  vi.unstubAllGlobals()
})
async function setup() {
  const { pb } = await import('../src/lib/pb')
  pb.authStore.save(token('a'), record('a'))
  const otherTab = new PocketBase(window.location.origin)
  const auth = await import('../src/lib/auth')
  const adapter = await import('../src/lib/shared-web-session')
  return { pb, otherTab, auth, adapter }
}
function pending(mode: 'login' | 'link' = 'login', epoch = guard().epoch) {
  sessionStorage.setItem('qatlas_oauth_pending', JSON.stringify({
    provider: 'gitea', state: 'state', codeVerifier: 'verifier',
    redirectURL: `${window.location.origin}/auth/callback`, from: '/pursuit-session/', mode,
    sharedAuthEpoch: epoch, sharedAuthSubject: mode === 'link' ? 'a' : null,
  }))
}

describe('real SDK: serialized refresh/save/mint/logout across tabs', () => {
  it('logout cannot acknowledge ahead of an inflight refresh; nothing revives after ack', async () => {
    const { pb, otherTab, auth, adapter } = await setup()
    const response = deferred<Response>(), started = deferred<void>()
    handler = (path, init) => {
      if (path.endsWith('/auth-refresh')) {
        expect(init.signal).toBeInstanceOf(AbortSignal)
        started.resolve()
        return response.promise
      }
      return new Response(null, { status: 204 })
    }
    const bootstrap = auth.ensureAuthBootstrap()
    await started.promise
    let acknowledged = false
    const logout = adapter.logoutMainSharedSession(() => otherTab.authStore.clear()).then(() => { acknowledged = true })
    await Promise.resolve()
    expect(acknowledged).toBe(false)
    expect(paths).not.toContain('/pursuit-session/logout')
    response.resolve(authResponse('a'))
    await Promise.all([bootstrap, logout])
    expect(pb.authStore.token).toBe('')
    expect(otherTab.authStore.token).toBe('')
    expect(guard().blocked).toBe(true)
    const atAck = paths.length
    await adapter.syncMainSharedSession()
    expect(paths).toHaveLength(atAck)
    expect(paths.at(-1)).toBe('/pursuit-session/logout')
  })

  it('a bootstrap queued behind logout never runs SDK refresh or issuer after ack', async () => {
    const { pb, otherTab, auth, adapter } = await setup()
    const response = deferred<Response>(), started = deferred<void>()
    handler = (path) => {
      expect(path).toBe('/pursuit-session/logout')
      started.resolve()
      return response.promise
    }
    const logout = adapter.logoutMainSharedSession(() => otherTab.authStore.clear())
    await started.promise
    const bootstrap = auth.ensureAuthBootstrap()
    response.resolve(new Response(null, { status: 204 }))
    await Promise.all([logout, bootstrap])
    expect(pb.authStore.token).toBe('')
    expect(paths).toEqual(['/pursuit-session/logout'])
  })

  it('legacy SDK late write after successful logout cannot mint; guarded refresh clears it', async () => {
    const { pb, otherTab, auth, adapter } = await setup()
    const response = deferred<Response>(), started = deferred<void>()
    handler = (path) => {
      if (path.endsWith('/auth-refresh')) { started.resolve(); return response.promise }
      return new Response(null, { status: 204 })
    }
    // Deliberately reproduce the old version's uncoordinated automatic save.
    const legacy = otherTab.collection('users').authRefresh()
    await started.promise
    await auth.logout()
    const epoch = guard().epoch
    expect(pb.authStore.token).toBe('')
    const atAck = paths.length
    response.resolve(authResponse('a', 'legacy-late'))
    await legacy
    expect(pb.authStore.token).toBe(token('a', 'legacy-late'))
    await adapter.syncMainSharedSession()
    const { refreshSharedMainAuth } = await import('../src/lib/shared-pb-auth')
    await refreshSharedMainAuth(vi.fn())
    expect(pb.authStore.token).toBe('')
    expect(paths).toHaveLength(atAck)
    expect(guard()).toEqual({ epoch, blocked: true })
  })

  it('actual main useAuth does not accept a legacy late token as a new sign-in', async () => {
    const { pb, otherTab, auth } = await setup()
    handler = (path) => path.endsWith('/auth-refresh')
      ? authResponse('a') : new Response(null, { status: 204 })
    await auth.ensureAuthBootstrap()
    await auth.logout()
    otherTab.authStore.save(token('a', 'legacy-late'), record('a'))
    vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
    const element = document.createElement('div')
    document.body.append(element)
    const root = createRoot(element)
    let snapshot: ReturnType<typeof auth.useAuth> | undefined
    function Probe() { snapshot = auth.useAuth(); return null }
    try {
      await act(async () => root.render(createElement(Probe)))
      expect(pb.authStore.isValid).toBe(true) // SDK alone WOULD accept it
      expect(snapshot?.isAuthed).toBe(false) // lifecycle guard does not
      expect(snapshot?.isChecking).toBe(false)
    } finally {
      await act(async () => root.unmount())
      element.remove()
    }
  })

  it.each([200, 401])('late A refresh (%s) cannot overwrite or clear newer principal B', async (status) => {
    const { pb, otherTab, auth } = await setup()
    const response = deferred<Response>(), started = deferred<void>()
    handler = (path) => {
      expect(path).toMatch(/\/auth-refresh$/)
      started.resolve()
      return response.promise
    }
    const bootstrap = auth.ensureAuthBootstrap()
    await started.promise
    otherTab.authStore.save(token('b'), record('b'))
    response.resolve(status === 200 ? authResponse('a') : new Response('{}', { status }))
    await bootstrap
    expect(pb.authStore.token).toBe(token('b'))
    expect(pb.authStore.record?.id).toBe('b')
    expect(paths).toHaveLength(1)
  })

  it('passes the lifecycle abort signal to real SDK and never commits aborted refresh', async () => {
    const { pb, auth } = await setup()
    const started = deferred<AbortSignal>()
    handler = (_path, init) => new Promise<Response>((_resolve, reject) => {
      const signal = init.signal as AbortSignal
      started.resolve(signal)
      signal.addEventListener('abort', () => reject(signal.reason), { once: true })
    })
    vi.useFakeTimers()
    const bootstrap = auth.ensureAuthBootstrap()
    const signal = await started.promise
    expect(signal.aborted).toBe(false)
    await vi.advanceTimersByTimeAsync(15000)
    await bootstrap
    expect(signal.aborted).toBe(true)
    expect(pb.authStore.token).toBe(token('a'))
    expect(paths).toHaveLength(1)
  })
})

describe('real SDK: explicit OAuth generations, binding and cancellation', () => {
  it('logout invalidates an earlier OAuth pending, without exchanging or restoring auth', async () => {
    const { pb, auth } = await setup()
    pending()
    await auth.logout()
    const atAck = paths.length
    await expect(auth.completeOAuth2Login('code', 'state')).rejects.toThrow('superseded')
    expect(pb.authStore.token).toBe('')
    expect(paths).toHaveLength(atAck)
    expect(sessionStorage.getItem('qatlas_oauth_pending')).toBeNull()
  })

  it('only a new explicit successful OAuth clears the guard and grants a new session', async () => {
    const { pb, auth } = await setup()
    await auth.logout()
    const oldEpoch = guard().epoch
    // A late legacy write must not be sent as bearer auth for a new login.
    pb.authStore.save(token('a', 'legacy'), record('a'))
    pending('login', oldEpoch)
    handler = (path, init) => {
      if (path.endsWith('/auth-with-oauth2')) {
        expect(init.signal).toBeInstanceOf(AbortSignal)
        expect(new Headers(init.headers).get('Authorization')).toBeNull()
        return authResponse('b', 'new-oauth')
      }
      if (path.endsWith('/session')) {
        expect(new Headers(init.headers).get('Authorization')).toBe(`Bearer ${token('b', 'new-oauth')}`)
      }
      return new Response(null, { status: 204 })
    }
    expect(await auth.completeOAuth2Login('code', 'state')).toEqual({ from: '/pursuit-session/', switchedAccount: false })
    expect(pb.authStore.token).toBe(token('b', 'new-oauth'))
    expect(guard().blocked).toBe(false)
    expect(guard().epoch).not.toBe(oldEpoch)
    expect(paths.slice(-3)).toEqual(['/api/collections/users/auth-with-oauth2', '/pursuit-session/context', '/pursuit-session/session'])
  })

  it('OAuth automatic SDK save and mint finish before queued logout; ack stays final', async () => {
    const { pb, otherTab, auth, adapter } = await setup()
    pending()
    const response = deferred<Response>(), started = deferred<void>()
    handler = (path) => {
      if (path.endsWith('/auth-with-oauth2')) { started.resolve(); return response.promise }
      return new Response(null, { status: 204 })
    }
    const completion = auth.completeOAuth2Login('code', 'state')
    await started.promise
    const logout = adapter.logoutMainSharedSession(() => otherTab.authStore.clear())
    await Promise.resolve()
    expect(paths).not.toContain('/pursuit-session/logout')
    response.resolve(authResponse('b'))
    await Promise.all([completion, logout])
    expect(pb.authStore.token).toBe('')
    expect(guard().blocked).toBe(true)
    expect(paths.at(-1)).toBe('/pursuit-session/logout')
  })

  it('actual SDK late OAuth response after abort remains detached and lock-held until settled', async () => {
    const { pb, auth, adapter } = await setup()
    pending()
    const response = deferred<Response>(), started = deferred<AbortSignal>()
    const saves: string[] = []
    const off = pb.authStore.onChange((value) => saves.push(value))
    handler = (path, init) => {
      if (path.endsWith('/auth-with-oauth2')) {
        started.resolve(init.signal as AbortSignal)
        return response.promise // intentionally ignores cancellation
      }
      return new Response(null, { status: 204 })
    }
    vi.useFakeTimers()
    const outcome = auth.completeOAuth2Login('code', 'state').catch((error: unknown) => error)
    const signal = await started.promise
    await vi.advanceTimersByTimeAsync(15000)
    expect(signal.aborted).toBe(true)
    const logout = adapter.logoutMainSharedSession(() => pb.authStore.clear())
    await Promise.resolve()
    expect(paths).not.toContain('/pursuit-session/logout')
    response.resolve(authResponse('a', 'must-stay-detached'))
    expect(await outcome).toMatchObject({ name: 'AbortError' })
    await logout
    off()
    expect(saves).toEqual([''])
    expect(pb.authStore.token).toBe('')
    expect(paths).toEqual(['/api/collections/users/auth-with-oauth2', '/pursuit-session/logout'])
    expect(guard().blocked).toBe(true)
  })

  it('late detached OAuth result cannot overwrite principal B or mint for A', async () => {
    const { pb, otherTab, auth } = await setup()
    pending()
    const response = deferred<Response>(), started = deferred<void>()
    handler = (path) => {
      expect(path).toMatch(/\/auth-with-oauth2$/)
      started.resolve()
      return response.promise
    }
    const completion = auth.completeOAuth2Login('code', 'state')
    const rejected = expect(completion).rejects.toThrow('superseded')
    await started.promise
    otherTab.authStore.save(token('b'), record('b'))
    response.resolve(authResponse('a'))
    await rejected
    expect(pb.authStore.token).toBe(token('b'))
    expect(paths).toHaveLength(1)
  })

  it.each([false, true])('preserves real SDK link-mode result and binding flags (switch=%s)', async (switched) => {
    const { pb, auth } = await setup()
    pending('link')
    handler = (path, init) => {
      if (path.endsWith('/auth-with-oauth2')) {
        expect(new Headers(init.headers).get('Authorization')).toBe(token('a'))
        return authResponse(switched ? 'b' : 'a')
      }
      return new Response(null, { status: 204 })
    }
    expect(await auth.completeOAuth2Login('code', 'state')).toEqual({ from: '/pursuit-session/', switchedAccount: switched })
    expect(pb.authStore.record?.id).toBe(switched ? 'b' : 'a')
    expect(sessionStorage.getItem(switched ? auth.BIND_SWITCHED_KEY : auth.BIND_DONE_KEY)).toBe('1')
    expect(sessionStorage.getItem(switched ? auth.BIND_DONE_KEY : auth.BIND_SWITCHED_KEY)).toBeNull()
  })

  it('preserves SDK conflict handling without committing detached auth or clearing identity', async () => {
    const { pb, auth } = await setup()
    pending('link')
    handler = () => new Response(JSON.stringify({ data: {
      code: 'oauth_conflict', provider: 'gitea', login: 'new', conflict: 'username', existing: 'old',
    } }), { status: 409, headers: { 'Content-Type': 'application/json' } })
    await expect(auth.completeOAuth2Login('code', 'state')).rejects.toMatchObject({
      name: 'OAuthConflictError', info: { from: '/pursuit-session/', conflict: 'username' },
    })
    expect(pb.authStore.token).toBe(token('a'))
    expect(paths).toHaveLength(1)
    expect(guard()).toEqual({ epoch: 'initial', blocked: false })
  })

  it('actual login preparation captures epoch and no PB token in OAuth pending', async () => {
    const { auth } = await setup()
    await auth.logout()
    const origin = window.location.origin
    const actualWindow = window
    const assign = vi.fn()
    vi.stubGlobal('window', {
      location: { origin, assign }, localStorage: actualWindow.localStorage,
      addEventListener: actualWindow.addEventListener.bind(actualWindow),
      removeEventListener: actualWindow.removeEventListener.bind(actualWindow),
    })
    handler = (path, init) => {
      expect(path).toMatch(/\/auth-methods$/)
      expect(init.signal).toBeInstanceOf(AbortSignal)
      return new Response(JSON.stringify({ oauth2: { providers: [{
        name: 'gitea', state: 'provider-state', codeVerifier: 'provider-verifier',
        authURL: 'https://provider.test/authorize?redirect_uri=',
      }] } }), { status: 200, headers: { 'Content-Type': 'application/json' } })
    }
    await auth.loginWithOAuth2('gitea', '/pursuit-session/')
    const raw = sessionStorage.getItem('qatlas_oauth_pending')!
    expect(JSON.parse(raw)).toMatchObject({ sharedAuthEpoch: guard().epoch, sharedAuthSubject: null })
    expect(raw).not.toContain(token('a'))
    expect(assign).toHaveBeenCalledOnce()
    expect(guard().blocked).toBe(true) // starting OAuth alone never lifts it
  })
})
