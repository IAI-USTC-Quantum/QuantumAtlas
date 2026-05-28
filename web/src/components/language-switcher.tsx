import { useTranslation } from 'react-i18next'
import { useLocation, useRouter } from '@tanstack/react-router'
import { Check, Globe } from 'lucide-react'

import { SUPPORTED_LANGS, type Lang } from '@/i18n'
import { useLang } from '@/hooks/use-lang'
import { Button } from '@/components/ui/button'
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuTrigger,
} from '@/components/ui/dropdown-menu'

/**
 * Language switcher that swaps the `:lang` URL segment while preserving
 * the rest of the path and the search string. Re-rendered routes will
 * see the new param and re-run their queries / translations.
 *
 * We use `router.navigate({ to: string })` with a type cast because the
 * destination is computed at runtime from `useLocation()` and can't be
 * proven correct against TSR's typed route tree.
 */
export function LanguageSwitcher() {
  const { t } = useTranslation('common')
  const current = useLang()
  const router = useRouter()
  const location = useLocation()

  function pathFor(target: Lang): string {
    const path = location.pathname
    if (/^\/(zh|en)(?=\/|$)/.test(path)) {
      return path.replace(/^\/(zh|en)/, `/${target}`)
    }
    return `/${target}${path === '/' ? '' : path}`
  }

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button variant="ghost" size="icon-sm" aria-label={t('language')}>
          <Globe className="size-4" />
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        {SUPPORTED_LANGS.map((lang) => (
          <DropdownMenuItem
            key={lang}
            onClick={() =>
              void router.navigate({
                to: pathFor(lang),
                search: (prev: Record<string, unknown>) => prev,
              } as unknown as Parameters<typeof router.navigate>[0])
            }
            className="flex items-center justify-between gap-6"
          >
            <span>{t(`languageNames.${lang}`)}</span>
            {current === lang && <Check className="size-4 text-primary" />}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
