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

type LoginSearch = { from?: string }

export const Route = createFileRoute('/login')({
  component: LoginPage,
  validateSearch: (search: Record<string, unknown>): LoginSearch => ({
    from: typeof search.from === 'string' ? search.from : undefined,
  }),
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
      const dest = search.from && search.from !== '/login' ? search.from : '/'
      navigate({ to: dest })
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

