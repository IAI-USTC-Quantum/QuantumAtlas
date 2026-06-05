import { Link, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  BookOpen,
  FileSearch,
  Home,
  Key,
  Search,
  Sparkles,
  type LucideIcon,
} from 'lucide-react'

import { useLang } from '@/hooks/use-lang'
import { cn } from '@/lib/utils'

type NavLink = {
  to:
    | '/$lang'
    | '/$lang/wiki'
    | '/$lang/wiki/search'
    | '/$lang/papers/search'
    | '/$lang/graph'
    | '/$lang/pat'
  labelKey: string
  icon: LucideIcon
  /**
   * If set, the link is "active" when the current pathname starts with
   * `/{lang}{matchPrefix}`. If unset, the link is active only when the
   * pathname matches exactly.
   */
  matchPrefix?: string
}

// Graph is intentionally omitted from nav until it's ready; the route file
// and /api/graph backend are kept so it can be re-enabled later.
const links: NavLink[] = [
  { to: '/$lang', labelKey: 'nav.home', icon: Home },
  { to: '/$lang/wiki', labelKey: 'nav.wiki', icon: BookOpen, matchPrefix: '/wiki' },
  { to: '/$lang/wiki/search', labelKey: 'nav.search', icon: Search },
  { to: '/$lang/papers/search', labelKey: 'nav.papers', icon: FileSearch, matchPrefix: '/papers' },
  { to: '/$lang/pat', labelKey: 'nav.pat', icon: Key },
]

export function Sidebar({
  className,
  onNavigate,
}: {
  className?: string
  /** Fired after a nav link is clicked. Used by the mobile Sheet wrapper to close itself. */
  onNavigate?: () => void
}) {
  const { t } = useTranslation('common')
  const lang = useLang()
  const pathname = useRouterState({ select: (state) => state.location.pathname })
  const homePath = `/${lang}`

  return (
    <aside
      className={cn(
        'flex w-60 shrink-0 flex-col gap-6 border-r border-sidebar-border bg-sidebar px-4 py-6 text-sidebar-foreground',
        className,
      )}
    >
      <Link
        to="/$lang"
        params={{ lang }}
        onClick={onNavigate}
        className="flex items-center gap-3 px-2 transition-colors hover:text-sidebar-primary"
      >
        <span className="flex size-9 items-center justify-center rounded-lg bg-sidebar-primary/15 text-sidebar-primary">
          <Sparkles className="size-5" />
        </span>
        <span className="flex flex-col leading-tight">
          <strong className="text-sm font-semibold">{t('brand')}</strong>
          <small className="text-xs text-muted-foreground">{t('brandTagline')}</small>
        </span>
      </Link>
      <nav className="flex flex-1 flex-col gap-0.5">
        {links.map((link) => {
          const Icon = link.icon
          const targetPath = link.to === '/$lang'
            ? homePath
            : `${homePath}${link.to.slice('/$lang'.length)}`
          const active = link.matchPrefix
            ? pathname.startsWith(`${homePath}${link.matchPrefix}`)
            : pathname === targetPath
          return (
            <Link
              key={link.to}
              to={link.to}
              params={{ lang }}
              onClick={onNavigate}
              className={cn(
                'flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors',
                'hover:bg-sidebar-accent hover:text-sidebar-accent-foreground',
                'focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-sidebar-ring/50',
                active
                  ? 'bg-sidebar-accent text-sidebar-accent-foreground'
                  : 'text-sidebar-foreground/80',
              )}
            >
              <Icon className="size-4 shrink-0" />
              {t(link.labelKey)}
            </Link>
          )
        })}
      </nav>
    </aside>
  )
}
