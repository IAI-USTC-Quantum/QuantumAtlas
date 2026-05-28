import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Loader2 } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { completeOAuth2Login } from '@/lib/auth'

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
        const dest = from && from !== '/login' ? from : '/'
        navigate({ to: dest, replace: true })
      })
      .catch((e: unknown) => {
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

