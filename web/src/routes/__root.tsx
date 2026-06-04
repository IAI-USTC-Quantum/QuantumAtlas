import { Outlet, createRootRoute, useNavigate, useRouterState } from '@tanstack/react-router'
import { useEffect } from 'react'
import { Loader2 } from 'lucide-react'
import { useTranslation } from 'react-i18next'

import { ensureAuthBootstrap, useAuth } from '@/lib/auth'

export const Route = createRootRoute({
  component: RootLayout,
  notFoundComponent: NotFoundView,
})

// Routes that don't require an authenticated session. These live at the
// top level (no language prefix) because the OAuth flow happens before
// we know the user's language preference, and bookmarks / external
// redirects (e.g. GitHub callback) cannot encode a language.
const ANON_ROUTES = new Set(['/login', '/auth/callback'])

function RootLayout() {
  const { t } = useTranslation('auth')
  const auth = useAuth()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  // `href` captures pathname + search + hash so that gated routes like
  // `/<lang>/pat?cli_callback=…&cli_state=…` (the qatlas CLI's
  // loopback-callback hand-off) survive the redirect through /login
  // and the GitHub OAuth round trip. Without this the search part is
  // dropped when we send the user to the login screen, breaking the
  // CLI flow silently.
  const href = useRouterState({ select: (state) => state.location.href })
  const onAnonRoute = ANON_ROUTES.has(pathname)

  useEffect(() => {
    ensureAuthBootstrap()
  }, [])

  useEffect(() => {
    if (auth.isChecking) return
    if (!auth.isAuthed && !onAnonRoute) {
      navigate({
        to: '/login',
        search: { from: href === '/' ? undefined : href },
      })
    }
  }, [auth.isChecking, auth.isAuthed, onAnonRoute, navigate, href])

  if (auth.isChecking) {
    return (
      <div className="flex min-h-svh flex-col items-center justify-center gap-3 text-muted-foreground">
        <Loader2 className="size-7 animate-spin" />
        <p className="text-sm">{t('restoring')}</p>
      </div>
    )
  }

  if (!auth.isAuthed && !onAnonRoute) {
    return null
  }

  // Both the anonymous routes and the localized $lang layout route render
  // their own outer chrome. The root layout is intentionally chrome-free
  // beyond the auth gate; the application shell (Sidebar + Topbar) lives
  // in `routes/$lang.tsx` so it can see the active language.
  return <Outlet />
}

function NotFoundView() {
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-2 px-6 text-center">
      <p className="text-xs uppercase tracking-[0.18em] text-muted-foreground">404</p>
      <h1 className="text-2xl font-semibold">Page not found</h1>
      <p className="max-w-md text-sm text-muted-foreground">
        This route is not part of the web workspace.
      </p>
    </div>
  )
}
