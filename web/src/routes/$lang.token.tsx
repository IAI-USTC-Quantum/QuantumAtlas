import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { Trans, useTranslation } from 'react-i18next'
import { Clipboard, Code2, Eye, EyeOff, Key, KeyRound } from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import {
  Card,
  CardContent,
  CardDescription,
  CardHeader,
  CardTitle,
} from '@/components/ui/card'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'
import { useLang } from '@/hooks/use-lang'
import { useAuth } from '@/lib/auth'
import { maskToken, shortToken } from '@/lib/utils'

export const Route = createFileRoute('/$lang/token')({
  component: TokenPage,
})

function TokenPage() {
  const { t } = useTranslation('token')
  const { t: tc } = useTranslation('common')
  const lang = useLang()
  const auth = useAuth()
  const token = auth.token
  const [revealed, setRevealed] = useState(false)
  const origin = typeof window !== 'undefined' ? window.location.origin : ''
  const curlCommand = `curl -k -H 'Authorization: Bearer ${token}' ${origin}/api/server/info`
  const cliExport = `export QATLAS_SERVER_URL=${origin}\nexport QATLAS_TOKEN=${token}`

  async function copy(text: string, label: string) {
    await navigator.clipboard.writeText(text)
    toast.success(t('copied', { label }))
  }

  return (
    <section className="grid gap-6 lg:grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)]">
      <div className="space-y-3">
        <span className="inline-flex size-12 items-center justify-center rounded-2xl bg-primary/15 text-primary">
          <KeyRound className="size-6" />
        </span>
        <p className="text-xs font-medium uppercase tracking-[0.18em] text-primary">
          {t('eyebrow')}
        </p>
        <h1 className="text-2xl font-semibold tracking-tight">{t('title')}</h1>
        <p className="text-sm text-muted-foreground">{t('subtitle')}</p>
        <p className="flex items-start gap-2 text-sm text-muted-foreground">
          <Key className="mt-0.5 size-3.5 shrink-0" />
          <span>
            <Trans
              t={t}
              i18nKey="patHint"
              components={{
                patLink: (
                  <Link
                    to="/$lang/pat"
                    params={{ lang }}
                    className="font-medium text-primary underline-offset-4 hover:underline"
                  />
                ),
              }}
            />
          </span>
        </p>
        <dl className="grid gap-2 pt-2 text-sm">
          <div className="flex items-baseline justify-between gap-3 border-b border-border py-1.5">
            <dt className="text-muted-foreground">{t('fields.scope')}</dt>
            <dd className="font-medium">{t('fields.scopeValue')}</dd>
          </div>
          <div className="flex items-baseline justify-between gap-3 border-b border-border py-1.5">
            <dt className="text-muted-foreground">{t('fields.identity')}</dt>
            <dd className="truncate font-medium">
              {auth.user?.email || auth.user?.username || 'unknown'}
            </dd>
          </div>
          <div className="flex items-baseline justify-between gap-3 py-1.5">
            <dt className="text-muted-foreground">{t('fields.source')}</dt>
            <dd className="font-medium">{t('fields.sourceValue')}</dd>
          </div>
        </dl>
      </div>

      <Card>
        <CardHeader className="flex flex-row items-start justify-between gap-3">
          <div>
            <CardDescription className="text-xs font-medium uppercase tracking-wider text-primary">
              {t('panelEyebrow')}
            </CardDescription>
            <CardTitle className="text-xl">
              {token ? t('ready') : t('needSignIn')}
            </CardTitle>
          </div>
          <Badge variant={token ? 'default' : 'secondary'}>
            {token ? tc('status.active') : tc('status.missing')}
          </Badge>
        </CardHeader>
        <CardContent className="space-y-4">
          <div className="break-all rounded-md border border-dashed border-border bg-muted/40 px-3 py-2 font-mono text-xs">
            {shortToken(token)}
          </div>
          <div className="flex items-center justify-between gap-2">
            <Label htmlFor="token-value">{t('tokenLabel')}</Label>
            <Button
              type="button"
              size="xs"
              variant="ghost"
              disabled={!token}
              onClick={() => setRevealed((value) => !value)}
            >
              {revealed ? (
                <EyeOff className="size-3.5" />
              ) : (
                <Eye className="size-3.5" />
              )}
              {revealed ? tc('actions.hide') : tc('actions.reveal')}
            </Button>
          </div>
          <Textarea
            id="token-value"
            readOnly
            spellCheck={false}
            value={revealed ? token : maskToken(token)}
            className="font-mono text-xs"
            rows={4}
          />
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              disabled={!token}
              onClick={() => copy(token, t('copyLabels.token'))}
            >
              <Clipboard className="size-4" /> {t('actions.copyToken')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={!token}
              onClick={() => copy(curlCommand, t('copyLabels.command'))}
            >
              <Code2 className="size-4" /> {t('actions.copyCurl')}
            </Button>
            <Button
              type="button"
              variant="secondary"
              disabled={!token}
              onClick={() => copy(cliExport, t('copyLabels.cliEnv'))}
            >
              <Code2 className="size-4" /> {t('actions.copyCliEnv')}
            </Button>
          </div>
          <pre className="overflow-x-auto rounded-md bg-muted px-3 py-2 text-xs">
            <code>{curlCommand}</code>
          </pre>
          <p className="text-xs text-muted-foreground">{t('warning')}</p>
        </CardContent>
      </Card>
    </section>
  )
}
