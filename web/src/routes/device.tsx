import { createFileRoute, redirect } from '@tanstack/react-router'

import { DEFAULT_LANG, isLang, type Lang } from '@/i18n'

// Bare `/device` always redirects to the localized `/<lang>/device`.
// The SPA device-approve page lives under the `$lang` layout so it
// gets the sidebar + i18n + auth gate for free. Several callers point
// users at the shorter `/device` form:
//   - the qatlas CLI device-flow output: "open https://<host>/device
//     and enter code WDJB-MJHT"
//   - the verification_uri field on /api/oauth/device/code responses
//     (RFC 8628 §3.2)
//   - docs / external bookmarks
// Without this redirect those URLs would land on the SPA shell with
// no matching route. Mirrors `pat.tsx`'s shape.
export const Route = createFileRoute('/device')({
  beforeLoad: () => {
    throw redirect({
      to: '/$lang/device',
      params: { lang: detectLang() },
      // Forward search (e.g. ?user_code=WDJB-MJHT from
      // verification_uri_complete) through the language redirect.
      search: (prev) => prev,
    })
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
