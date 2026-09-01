import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { GitFork, Github, Info, Loader2, Sparkles } from 'lucide-react'

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
import {
  listLoginProviders,
  loginWithGitea,
  loginWithGitHub,
  loginWithOAuth2,
  useAuth,
  type OAuth2ProviderName,
} from '@/lib/auth'
import { safeRedirect } from '@/lib/safe-redirect'

type LoginSearch = { from?: string; match?: string }

export const Route = createFileRoute('/login')({
  component: LoginPage,
  validateSearch: (search: Record<string, unknown>): LoginSearch => {
    // Same-origin gate at the search-validation layer too, so we never
    // even round-trip a hostile `?from=` through the component (defence
    // in depth — the component also calls safeRedirect before
    // window.location.assign). `match` is the provider name the conflict
    // prompt forwarded: it only toggles a guidance banner, never a
    // redirect target.
    const out: LoginSearch = {}
    if (typeof search.from === 'string' && safeRedirect(search.from, '/') !== '/') {
      out.from = search.from
    }
    if (typeof search.match === 'string' && search.match) {
      out.match = search.match
    }
    return out
  },
})

// One button per OAuth2 provider the server exposes. The busy provider's
// button spins; the others stay clickable (the page is torn down on
// navigation anyway, so at most one flow can ever start).
const PROVIDER_BUTTONS: Record<
  OAuth2ProviderName,
  { icon: typeof Github; login: (from?: string) => Promise<void>; labelKey: string; redirectKey: string }
> = {
  github: { icon: Github, login: loginWithGitHub, labelKey: 'github', redirectKey: 'redirecting' },
  gitea: { icon: GitFork, login: loginWithGitea, labelKey: 'gitea', redirectKey: 'redirectingGitea' },
}

function LoginPage() {
  const { t } = useTranslation('login')
  const auth = useAuth()
  const navigate = useNavigate()
  const search = Route.useSearch()
  const [busy, setBusy] = useState<OAuth2ProviderName | null>(null)
  const [error, setError] = useState<string>('')
  // Providers enabled server-side; null until listAuthMethods resolves.
  // On fetch failure we fall back to the historic GitHub-only layout so
  // the page still renders a working sign-in affordance.
  const [providers, setProviders] = useState<string[] | null>(null)

  useEffect(() => {
    listLoginProviders()
      .then((names) => setProviders(names))
      .catch(() => setProviders(['github']))
  }, [])

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

  function handleLogin(provider: OAuth2ProviderName) {
    setBusy(provider)
    setError('')
    // On success this navigates the whole tab to the provider; the promise
    // never resolves from this page's perspective because the document is
    // torn down. We only land in catch if the provider lookup fails
    // synchronously (network / config error).
    loginWithOAuth2(provider, search.from).catch((e: unknown) => {
      const message = e instanceof Error ? e.message : String(e)
      setError(message || t('failed'))
      setBusy(null)
    })
  }

  // Render one entry per server-enabled provider we know how to draw.
  const buttons = (providers ?? []).filter(
    (name): name is OAuth2ProviderName => name in PROVIDER_BUTTONS,
  )

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
          {search.match && (
            // Arrived from the conflict prompt's "match" action: guide the
            // user through the two remaining steps instead of leaving them
            // to guess what "matching" means.
            <Alert className="text-start">
              <Info className="size-4" />
              <AlertTitle>{t('matchTitle')}</AlertTitle>
              <AlertDescription>
                {t('matchHint', { provider: search.match })}
              </AlertDescription>
            </Alert>
          )}
          {buttons.length === 0 ? (
            // listAuthMethods resolved but no known provider is enabled —
            // surface it instead of drawing a dead button.
            <Alert variant="destructive">
              <AlertTitle>{t('failed')}</AlertTitle>
              <AlertDescription>{t('noProviders')}</AlertDescription>
            </Alert>
          ) : (
            buttons.map((name) => {
              const { icon: Icon, labelKey, redirectKey } = PROVIDER_BUTTONS[name]
              const isBusy = busy === name
              return (
                <Button
                  key={name}
                  type="button"
                  size="lg"
                  variant={name === 'github' ? 'default' : 'outline'}
                  className="w-full"
                  disabled={busy !== null}
                  onClick={() => handleLogin(name)}
                >
                  {isBusy ? (
                    <Loader2 className="size-4 animate-spin" />
                  ) : (
                    <Icon className="size-4" />
                  )}
                  {isBusy ? t(redirectKey) : t(labelKey)}
                </Button>
              )
            })
          )}
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
