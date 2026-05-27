import { Outlet, createRootRoute, useNavigate, useRouterState } from '@tanstack/react-router'
import { useEffect } from 'react'
import { Sidebar } from '@/components/Sidebar'
import { Topbar } from '@/components/Topbar'
import { useAuth } from '@/lib/auth'

export const Route = createRootRoute({
  component: RootLayout,
  notFoundComponent: NotFoundView,
})

function RootLayout() {
  const auth = useAuth()
  const navigate = useNavigate()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const onLoginRoute = pathname === '/login'

  useEffect(() => {
    if (!auth.isAuthed && !onLoginRoute) {
      navigate({
        to: '/login',
        search: { from: pathname === '/' ? undefined : pathname },
      })
    }
  }, [auth.isAuthed, onLoginRoute, navigate, pathname])

  if (!auth.isAuthed && !onLoginRoute) {
    return null
  }

  if (onLoginRoute) {
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
