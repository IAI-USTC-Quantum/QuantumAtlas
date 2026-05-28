// Thin wrapper around `next-themes`' ThemeProvider so app code imports
// `@/components/theme-provider` and doesn't have to know which library
// powers it. `next-themes` despite its name is a pure React context
// provider that works in any framework — Vite, CRA, Remix, etc — and
// simply toggles a `class` on <html>, which is exactly what our
// shadcn-style `.dark` CSS variables expect.

import { ThemeProvider as NextThemesProvider } from 'next-themes'
import type { ComponentProps } from 'react'

export function ThemeProvider({
  children,
  ...props
}: ComponentProps<typeof NextThemesProvider>) {
  return (
    <NextThemesProvider
      attribute="class"
      defaultTheme="system"
      enableSystem
      disableTransitionOnChange
      storageKey="qatlas_theme"
      {...props}
    >
      {children}
    </NextThemesProvider>
  )
}

export { useTheme } from 'next-themes'
