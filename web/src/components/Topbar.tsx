import { useNavigate } from '@tanstack/react-router'
import { ChevronRight, Search } from 'lucide-react'

export function Topbar() {
  const navigate = useNavigate()

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
    </header>
  )
}
