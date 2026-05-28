import { Outlet, createFileRoute, redirect } from '@tanstack/react-router'
import { useEffect } from 'react'
import { useTranslation } from 'react-i18next'

import { isLang } from '@/i18n'
import { Sidebar } from '@/components/sidebar'
import { Topbar } from '@/components/topbar'

// `$lang` is the layout route for every localized page. Every URL under
// the application shell looks like `/{zh|en}/<rest>`. We validate the
// `lang` segment in `beforeLoad`; an unknown value redirects to `/zh/`
// rather than rendering a 404, so a stray `/de/wiki` lands somewhere
// useful instead of failing silently.
export const Route = createFileRoute('/$lang')({
  beforeLoad: ({ params }) => {
    if (!isLang(params.lang)) {
      throw redirect({ to: '/$lang', params: { lang: 'zh' } })
    }
  },
  component: LangLayout,
})

function LangLayout() {
  const { lang } = Route.useParams()
  const { i18n } = useTranslation()

  // Sync the i18next runtime language to the URL. The URL is the source
  // of truth (agent-friendly, link-shareable, bookmark-stable); the
  // local detector is only consulted once at the `/` redirect.
  useEffect(() => {
    if (i18n.language !== lang) {
      void i18n.changeLanguage(lang)
    }
    if (typeof document !== 'undefined') {
      document.documentElement.lang = lang
    }
  }, [lang, i18n])

  return (
    <div className="flex min-h-svh w-full bg-background text-foreground">
      <Sidebar className="hidden lg:flex" />
      <div className="flex min-w-0 flex-1 flex-col">
        <Topbar />
        <main className="flex-1 overflow-x-hidden">
          <div className="mx-auto w-full max-w-6xl px-4 py-6 sm:px-6 lg:px-8 lg:py-10">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  )
}
