import { createFileRoute, redirect } from '@tanstack/react-router'

import { DEFAULT_LANG, isLang, type Lang } from '@/i18n'

// Root `/` does nothing visible — it always redirects to a localized URL.
// Detection order mirrors `i18n/index.ts`: previously stored choice
// (`localStorage.qatlas_lang`) → browser navigator → DEFAULT_LANG.
//
// The redirect happens in `beforeLoad` so it fires before the component
// is created; the user only ever sees the destination URL in their
// address bar, never `/`.
export const Route = createFileRoute('/')({
  beforeLoad: () => {
    throw redirect({ to: '/$lang', params: { lang: detectLang() } })
  },
})

function detectLang(): Lang {
  if (typeof window === 'undefined') return DEFAULT_LANG
  try {
    const stored = window.localStorage.getItem('qatlas_lang')
    if (isLang(stored ?? undefined)) return stored as Lang
  } catch {
    // localStorage can throw in private mode / sandboxed iframes; fall
    // through to the navigator probe.
  }
  const nav = window.navigator.language?.toLowerCase() ?? ''
  if (nav.startsWith('en')) return 'en'
  return DEFAULT_LANG
}
