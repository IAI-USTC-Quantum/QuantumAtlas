import { Link, useRouterState } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  Database,
  Download,
  FileSearch,
  FolderOpen,
  Home,
  Key,
  LayoutDashboard,
  Library,
  Sparkles,
  type LucideIcon,
} from 'lucide-react'

import { useLang } from '@/hooks/use-lang'
import { useAdminWhoami } from '@/lib/queries'
import { cn } from '@/lib/utils'

type NavLink = {
  to:
    | '/$lang'
    | '/$lang/dashboard'
    | '/$lang/papers/search'
    | '/$lang/papers'
    | '/$lang/downloader'
    | '/$lang/admin'
    | '/$lang/pat'
  labelKey: string
  icon: LucideIcon
  /**
   * If set, the link is "active" when the current pathname starts with
   * `/{lang}{matchPrefix}`. If unset, the link is active only when the
   * pathname matches exactly.
   */
  matchPrefix?: string
  /**
   * Suppresses matchPrefix hits under this sub-prefix. Lets /papers and
   * /papers/search coexist without both highlighting at once.
   */
  excludePrefix?: string
}

const links: NavLink[] = [
  { to: '/$lang', labelKey: 'nav.home', icon: Home },
  { to: '/$lang/dashboard', labelKey: 'nav.dashboard', icon: LayoutDashboard },
  { to: '/$lang/papers/search', labelKey: 'nav.papers', icon: FileSearch, matchPrefix: '/papers/search' },
  { to: '/$lang/papers', labelKey: 'nav.papersList', icon: Library, matchPrefix: '/papers', excludePrefix: '/papers/search' },
  { to: '/$lang/downloader', labelKey: 'nav.downloader', icon: Download },
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
  // Session-only whoami; cached for a few minutes in the query cache.
  // Hidden (not errored) for non-admins / non-sessions. is_user_admin
  // broadens the entry to DB-flag user managers (is_admin stays the env
  // allowlist gate for the ops dashboard itself).
  const whoami = useAdminWhoami()
  const showAdmin =
    whoami.data?.is_admin === true || whoami.data?.is_user_admin === true
  // The asset browser is adminGuard-ed (env allowlist), unlike the user
  // pages user-managers can reach — so only surface it for real admins.
  const showAdminAssets = whoami.data?.is_admin === true
  const adminPath = `${homePath}/admin`
  const adminAssetsPath = `${adminPath}/assets`

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
            ? pathname.startsWith(`${homePath}${link.matchPrefix}`) &&
              !(link.excludePrefix &&
                pathname.startsWith(`${homePath}${link.excludePrefix}`))
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
        {showAdmin && (
          <>
            <Link
              to="/$lang/admin"
              params={{ lang }}
              onClick={onNavigate}
              className={cn(
                'flex items-center gap-3 rounded-md px-3 py-2 text-sm font-medium transition-colors',
                'hover:bg-sidebar-accent hover:text-sidebar-accent-foreground',
                'focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-sidebar-ring/50',
                pathname.startsWith(adminPath)
                  ? 'bg-sidebar-accent text-sidebar-accent-foreground'
                  : 'text-sidebar-foreground/80',
              )}
            >
              <Database className="size-4 shrink-0" />
              {t('nav.admin')}
            </Link>
            {showAdminAssets && (
              <Link
                to="/$lang/admin/assets"
                params={{ lang }}
                onClick={onNavigate}
                className={cn(
                  'ml-5 flex items-center gap-3 rounded-md border-l border-sidebar-border px-3 py-1.5 text-[13px] font-medium transition-colors',
                  'hover:bg-sidebar-accent hover:text-sidebar-accent-foreground',
                  'focus-visible:outline-none focus-visible:ring-[3px] focus-visible:ring-sidebar-ring/50',
                  pathname.startsWith(adminAssetsPath)
                    ? 'bg-sidebar-accent text-sidebar-accent-foreground'
                    : 'text-sidebar-foreground/70',
                )}
              >
                <FolderOpen className="size-4 shrink-0" />
                {t('nav.adminAssets')}
              </Link>
            )}
          </>
        )}
      </nav>
    </aside>
  )
}
