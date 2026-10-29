import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const bridge = vi.hoisted(() => ({
  syncSharedSession: vi.fn(),
  logoutSharedSession: vi.fn(),
  imported: vi.fn(),
}))
vi.mock('/pursuit-session/client.js', () => {
  bridge.imported()
  return bridge
})

beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
  vi.stubEnv('VITE_SHARED_WEB_SESSION_ORIGIN', '')
  localStorage.clear()
  bridge.syncSharedSession.mockResolvedValue(true)
  bridge.logoutSharedSession.mockImplementation(async (clear: () => void) => { clear() })
})
afterEach(() => vi.unstubAllEnvs())

describe('optional shared web session adapter', () => {
  it('defaults off: no dynamic import or issuer call; local logout unchanged', async () => {
    const adapter = await import('../src/lib/shared-web-session')
    const clear = vi.fn()
    await adapter.syncMainSharedSession()
    await adapter.logoutMainSharedSession(clear)
    expect(adapter.sharedWebSessionEnabled()).toBe(false)
    expect(clear).toHaveBeenCalledOnce()
    expect(bridge.imported).not.toHaveBeenCalled()
    expect(bridge.syncSharedSession).not.toHaveBeenCalled()
  })

  it.each([
    'https://evil.example', `${window.location.origin}/`,
    `${window.location.origin}/pursuit-session/client.js`,
    `${window.location.origin}.evil.example`,
  ])('does not import or send credentials for nonexact origin %s', async (origin) => {
    vi.stubEnv('VITE_SHARED_WEB_SESSION_ORIGIN', origin)
    const adapter = await import('../src/lib/shared-web-session')
    await adapter.syncMainSharedSession()
    expect(adapter.sharedWebSessionEnabled()).toBe(false)
    expect(bridge.imported).not.toHaveBeenCalled()
  })

  it('exact current origin imports only fixed module and passes a fresh storage reader', async () => {
    vi.stubEnv('VITE_SHARED_WEB_SESSION_ORIGIN', window.location.origin)
    const adapter = await import('../src/lib/shared-web-session')
    localStorage.setItem('pocketbase_auth', '{"token":"old-synthetic"}')
    await adapter.syncMainSharedSession()
    expect(adapter.sharedWebSessionEnabled()).toBe(true)
    expect(bridge.syncSharedSession).toHaveBeenCalledOnce()
    const getToken = bridge.syncSharedSession.mock.calls[0][0] as () => string
    localStorage.setItem('pocketbase_auth', '{"token":"fresh-synthetic","record":{"id":"untrusted"}}')
    expect(getToken()).toBe('fresh-synthetic')
    localStorage.removeItem('pocketbase_auth')
    expect(getToken()).toBe('')
  })

  it.each(['{broken', '{"token":1}', '{"record":{"id":"not-auth"}}', 'null'])('ignores invalid local auth %s', async (raw) => {
    const adapter = await import('../src/lib/shared-web-session')
    localStorage.setItem('pocketbase_auth', raw)
    expect(adapter.currentMainSessionToken()).toBe('')
  })

  it('propagates failed revocation and never clears local auth on its own', async () => {
    vi.stubEnv('VITE_SHARED_WEB_SESSION_ORIGIN', window.location.origin)
    const adapter = await import('../src/lib/shared-web-session')
    const clear = vi.fn()
    bridge.logoutSharedSession.mockRejectedValue(new Error('unavailable'))
    await expect(adapter.logoutMainSharedSession(clear)).rejects.toThrow('unavailable')
    expect(clear).not.toHaveBeenCalled()
  })

  it('logout guard is cancellation-only and ignored completely in default-off deployments', async () => {
    const adapter = await import('../src/lib/shared-web-session')
    localStorage.setItem('pocketbase_auth', '{"token":"legacy-late"}')
    localStorage.setItem('qatlas_shared_auth_guard_v1', '{"epoch":"logout","blocked":true}')
    expect(adapter.sharedMainAuthBlocked()).toBe(false)
    expect(adapter.currentMainSessionToken()).toBe('legacy-late')
    vi.stubEnv('VITE_SHARED_WEB_SESSION_ORIGIN', window.location.origin)
    expect(adapter.sharedMainAuthBlocked()).toBe(true)
    expect(adapter.currentMainSessionToken()).toBe('')
    expect(adapter.currentMainAuthSnapshot()).toEqual({ token: 'legacy-late', epoch: 'logout', blocked: true })
  })

  it('malformed opt-in cancellation state fails closed without deleting main auth', async () => {
    vi.stubEnv('VITE_SHARED_WEB_SESSION_ORIGIN', window.location.origin)
    const adapter = await import('../src/lib/shared-web-session')
    localStorage.setItem('pocketbase_auth', '{"token":"synthetic"}')
    localStorage.setItem('qatlas_shared_auth_guard_v1', '{bad')
    expect(adapter.sharedMainAuthBlocked()).toBe(true)
    expect(localStorage.getItem('pocketbase_auth')).toBe('{"token":"synthetic"}')
  })

  it('only fixed bootstrap path opts into document navigation, feature default off', async () => {
    const adapter = await import('../src/lib/shared-web-session')
    expect(adapter.isSharedSessionBootstrap('/pursuit-session/')).toBe(false)
    vi.stubEnv('VITE_SHARED_WEB_SESSION_ORIGIN', window.location.origin)
    expect(adapter.isSharedSessionBootstrap('/pursuit-session/')).toBe(true)
    for (const path of ['/pursuit-session/logout', '//evil.example/', '/en/papers', '/pursuit-session/?next=evil']) {
      expect(adapter.isSharedSessionBootstrap(path)).toBe(false)
    }
  })
})
