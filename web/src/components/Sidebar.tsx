import { Link, useRouterState } from '@tanstack/react-router'
import { BookOpen, Home, KeyRound, Network, Search, Sparkles, type LucideIcon } from 'lucide-react'

type NavLink = {
  to: string
  label: string
  icon: LucideIcon
  /** A link is active if the current pathname equals or starts with this prefix. */
  matchPrefix?: string
}

const links: NavLink[] = [
  { to: '/', label: 'Home', icon: Home },
  { to: '/wiki', label: 'Wiki', icon: BookOpen, matchPrefix: '/wiki' },
  { to: '/wiki/search', label: 'Search', icon: Search },
  { to: '/graph', label: 'Graph', icon: Network, matchPrefix: '/graph' },
  { to: '/token', label: 'Token', icon: KeyRound },
]

export function Sidebar() {
  const pathname = useRouterState({ select: (state) => state.location.pathname })

  return (
    <aside className="sidebar">
      <Link to="/" className="brand">
        <span className="brand-mark"><Sparkles size={20} /></span>
        <span>
          <strong>QuantumAtlas</strong>
          <small>Knowledge workbench</small>
        </span>
      </Link>
      <nav>
        {links.map((link) => {
          const Icon = link.icon
          const active = link.to === '/'
            ? pathname === '/'
            : (link.matchPrefix ? pathname.startsWith(link.matchPrefix) : pathname === link.to)
          return (
            <Link
              key={link.to}
              to={link.to}
              className={active ? 'active' : ''}
            >
              <Icon size={18} />
              {link.label}
            </Link>
          )
        })}
      </nav>
    </aside>
  )
}
