import { createFileRoute, useNavigate, useRouter } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { Github, Loader2, Sparkles } from 'lucide-react'
import { loginWithGitHub, useAuth } from '@/lib/auth'

type LoginSearch = { from?: string }

export const Route = createFileRoute('/login')({
  component: LoginPage,
  validateSearch: (search: Record<string, unknown>): LoginSearch => ({
    from: typeof search.from === 'string' ? search.from : undefined,
  }),
})

function LoginPage() {
  const auth = useAuth()
  const navigate = useNavigate()
  const router = useRouter()
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
      await loginWithGitHub()
      router.invalidate()
    } catch (e) {
      const message = e instanceof Error ? e.message : String(e)
      setError(message || 'GitHub login failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="login-shell">
      <div className="login-card">
        <span className="brand-mark"><Sparkles size={28} /></span>
        <h1>QuantumAtlas</h1>
        <p className="muted">Sign in with your GitHub account to continue.</p>
        <button
          type="button"
          className="primary login-button"
          disabled={busy}
          onClick={handleLogin}
        >
          {busy ? <Loader2 className="spin" size={18} /> : <Github size={18} />}
          {busy ? 'Opening GitHub...' : 'Continue with GitHub'}
        </button>
        {error && <div className="notice danger">{error}</div>}
        <p className="muted small">
          A popup will open to github.com. Allow popups for this domain if it
          does not appear. After authorizing, you will be redirected back here.
        </p>
      </div>
    </div>
  )
}
