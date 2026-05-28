import { useParams } from '@tanstack/react-router'
import { DEFAULT_LANG, isLang, type Lang } from '@/i18n'

/**
 * Resolves the active language from the `:lang` route param, falling back
 * to {@link DEFAULT_LANG} when the param is missing or malformed.
 *
 * `strict: false` lets us call this from any descendant of the `$lang`
 * layout route as well as from anonymous routes (login / callback), where
 * the param is absent and we just want the default.
 */
export function useLang(): Lang {
  const params = useParams({ strict: false }) as { lang?: string }
  return isLang(params.lang) ? params.lang : DEFAULT_LANG
}
