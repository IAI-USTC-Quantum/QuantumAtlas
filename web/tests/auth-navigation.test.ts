// Include generated route + router Register types without executing the app.
import type {} from '../src/main'
import { act, createElement, type PropsWithChildren } from 'react'
import { createRoot, type Root } from 'react-dom/client'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

const h = vi.hoisted(() => ({
  navigate: vi.fn(), logout: vi.fn(), toast: vi.fn(), complete: vi.fn(),
  search: {} as Record<string, string>,
  authed: false,
  enabled: true,
  routes: {} as Record<string, { component: () => unknown; validateSearch: (s: Record<string, unknown>) => unknown }>,
}))
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => h.navigate,
  Link: ({ children }: PropsWithChildren) => createElement('a', null, children),
  createFileRoute: (path: string) => (options: typeof h.routes[string]) => {
    h.routes[path] = options
    return { ...options, useSearch: () => h.search }
  },
}))
vi.mock('react-i18next', () => ({
  useTranslation: () => ({ t: (key: string) => key }),
  Trans: () => null,
}))
vi.mock('sonner', () => ({ toast: { error: h.toast } }))
vi.mock('@/lib/auth', () => ({
  logout: h.logout,
  useAuth: () => ({ isAuthed: h.authed, user: { name: 'test', email: 'test@example' } }),
  completeOAuth2Login: h.complete,
  listLoginProviders: async () => ['github', 'gitea'],
  loginWithGitHub: vi.fn(), loginWithGitea: vi.fn(), loginWithOAuth2: vi.fn(),
  OAuthConflictError: class extends Error {},
}))
vi.mock('@/lib/shared-web-session', () => ({
  SHARED_LOGOUT_ERROR: 'Sign-out was not confirmed; still signed in.',
  isSharedSessionBootstrap: (dest: string) => h.enabled && dest === '/pursuit-session/',
}))
vi.mock('@/hooks/use-lang', () => ({ useLang: () => 'en' }))
vi.mock('@/components/sidebar', () => ({ Sidebar: () => null }))
vi.mock('@/components/language-switcher', () => ({ LanguageSwitcher: () => null }))
vi.mock('@/components/theme-toggle', () => ({ ThemeToggle: () => null }))
function wrap({ children }: PropsWithChildren) { return createElement('div', null, children) }
vi.mock('@/components/ui/dropdown-menu', () => ({
  DropdownMenu: wrap, DropdownMenuContent: wrap, DropdownMenuTrigger: wrap,
  DropdownMenuLabel: wrap, DropdownMenuSeparator: () => null,
  DropdownMenuItem: ({ children, disabled, onSelect }: PropsWithChildren<{
    disabled?: boolean; onSelect?: (event: { preventDefault(): void }) => void
  }>) => createElement('button', { disabled, onClick: () => onSelect?.({ preventDefault() {} }) }, children),
}))

let root: Root
let element: HTMLDivElement
beforeEach(() => {
  vi.clearAllMocks()
  vi.stubGlobal('IS_REACT_ACT_ENVIRONMENT', true)
  h.authed = false
  h.enabled = true
  h.search = {}
  h.logout.mockReset().mockResolvedValue(undefined)
  h.complete.mockReset().mockResolvedValue({ from: '/pursuit-session/' })
  element = document.createElement('div')
  document.body.append(element)
  root = createRoot(element)
})
afterEach(async () => {
  await act(async () => root.unmount())
  element.remove()
  vi.unstubAllGlobals()
})
async function render(component: () => unknown) {
  await act(async () => root.render(createElement(component as () => ReturnType<typeof createElement>)))
}

