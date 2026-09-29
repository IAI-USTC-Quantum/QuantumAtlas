import { createFileRoute, redirect } from '@tanstack/react-router'

import { detectLang } from '@/lib/lang-detect'

// Root `/` does nothing visible — it always redirects to a localized URL.
// Detection order mirrors `i18n/index.ts`: previously stored choice
// (`localStorage.qatlas_lang`) → browser navigator → default (zh).
//
// The redirect happens in `beforeLoad` so it fires before the component
// is created; the user only ever sees the destination URL in their
// address bar, never `/`.
export const Route = createFileRoute('/')({
  beforeLoad: () => {
    throw redirect({ to: '/$lang', params: { lang: detectLang() } })
  },
})
