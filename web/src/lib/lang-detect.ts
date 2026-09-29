// Language detection for the `/` entry redirect (src/routes/index.tsx).
//
// Extracted as a leaf module so the priority order — stored choice
// (`localStorage.qatlas_lang`, the key i18next-browser-languagedetector
// caches to, see src/i18n/index.ts) → browser navigator → DEFAULT_LANG —
// stays unit-testable without pulling the route tree into the test.
import { DEFAULT_LANG, isLang, type Lang } from '@/i18n'

export function detectLang(): Lang {
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
