// detectLang drives the `/` entry redirect: stored i18next choice
// (localStorage 'qatlas_lang') wins over the navigator, default is zh.
// This is the read side of the language-switcher persistence chain
// (switcher → URL → i18n.changeLanguage → detector cache).
import { afterEach, describe, expect, it } from 'vitest'

import { detectLang } from '@/lib/lang-detect'

function setNavigatorLanguage(value: string) {
  Object.defineProperty(window.navigator, 'language', { value, configurable: true })
}

describe('detectLang priority order', () => {
  afterEach(() => {
    window.localStorage.clear()
    setNavigatorLanguage('en-US')
  })

  it('returns the stored choice regardless of the navigator language', () => {
    setNavigatorLanguage('zh-CN')
    window.localStorage.setItem('qatlas_lang', 'en')
    expect(detectLang()).toBe('en')

    setNavigatorLanguage('en-US')
    window.localStorage.setItem('qatlas_lang', 'zh')
    expect(detectLang()).toBe('zh')
  })

  it('falls back to the navigator language when nothing is stored', () => {
    setNavigatorLanguage('en-US')
    expect(detectLang()).toBe('en')

    setNavigatorLanguage('en-GB')
    expect(detectLang()).toBe('en')
  })

  it('defaults to zh for non-en navigators and ignores garbage storage', () => {
    setNavigatorLanguage('zh-CN')
    expect(detectLang()).toBe('zh')

    setNavigatorLanguage('fr-FR')
    window.localStorage.setItem('qatlas_lang', 'klingon')
    expect(detectLang()).toBe('zh')
  })
})
