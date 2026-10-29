import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  store: { token: 'existing-synthetic', record: { id: 'user-a' } as { id: string } | null, clear: vi.fn() },
  authRefresh: vi.fn(), exchange: vi.fn(), listAuthMethods: vi.fn(),
  sync: vi.fn(), logout: vi.fn(), toast: vi.fn(),
}))
vi.mock('../src/lib/pb', () => ({
  AUTH_COLLECTION: 'users',
  pb: { authStore: mocks.store, collection: vi.fn(() => ({
    authRefresh: mocks.authRefresh, authWithOAuth2Code: mocks.exchange,
    listAuthMethods: mocks.listAuthMethods,
  })) },
}))
vi.mock('../src/lib/shared-web-session', () => ({
  syncMainSharedSession: mocks.sync,
  logoutMainSharedSession: mocks.logout,
  SHARED_SESSION_ERROR: 'shared session failed; main login kept',
  sharedWebSessionEnabled: () => false,
  sharedMainAuthBlocked: () => false,
}))
vi.mock('sonner', () => ({ toast: { error: mocks.toast } }))

function pending(mode: 'login' | 'link' = 'login', from = '/pursuit-session/') {
  sessionStorage.setItem('qatlas_oauth_pending', JSON.stringify({
    provider: 'gitea', state: 'state', codeVerifier: 'verifier',
    redirectURL: `${window.location.origin}/auth/callback`, from, mode,
  }))
}
beforeEach(() => {
  vi.resetModules()
  vi.clearAllMocks()
  vi.stubEnv('VITE_DEV_FAKE_AUTH', '0')
  mocks.store.token = 'existing-synthetic'
  mocks.store.record = { id: 'user-a' }
  mocks.authRefresh.mockReset().mockResolvedValue(undefined)
  mocks.exchange.mockReset().mockResolvedValue(undefined)
  mocks.sync.mockReset().mockResolvedValue(undefined)
  mocks.logout.mockReset().mockImplementation(async (clear: () => void) => { clear() })
  mocks.store.clear.mockImplementation(() => { mocks.store.token = ''; mocks.store.record = null })
  sessionStorage.clear()
})
afterEach(() => vi.unstubAllEnvs())

