import { Outlet, createRootRoute, useNavigate, useRouterState } from '@tanstack/react-router'
import { useEffect } from 'react'
import { Loader2 } from 'lucide-react'
import { Sidebar } from '@/components/Sidebar'
import { Topbar } from '@/components/Topbar'
import { ensureAuthBootstrap, useAuth } from '@/lib/auth'

export const Route = createRootRoute({
  component: RootLayout,
  notFoundComponent: NotFoundView,
})

const ANON_ROUTES = new Set(['/login', '/auth/callback'])

function RootLayout() {
  const auth = useAuth()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const onAnonRoute = ANON_ROUTES.has(pathname)

  useEffect(() => {
    ensureAuthBootstrap()
  }, [])

  useEffect(() => {
    if (auth.isChecking) return
    if (!auth.isAuthed && !onAnonRoute) {
      navigate({
        to: '/login',
        search: { from: pathname === '/' ? undefined : pathname },
      })
    }
  }, [auth.isChecking, auth.isAuthed, onAnonRoute, navigate, pathname])

  if (auth.isChecking) {
    return (
      <div className="auth-bootstrap">
        <Loader2 className="spin" size={28} />
        <p>Restoring your session…</p>
      </div>
    )
  }

  if (!auth.isAuthed && !onAnonRoute) {
    return null
  }

  if (onAnonRoute) {
    return <Outlet />
  }

  return (
    <div className="app-shell">
      <Sidebar />
      <main className="app-main">
        <Topbar />
        <div className="page-frame">
          <Outlet />
        </div>
      </main>
    </div>
  )
}

function NotFoundView() {
  return (
    <div className="app-shell">
      <Sidebar />
      <main className="app-main">
        <Topbar />
        <div className="page-frame">
          <header className="page-header">
            <p className="eyebrow">404</p>
            <h1>Page not found</h1>
            <p>This route is not part of the web workspace.</p>
          </header>
        </div>
      </main>
    </div>
  )
}
