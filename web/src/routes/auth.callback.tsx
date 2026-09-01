import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { GitFork, Link2, Loader2 } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import {
  completeOAuth2Login,
  loginWithOAuth2,
  OAuthConflictError,
  type OAuth2ProviderName,
  type OAuthConflictInfo,
} from '@/lib/auth'
import { safeRedirect } from '@/lib/safe-redirect'

type CallbackSearch = {
  code?: string
  state?: string
  error?: string
  error_description?: string
}

export const Route = createFileRoute('/auth/callback')({
  component: AuthCallbackPage,
  validateSearch: (search: Record<string, unknown>): CallbackSearch => ({
    code: typeof search.code === 'string' ? search.code : undefined,
    state: typeof search.state === 'string' ? search.state : undefined,
    error: typeof search.error === 'string' ? search.error : undefined,
    error_description:
      typeof search.error_description === 'string'
        ? search.error_description
        : undefined,
  }),
})

function AuthCallbackPage() {
  const { t } = useTranslation('auth')
  const navigate = useNavigate()
  const search = Route.useSearch()
  const [error, setError] = useState<string>('')
  const [conflict, setConflict] = useState<OAuthConflictInfo | null>(null)
  const [retrying, setRetrying] = useState(false)
  // React 18 dev StrictMode mounts effects twice. The OAuth code is single-use
  // — a second call would fail with "invalid grant". Guard with a ref instead
  // of state so it survives the synchronous remount.
  const startedRef = useRef(false)

  useEffect(() => {
    if (startedRef.current) return
    startedRef.current = true

    if (search.error) {
      setError(search.error_description || search.error)
      return
    }
    if (!search.code || !search.state) {
      setError(t('missingCode'))
      return
    }

    completeOAuth2Login(search.code, search.state)
      .then(({ from }) => {
        // safeRedirect collapses anything cross-origin / malformed to "/"
        // so the post-login bounce never lands on an attacker host even
        // if `from` was tampered with in the state store.
        const dest = safeRedirect(from)
        // `dest` may carry search params after the CLI loopback flow —
        // see login.tsx for the same fallback. window.location.replace
        // is the safe path for URLs with `?…` / `#…`; the normal
        // TanStack navigate call covers static route ids.
        if (dest.includes('?') || dest.includes('#')) {
          window.location.replace(dest)
        } else {
          navigate({ to: dest, replace: true })
        }
      })
      .catch((e: unknown) => {
        if (e instanceof OAuthConflictError) {
          setConflict(e.info)
          return
        }
        const message = e instanceof Error ? e.message : String(e)
        setError(message || t('failedTitle'))
      })
  }, [
    navigate,
    search.code,
    search.state,
    search.error,
    search.error_description,
    t,
  ])

  // "保持独立账号" restarts the authorize round trip; the server recorded
  // the conflict offer in memory, so the retried exchange passes and
  // creates the separate account. Only meaningful for username conflicts —
  // a same-email sign-in can never be a separate record.
  function retryIndependent() {
    if (!conflict) return
    setRetrying(true)
    loginWithOAuth2(
      conflict.provider as OAuth2ProviderName,
      conflict.from ?? undefined,
    ).catch((e: unknown) => {
      setRetrying(false)
      const message = e instanceof Error ? e.message : String(e)
      setError(message || t('failedTitle'))
    })
  }

  if (conflict) {
    return (
      <div className="flex min-h-svh items-center justify-center bg-gradient-to-br from-primary/10 via-background to-accent/30 p-6">
        <Card className="w-full max-w-md">
          <CardHeader className="items-center text-center">
            <span className="mb-2 flex size-14 items-center justify-center rounded-2xl bg-primary/15 text-primary">
              <Link2 className="size-7" />
            </span>
            <CardTitle className="text-2xl">{t('conflict.title')}</CardTitle>
            <CardDescription>{t('conflict.subtitle')}</CardDescription>
          </CardHeader>
          <CardContent className="space-y-4">
            <Alert>
              <GitFork className="size-4" />
              <AlertTitle>
                {conflict.conflict === 'email'
                  ? t('conflict.emailTitle')
                  : t('conflict.usernameTitle')}
              </AlertTitle>
              <AlertDescription>
                {conflict.conflict === 'email'
                  ? t('conflict.emailDesc', {
                      login: conflict.login || conflict.provider,
                      existing: conflict.existing,
                    })
                  : t('conflict.usernameDesc', {
                      login: conflict.login,
                      existing: conflict.existing,
                    })}
              </AlertDescription>
            </Alert>
            <Button
              type="button"
              size="lg"
              className="w-full"
              disabled={retrying}
              onClick={() =>
                navigate({
                  to: '/login',
                  search: {
                    from: conflict.from ?? undefined,
                    match: conflict.provider,
                  },
                })
              }
            >
              {t('conflict.match')}
            </Button>
            {conflict.conflict === 'username' && (
              <Button
                type="button"
                size="lg"
                variant="outline"
                className="w-full"
                disabled={retrying}
                onClick={retryIndependent}
              >
                {retrying ? (
                  <Loader2 className="size-4 animate-spin" />
                ) : null}
                {t('conflict.independent')}
              </Button>
            )}
            <Button
              type="button"
              size="lg"
              variant="ghost"
              className="w-full"
              disabled={retrying}
              onClick={() => navigate({ to: '/login' })}
            >
              {t('conflict.cancel')}
            </Button>
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="flex min-h-svh items-center justify-center bg-gradient-to-br from-primary/10 via-background to-accent/30 p-6">
      <Card className="w-full max-w-md">
        {error ? (
          <>
            <CardHeader>
              <CardTitle>{t('failedTitle')}</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3">
              <Alert variant="destructive">
                <AlertTitle>{t('failedTitle')}</AlertTitle>
                <AlertDescription>{error}</AlertDescription>
              </Alert>
              <p className="text-sm text-muted-foreground">
                <Trans
                  t={t}
                  i18nKey="failedReturn"
                  components={{
                    loginLink: (
                      <Link
                        to="/login"
                        className="font-medium text-primary underline-offset-4 hover:underline"
                      />
                    ),
                  }}
                />
              </p>
            </CardContent>
          </>
        ) : (
          <CardHeader className="items-center text-center">
            <Loader2 className="mb-2 size-7 animate-spin text-primary" />
            <CardTitle>{t('finishing')}</CardTitle>
            <CardDescription>{t('exchanging')}</CardDescription>
          </CardHeader>
        )}
      </Card>
    </div>
  )
}
