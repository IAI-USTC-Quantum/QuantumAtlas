import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { Github, Loader2, Sparkles } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardFooter,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { loginWithGitHub, useAuth } from '@/lib/auth'
import { safeRedirect } from '@/lib/safe-redirect'

type LoginSearch = { from?: string }

export const Route = createFileRoute('/login')({
  component: LoginPage,
  validateSearch: (search: Record<string, unknown>): LoginSearch => {
    // Same-origin gate at the search-validation layer too, so we never
    // even round-trip a hostile `?from=` through the component (defence
    // in depth — the component also calls safeRedirect before
    // window.location.assign).
    if (typeof search.from !== 'string') return {}
    return { from: safeRedirect(search.from, '/') === '/' ? undefined : search.from }
  },
})

function LoginPage() {
  const { t } = useTranslation('login')
  const auth = useAuth()
  const navigate = useNavigate()
  const search = Route.useSearch()
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<string>('')

  useEffect(() => {
    if (auth.isAuthed) {
      // safeRedirect collapses anything cross-origin / malformed to "/"
      // so a hostile ?from=//evil.com cannot phishing-jump after login.
      const dest = safeRedirect(search.from)
      // `dest` may carry search params (e.g. /<lang>/pat?cli_callback=…
      // when the qatlas CLI's loopback flow bounces the user through
      // a fresh sign-in). TanStack Router's `navigate({to})` only
      // accepts route ids — passing a raw URL with `?…` would either
      // fail typing or strip the search part. Fall back to a hard
      // navigation in that case so the SPA reloads with the full URL.
      if (dest.includes('?') || dest.includes('#')) {
        window.location.assign(dest)
      } else {
        navigate({ to: dest })
      }
    }
  }, [auth.isAuthed, navigate, search.from])

  async function handleLogin() {
    setBusy(true)
    setError('')
    try {
      // On success this navigates the whole tab to github.com; the promise
      // never resolves from this page's perspective because the document is
      // torn down. We only land in catch if the provider lookup fails
      // synchronously (network / config error).
      await loginWithGitHub(search.from)
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setError(message || t('failed'))
      setBusy(false)
    }
  }

  return (
    <div className="flex min-h-svh items-center justify-center bg-gradient-to-br from-primary/10 via-background to-accent/30 p-6">
      <Card className="w-full max-w-md">
        <CardHeader className="items-center text-center">
          <span className="mb-2 flex size-14 items-center justify-center rounded-2xl bg-primary/15 text-primary">
            <Sparkles className="size-7" />
          </span>
          <CardTitle className="text-2xl">{t('title')}</CardTitle>
          <CardDescription>{t('subtitle')}</CardDescription>
        </CardHeader>
        <CardContent className="space-y-4">
          <Button
            type="button"
            size="lg"
            className="w-full"
            disabled={busy}
            onClick={handleLogin}
          >
            {busy ? (
              <Loader2 className="size-4 animate-spin" />
            ) : (
              <Github className="size-4" />
            )}
            {busy ? t('redirecting') : t('github')}
          </Button>
          {error && (
            <Alert variant="destructive">
              <AlertTitle>{t('failed')}</AlertTitle>
              <AlertDescription>{error}</AlertDescription>
            </Alert>
          )}
        </CardContent>
        <CardFooter>
          <p className="text-center text-xs text-muted-foreground">
            {t('footer')}
          </p>
        </CardFooter>
      </Card>
    </div>
  )
}

