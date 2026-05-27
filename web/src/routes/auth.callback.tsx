import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useEffect, useRef, useState } from 'react'
import { Loader2 } from 'lucide-react'
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
      setError('Missing code or state in OAuth callback URL.')
      return
    }

    completeOAuth2Login(search.code, search.state)
      .then(({ from }) => {
        const dest = from && from !== '/login' ? from : '/'
        navigate({ to: dest, replace: true })
      })
      .catch((e: unknown) => {
        const message = e instanceof Error ? e.message : String(e)
        setError(message || 'Failed to complete GitHub sign-in.')
      })
  }, [navigate, search.code, search.state, search.error, search.error_description])

  return (
    <div className="login-shell">
      <div className="login-card">
        {error ? (
          <>
            <h1>Sign-in failed</h1>
            <div className="notice danger">{error}</div>
            <p className="muted small">
              <Link to="/login">Return to the sign-in page</Link> and try again.
            </p>
          </>
        ) : (
          <>
            <Loader2 className="spin" size={28} />
            <h1>Finishing sign-in…</h1>
            <p className="muted">Exchanging the GitHub code with PocketBase.</p>
          </>
        )}
      </div>
    </div>
  )
}
