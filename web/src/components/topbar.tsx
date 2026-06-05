import { useState } from 'react'
import { Link, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  ExternalLink,
  Key,
  LogOut,
  Menu,
  Search,
  UserCircle2,
} from 'lucide-react'

import { Sidebar } from '@/components/sidebar'
import { LanguageSwitcher } from '@/components/language-switcher'
import { ThemeToggle } from '@/components/theme-toggle'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'
import { Input } from '@/components/ui/input'
import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetTitle,
  SheetTrigger,
} from '@/components/ui/sheet'
import { useLang } from '@/hooks/use-lang'
import { logout, useAuth } from '@/lib/auth'

export function Topbar() {
  const { t } = useTranslation('common')
  const lang = useLang()
  const navigate = useNavigate()
  const auth = useAuth()
  const [mobileOpen, setMobileOpen] = useState(false)

  function handleLogout() {
    logout()
    navigate({ to: '/login' })
  }

  const identity =
    auth.user?.name || auth.user?.username || auth.user?.email || ''

  return (
    <header className="sticky top-0 z-30 flex h-14 items-center gap-2 border-b border-border bg-background/85 px-3 backdrop-blur sm:px-4 lg:px-6">
      <Sheet open={mobileOpen} onOpenChange={setMobileOpen}>
        <SheetTrigger asChild>
          <Button
            variant="ghost"
            size="icon-sm"
            className="lg:hidden"
            aria-label={t('nav.home')}
          >
            <Menu className="size-4" />
          </Button>
        </SheetTrigger>
        <SheetContent side="left" className="w-64 p-0">
          <SheetTitle className="sr-only">{t('brand')}</SheetTitle>
          <SheetDescription className="sr-only">
            {t('brandTagline')}
          </SheetDescription>
          <Sidebar className="h-full w-full border-r-0" onNavigate={() => setMobileOpen(false)} />
        </SheetContent>
      </Sheet>

      <form
        className="relative flex max-w-md flex-1 items-center"
        onSubmit={(event) => {
          event.preventDefault()
          const form = new FormData(event.currentTarget)
          const q = String(form.get('q') ?? '').trim()
          navigate({
            to: '/$lang/wiki/search',
            params: { lang },
            search: q ? { q } : {},
          })
        }}
      >
        <Search className="pointer-events-none absolute left-3 size-4 text-muted-foreground" />
        <Input
          name="q"
          type="search"
          placeholder={t('search.placeholder')}
          className="pl-9"
        />
      </form>

      <div className="ml-auto flex items-center gap-1">
        <Button
          variant="ghost"
          size="sm"
          asChild
          className="hidden text-xs text-muted-foreground sm:inline-flex"
        >
          <a href="/swagger/" target="_blank" rel="noreferrer">
            {t('topbar.docsLink')}
            <ExternalLink className="size-3.5" />
          </a>
        </Button>
        <LanguageSwitcher />
        <ThemeToggle />
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button
              variant="ghost"
              size="sm"
              aria-label={t('topbar.userMenu.openLabel')}
              className="gap-2"
            >
              <UserCircle2 className="size-4" />
              <span className="hidden max-w-[10rem] truncate sm:inline">
                {identity || t('status.active')}
              </span>
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="end" className="min-w-56">
            <DropdownMenuLabel className="flex flex-col gap-0.5">
              <span className="text-sm font-medium">{identity || '—'}</span>
              <span className="text-xs font-normal text-muted-foreground">
                {auth.user?.email ?? ''}
              </span>
            </DropdownMenuLabel>
            <DropdownMenuSeparator />
            <DropdownMenuItem asChild>
              <Link to="/$lang/pat" params={{ lang }}>
                <Key className="size-4" />
                {t('topbar.userMenu.patLink')}
              </Link>
            </DropdownMenuItem>
            <DropdownMenuSeparator />
            <DropdownMenuItem
              variant="destructive"
              onSelect={(event) => {
                event.preventDefault()
                handleLogout()
              }}
            >
              <LogOut className="size-4" />
              {t('topbar.userMenu.signOut')}
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>
      </div>
    </header>
  )
}