describe('Topbar async sign-out', () => {
  it('does not navigate until revocation resolves; repeated click is disabled', async () => {
    let ack!: () => void
    h.logout.mockImplementation(() => new Promise<void>((resolve) => { ack = resolve }))
    const { Topbar } = await import('../src/components/topbar')
    await render(Topbar)
    const button = Array.from(element.querySelectorAll('button')).find((b) => b.textContent === 'topbar.userMenu.signOut')!
    await act(async () => { button.click() })
    expect(h.logout).toHaveBeenCalledOnce()
    expect(h.navigate).not.toHaveBeenCalled()
    expect(button.disabled).toBe(true)
    await act(async () => { button.click() })
    expect(h.logout).toHaveBeenCalledOnce()
    await act(async () => { ack() })
    expect(h.navigate).toHaveBeenCalledWith({ to: '/login' })
    expect(h.toast).not.toHaveBeenCalled()
  })

  it('failed revocation leaves current page and displays explicit error', async () => {
    h.logout.mockRejectedValue(new Error('unavailable'))
    const { Topbar } = await import('../src/components/topbar')
    await render(Topbar)
    const button = Array.from(element.querySelectorAll('button')).find((b) => b.textContent === 'topbar.userMenu.signOut')!
    await act(async () => { button.click() })
    expect(h.navigate).not.toHaveBeenCalled()
    expect(h.toast).toHaveBeenCalledWith(expect.stringContaining('still signed in'))
    expect(button.disabled).toBe(false)
  })
})

describe('fixed bootstrap return through real Login/AuthCallback components', () => {
  // jsdom makes Location methods unforgeable. Install a same-origin window
  // facade only for the component tests; no browser or OAuth network is used.
  function spyLocation() {
    const assign = vi.fn(), replace = vi.fn()
    vi.stubGlobal('window', {
      location: { origin: 'https://main.example', assign, replace },
      matchMedia: () => ({ matches: false, addEventListener() {}, removeEventListener() {} }),
    })
    return { assign, replace }
  }

  it('OAuth callback document-loads backend bootstrap rather than SPA routing it', async () => {
    const location = spyLocation()
    h.search = { code: 'synthetic-code', state: 'synthetic-state' }
    await import('../src/routes/auth.callback')
    await render(h.routes['/auth/callback'].component)
    expect(h.complete).toHaveBeenCalledWith('synthetic-code', 'synthetic-state')
    expect(location.replace).toHaveBeenCalledWith('/pursuit-session/')
    expect(h.navigate).not.toHaveBeenCalled()
  })

  it('already-authed login also document-loads the fixed bootstrap return', async () => {
    const location = spyLocation()
    h.search = { from: '/pursuit-session/' }
    h.authed = true
    await import('../src/routes/login')
    await render(h.routes['/login'].component)
    expect(location.assign).toHaveBeenCalledWith('/pursuit-session/')
    expect(h.navigate).not.toHaveBeenCalled()
  })

  it('normal callback destination retains SPA navigation', async () => {
    const location = spyLocation()
    h.search = { code: 'code', state: 'state' }
    h.complete.mockResolvedValue({ from: '/en/dashboard' })
    await import('../src/routes/auth.callback')
    await render(h.routes['/auth/callback'].component)
    expect(h.navigate).toHaveBeenCalledWith({ to: '/en/dashboard', replace: true })
    expect(location.replace).not.toHaveBeenCalled()
  })

  it('hostile callback from is still collapsed to main root', async () => {
    const location = spyLocation()
    h.search = { code: 'code', state: 'state' }
    h.complete.mockResolvedValue({ from: '//evil.example/' })
    await import('../src/routes/auth.callback')
    await render(h.routes['/auth/callback'].component)
    expect(h.navigate).toHaveBeenCalledWith({ to: '/', replace: true })
    expect(location.replace).not.toHaveBeenCalled()
  })

  it('Login search validation keeps real from and rejects cross-origin returns', async () => {
    spyLocation()
    await import('../src/routes/login')
    expect(h.routes['/login'].validateSearch({ from: '/pursuit-session/' })).toEqual({ from: '/pursuit-session/' })
    expect(h.routes['/login'].validateSearch({ from: '//evil.example/', match: 'gitea' })).toEqual({ match: 'gitea' })
  })

  it('default-off callback behavior remains unchanged', async () => {
    const location = spyLocation()
    h.enabled = false
    h.search = { code: 'code', state: 'state' }
    await import('../src/routes/auth.callback')
    await render(h.routes['/auth/callback'].component)
    expect(h.navigate).toHaveBeenCalledWith({ to: '/pursuit-session/', replace: true })
    expect(location.replace).not.toHaveBeenCalled()
  })
})