describe('default-off auth behavior (mocked SDK, no FAKE_AUTH)', () => {
  it('syncs an existing login after successful server bootstrap, only once', async () => {
    const auth = await import('../src/lib/auth')
    await Promise.all([auth.ensureAuthBootstrap(), auth.ensureAuthBootstrap()])
    expect(mocks.authRefresh).toHaveBeenCalledOnce()
    expect(mocks.sync).toHaveBeenCalledOnce()
    expect(mocks.authRefresh.mock.invocationCallOrder[0]).toBeLessThan(mocks.sync.mock.invocationCallOrder[0])
    expect(mocks.store.clear).not.toHaveBeenCalled()
  })

  it('shared outage after bootstrap retains main auth and shows a visible error', async () => {
    mocks.sync.mockRejectedValue(new Error('issuer unavailable'))
    const auth = await import('../src/lib/auth')
    await expect(auth.ensureAuthBootstrap()).resolves.toBeUndefined()
    expect(mocks.store.token).toBe('existing-synthetic')
    expect(mocks.store.clear).not.toHaveBeenCalled()
    expect(mocks.toast).toHaveBeenCalledWith(expect.stringContaining('main login kept'))
  })

  it('rejected main bootstrap preserves original clear behavior, never syncs', async () => {
    mocks.authRefresh.mockRejectedValue(new Error('revoked'))
    const auth = await import('../src/lib/auth')
    await auth.ensureAuthBootstrap()
    expect(mocks.store.clear).toHaveBeenCalledOnce()
    expect(mocks.sync).not.toHaveBeenCalled()
  })

  it('no stored token does not invent a login or shared grant', async () => {
    mocks.store.token = ''
    const auth = await import('../src/lib/auth')
    await auth.ensureAuthBootstrap()
    expect(mocks.authRefresh).not.toHaveBeenCalled()
    expect(mocks.sync).not.toHaveBeenCalled()
  })

  it('OAuth success syncs and preserves original from and code exchange', async () => {
    pending()
    const auth = await import('../src/lib/auth')
    expect(await auth.completeOAuth2Login('code', 'state')).toEqual({ from: '/pursuit-session/', switchedAccount: false })
    expect(mocks.exchange).toHaveBeenCalledWith('gitea', 'code', 'verifier', `${window.location.origin}/auth/callback`)
    expect(mocks.sync).toHaveBeenCalledOnce()
    expect(mocks.exchange.mock.invocationCallOrder[0]).toBeLessThan(mocks.sync.mock.invocationCallOrder[0])
    expect(sessionStorage.getItem('qatlas_oauth_pending')).toBeNull()
  })

  it('OAuth shared outage retains main login and original successful result', async () => {
    pending()
    mocks.sync.mockRejectedValue(new Error('issuer unavailable'))
    const auth = await import('../src/lib/auth')
    expect((await auth.completeOAuth2Login('code', 'state')).from).toBe('/pursuit-session/')
    expect(mocks.store.clear).not.toHaveBeenCalled()
    expect(mocks.toast).toHaveBeenCalledOnce()
  })

  it.each([false, true])('link result and switched-account warning are unchanged (switch=%s)', async (switched) => {
    pending('link', '/en/dashboard')
    if (switched) mocks.exchange.mockImplementation(async () => { mocks.store.record = { id: 'user-b' } })
    const auth = await import('../src/lib/auth')
    expect(await auth.completeOAuth2Login('code', 'state')).toEqual({ from: '/en/dashboard', switchedAccount: switched })
    expect(sessionStorage.getItem(switched ? auth.BIND_SWITCHED_KEY : auth.BIND_DONE_KEY)).toBe('1')
    expect(sessionStorage.getItem(switched ? auth.BIND_DONE_KEY : auth.BIND_SWITCHED_KEY)).toBeNull()
    expect(mocks.sync).toHaveBeenCalledOnce()
  })

  it('link outcome stays bound to the exchange even if another tab changes auth during sync', async () => {
    pending('link', '/en/dashboard')
    mocks.sync.mockImplementation(async () => { mocks.store.record = { id: 'different-tab-user' } })
    const auth = await import('../src/lib/auth')
    expect(await auth.completeOAuth2Login('code', 'state')).toEqual({ from: '/en/dashboard', switchedAccount: false })
    expect(sessionStorage.getItem(auth.BIND_DONE_KEY)).toBe('1')
    expect(sessionStorage.getItem(auth.BIND_SWITCHED_KEY)).toBeNull()
  })

  it('OAuth conflict still carries original from, consumes pending, never syncs', async () => {
    pending('login', '/en/dashboard')
    mocks.exchange.mockRejectedValue({ data: { data: {
      code: 'oauth_conflict', provider: 'gitea', login: 'new-login', conflict: 'username', existing: 'existing-login',
    } } })
    const auth = await import('../src/lib/auth')
    await expect(auth.completeOAuth2Login('code', 'state')).rejects.toMatchObject({
      name: 'OAuthConflictError', info: { from: '/en/dashboard', conflict: 'username' },
    })
    expect(mocks.sync).not.toHaveBeenCalled()
    expect(sessionStorage.getItem('qatlas_oauth_pending')).toBeNull()
  })

  it('bad OAuth state never exchanges or syncs', async () => {
    pending()
    const auth = await import('../src/lib/auth')
    await expect(auth.completeOAuth2Login('code', 'wrong-state')).rejects.toThrow('state mismatch')
    expect(mocks.exchange).not.toHaveBeenCalled()
    expect(mocks.sync).not.toHaveBeenCalled()
  })

  it('logout waits for revocation before clearing, rejects without clearing on outage', async () => {
    let ack!: () => void
    mocks.logout.mockImplementation((clear: () => void) => new Promise<void>((resolve) => {
      ack = () => { clear(); resolve() }
    }))
    const auth = await import('../src/lib/auth')
    const logout = auth.logout()
    expect(mocks.store.clear).not.toHaveBeenCalled()
    ack()
    await logout
    expect(mocks.store.clear).toHaveBeenCalledOnce()
    mocks.store.clear.mockClear()
    mocks.logout.mockRejectedValue(new Error('revocation failed'))
    await expect(auth.logout()).rejects.toThrow('revocation failed')
    expect(mocks.store.clear).not.toHaveBeenCalled()
  })
})
