import { createFileRoute, redirect } from '@tanstack/react-router'

import { DEFAULT_LANG, isLang, type Lang } from '@/i18n'

// Bare `/pat` always redirects to the localized `/<lang>/pat`. The SPA
// PAT page only exists under the `$lang` layout (so it gets the sidebar
// + i18n + auth gate for free), but several callers point users at the
// shorter `/pat` form:
//   - server 401 hint in internal/routes/auth.go ("mint a PAT at /pat")
//   - 403 hint in internal/routes/scope_guard.go ("mint a new PAT at /pat")
//   - swagger securityDefinitions description ("minted at `/pat` after ...")
//   - qatlas CLI `--token` help text (qatlas/client/_common.py)
//   - README / docs / external bookmarks
// Without this redirect those URLs would land on the SPA shell with no
// matching route. Mirroring routes/index.tsx's `/` → `/$lang` pattern
// keeps the destination URL consistent: detected language wins, with
// localStorage > navigator > DEFAULT_LANG fallback chain.
export const Route = createFileRoute('/pat')({
  beforeLoad: () => {
    throw redirect({
      to: '/$lang/pat',
      params: { lang: detectLang() },
      // Forward search params (e.g. cli_callback / cli_state from the
      // qatlas CLI loopback flow). Without this they're dropped on the
      // language redirect and the SPA never sees the CLI hand-off.
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
