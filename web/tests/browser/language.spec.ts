import { expect, test, READER_PAPER_ID } from './fixtures'

// Language-switcher regression (Q4 usability): a prominent zh/en switch
// must exist in the topbar on every localized page (the reader included),
// and switching must swap the URL :lang segment, the document language, and
// persist through i18next's localStorage cache ('qatlas_lang'). The read
// side of that cache (the `/` entry redirect) is covered by the
// detectLang unit tests (tests/lang-detect.test.ts); the shared fixtures'
// init script rewrites localStorage on every navigation, so a cross-reload
// assertion would test the fixture, not the app.
test('switching language on the reader page swaps URL, DOM, and persists the choice', async ({ page }) => {
  await page.goto(`/zh/papers/${READER_PAPER_ID}`)
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })

  // Prominent entry: the globe button in the topbar (aria-label from i18n).
  await page.getByRole('button', { name: '语言' }).click()
  await page.getByRole('menuitem', { name: 'English' }).click()

  // URL swaps the :lang segment, keeping the paper path and search state.
  await expect(page).toHaveURL(new RegExp(`/en/papers/${READER_PAPER_ID}(\\?|$)`))
  await expect(page.locator('html')).toHaveAttribute('lang', 'en')

  // The reader still renders after the language remount, now localized.
  await expect(page.locator('canvas[data-rendered="1"]')).toBeAttached({ timeout: 15_000 })

  // Persistence: i18next-browser-languagedetector caches to localStorage.
  expect(await page.evaluate(() => localStorage.getItem('qatlas_lang'))).toBe('en')

  // And back to zh via the (now English-labeled) switcher.
  await page.getByRole('button', { name: 'Language' }).click()
  await page.getByRole('menuitem', { name: '中文' }).click()
  await expect(page).toHaveURL(new RegExp(`/zh/papers/${READER_PAPER_ID}(\\?|$)`))
  await expect(page.locator('html')).toHaveAttribute('lang', 'zh')
  expect(await page.evaluate(() => localStorage.getItem('qatlas_lang'))).toBe('zh')
})
