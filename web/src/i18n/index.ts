// i18n bootstrap.
//
// We use react-i18next with URL-prefix routing (`/zh/*` and `/en/*`).
// The URL prefix is authoritative: a `<Lang />` layout route reads the
// `:lang` route param and calls `i18n.changeLanguage(lang)` on every
// match, so the active language is always whatever the URL says. This
// is intentional — agents / bots / link sharing should never depend on
// localStorage state to land on the correct language.
//
// `i18next-browser-languagedetector` only kicks in for the *initial*
// landing on `/` to pick where to redirect (zh by default, en if the
// detected language starts with "en"). After that the URL takes over.

import i18n from 'i18next'
import LanguageDetector from 'i18next-browser-languagedetector'
import { initReactI18next } from 'react-i18next'

import en from './locales/en.json'
import zh from './locales/zh.json'

export const SUPPORTED_LANGS = ['zh', 'en'] as const
export type Lang = (typeof SUPPORTED_LANGS)[number]
export const DEFAULT_LANG: Lang = 'zh'

export function isLang(value: string | undefined): value is Lang {
  return value === 'zh' || value === 'en'
}

void i18n
  .use(LanguageDetector)
  .use(initReactI18next)
  .init({
    resources: {
      en: en as Record<string, Record<string, unknown>>,
      zh: zh as Record<string, Record<string, unknown>>,
    },
    fallbackLng: DEFAULT_LANG,
    supportedLngs: [...SUPPORTED_LANGS],
    defaultNS: 'common',
    ns: ['common', 'home', 'wiki', 'graph', 'token', 'pat', 'login', 'auth'],
    interpolation: { escapeValue: false },
    detection: {
      order: ['path', 'localStorage', 'navigator'],
      lookupFromPathIndex: 0,
      caches: ['localStorage'],
      lookupLocalStorage: 'qatlas_lang',
    },
  })

export default i18n
