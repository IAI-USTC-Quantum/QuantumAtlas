import { Outlet, createRootRoute } from '@tanstack/react-router'
import { Sidebar } from '@/components/Sidebar'
import { Topbar } from '@/components/Topbar'

export const Route = createRootRoute({
  component: RootLayout,
  notFoundComponent: NotFoundView,
})

function RootLayout() {
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
