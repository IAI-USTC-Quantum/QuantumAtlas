import { createFileRoute } from '@tanstack/react-router'
import { useState } from 'react'
import { Clipboard, Code2, KeyRound } from 'lucide-react'
import { useSessionToken } from '@/lib/queries'
import { maskToken, shortToken } from '@/lib/utils'

export const Route = createFileRoute('/token')({
  component: TokenPage,
})

declare global {
  interface Window {
    __tokenHintTimer?: number
  }
}

function TokenPage() {
  const session = useSessionToken()
  const token = session.data?.token ?? ''
  const [copied, setCopied] = useState<string>('')
  const [revealed, setRevealed] = useState(false)
  const origin = typeof window !== 'undefined' ? window.location.origin : ''
  const curlCommand = `curl -k -H 'Authorization: Bearer ${token}' ${origin}/api/server/info`
  const tokenStatus = session.isLoading ? 'Loading' : token ? 'Active' : 'Missing'

  async function copy(text: string, label: string) {
    await navigator.clipboard.writeText(text)
    setCopied(`${label} copied`)
    window.clearTimeout(window.__tokenHintTimer)
    window.__tokenHintTimer = window.setTimeout(() => setCopied(''), 2200)
  }

  return (
    <section className="token-layout">
      <div className="token-intro">
        <KeyRound size={34} />
        <p className="eyebrow">Caddy-authenticated access</p>
        <h1>QuantumAtlas Token</h1>
        <p>Copy your current Caddy-issued bearer token for API calls from trusted terminals.</p>
        <dl>
          <div><dt>Scope</dt><dd>QuantumAtlas API</dd></div>
          <div><dt>Lifetime</dt><dd>7 days</dd></div>
          <div><dt>Source</dt><dd>Caddy auth session</dd></div>
        </dl>
      </div>
      <div className="token-workspace">
        <div className="panel-heading">
          <div>
            <p className="eyebrow">Access token</p>
            <h2>{session.isLoading ? 'Loading session' : token ? 'Ready for CLI use' : 'Sign in required'}</h2>
          </div>
          <span className={token ? 'status good' : 'status'}>{tokenStatus}</span>
        </div>
        {session.error && <div className="notice danger">{session.error.message}</div>}
        <div className="token-box">{shortToken(token)}</div>
        <div className="field-row">
          <label htmlFor="token-value">Token value</label>
          <button className="ghost small" type="button" disabled={!token} onClick={() => setRevealed((value) => !value)}>
            {revealed ? 'Hide' : 'Reveal'}
          </button>
        </div>
        <textarea id="token-value" readOnly spellCheck={false} value={revealed ? token : maskToken(token)} />
        <div className="actions">
          <button className="primary" type="button" disabled={!token} onClick={() => copy(token, 'Token')}>
            <Clipboard size={17} /> Copy token
          </button>
          <button className="secondary" type="button" disabled={!token} onClick={() => copy(curlCommand, 'Command')}>
            <Code2 size={17} /> Copy curl
          </button>
          <span className="copy-state" aria-live="polite">{copied}</span>
        </div>
        <pre className="command-block"><code>{curlCommand}</code></pre>
        <p className="muted">Treat this value like a password. It is signed by Caddy and expires with the auth policy.</p>
      </div>
    </section>
  )
}
