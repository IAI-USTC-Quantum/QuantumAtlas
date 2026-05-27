import { useNavigate } from '@tanstack/react-router'
import { ChevronRight, LogOut, Search, UserCircle2 } from 'lucide-react'
import { logout, useAuth } from '@/lib/auth'

export function Topbar() {
  const navigate = useNavigate()
  const auth = useAuth()

  function handleLogout() {
    logout()
    navigate({ to: '/login' })
  }

  return (
    <header className="topbar">
      <form
        className="global-search"
        onSubmit={(event) => {
          event.preventDefault()
          const form = new FormData(event.currentTarget)
          const q = String(form.get('q') ?? '').trim()
          navigate({ to: '/wiki/search', search: q ? { q } : {} })
        }}
      >
        <Search size={17} />
        <input name="q" placeholder="Search pages, primitives, algorithms" />
      </form>
      <a className="docs-link" href="/api/docs">
        API docs
        <ChevronRight size={16} />
      </a>
      <div className="auth-chip" title={auth.user?.email ?? ''}>
        <UserCircle2 size={18} />
        <span>{auth.user?.name || auth.user?.username || auth.user?.email || 'signed in'}</span>
        <button type="button" className="ghost small" onClick={handleLogout} title="Sign out">
          <LogOut size={14} />
        </button>
      </div>
    </header>
  )
}
