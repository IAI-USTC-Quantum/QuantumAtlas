import { createFileRoute, Link } from '@tanstack/react-router'
import { useEffect, useState } from 'react'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import {
  Gauge,
  GitFork,
  Github,
  KeyRound,
  Link2,
  Loader2,
  Save,
  Search,
  ShieldCheck,
  Trash2,
  UserRound,
} from 'lucide-react'
import { toast } from 'sonner'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { pb } from '@/lib/pb'
import {
  BIND_DONE_KEY,
  BIND_SWITCHED_KEY,
  linkProvider,
  type OAuth2ProviderName,
} from '@/lib/auth'
import {
  useMe,
  useMySearchKeys,
  useMyUsage,
  useSaveSearchKey,
  useSearchBackends,
  useDeleteSearchKey,
} from '@/lib/queries'

export const Route = createFileRoute('/$lang/dashboard')({
  component: DashboardPage,
})

function DashboardPage() {
  const { t } = useTranslation('dashboard')
  const lang = useLang()
  const me = useMe()
  const usage = useMyUsage()
  const qc = useQueryClient()
  const [switched, setSwitched] = useState(false)

  // Returning from a link-mode OAuth callback: the me/whoami queries have
  // a 5-minute staleTime and refetchOnWindowFocus is off, so the fresh
  // binding would otherwise stay invisible until the cache expires.
  useEffect(() => {
    if (sessionStorage.getItem(BIND_DONE_KEY) === '1') {
      sessionStorage.removeItem(BIND_DONE_KEY)
      void qc.invalidateQueries({ queryKey: ['me'] })
      void qc.invalidateQueries({ queryKey: ['admin-whoami'] })
    }
    if (sessionStorage.getItem(BIND_SWITCHED_KEY) === '1') {
      sessionStorage.removeItem(BIND_SWITCHED_KEY)
      setSwitched(true)
    }
  }, [qc])

  const profile = me.data
  // avatar is the raw PocketBase file name; resolve it against the
  // record the session already holds (same id as /api/me returns).
  const avatarURL =
    profile?.avatar && pb.authStore.record
      ? pb.files.getURL(pb.authStore.record, profile.avatar)
      : ''
  const displayName =
    profile?.name ||
    profile?.github_login ||
    profile?.gitea_login ||
    profile?.email ||
    ''

  return (
    <section className="space-y-5">
      <PageHeader
        eyebrow={t('eyebrow')}
        title={t('title')}
        copy={t('subtitle')}
      />

      <Panel title={t('profile.title')} icon={UserRound}>
        <StatusBlock
          loading={me.isLoading}
          error={me.error?.message ?? ''}
          empty={!profile}
        >
          {profile && (
            <div className="flex items-start gap-4">
              {avatarURL ? (
                <img
                  src={avatarURL}
                  alt={displayName}
                  className="size-14 shrink-0 rounded-full border border-border object-cover"
                />
              ) : (
                <span className="flex size-14 shrink-0 items-center justify-center rounded-full bg-muted text-lg font-semibold text-muted-foreground">
                  {(displayName || '?').slice(0, 1).toUpperCase()}
                </span>
              )}
              <dl className="grid min-w-0 flex-1 grid-cols-1 gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
                <div>
                  <dt className="text-xs text-muted-foreground">
                    {t('profile.githubLogin')}
                  </dt>
                  <dd className="mt-0.5 flex items-center gap-1.5 font-medium">
                    <Github className="size-3.5 text-muted-foreground" />
                    {profile.github_login ? (
                      <a
                        href={`https://github.com/${profile.github_login}`}
                        target="_blank"
                        rel="noreferrer"
                        className="text-primary hover:underline"
                      >
                        {profile.github_login}
                      </a>
                    ) : (
                      '—'
                    )}
                  </dd>
                </div>
                {profile.gitea_login && (
                  <div>
                    <dt className="text-xs text-muted-foreground">
                      {t('profile.giteaLogin')}
                    </dt>
                    <dd className="mt-0.5 flex items-center gap-1.5 font-medium">
                      <GitFork className="size-3.5 text-muted-foreground" />
                      {profile.gitea_login}
                    </dd>
                  </div>
                )}
                <div>
                  <dt className="text-xs text-muted-foreground">
                    {t('profile.name')}
                  </dt>
                  <dd className="mt-0.5 font-medium">
                    {profile.name || '—'}
                  </dd>
                </div>
                <div>
                  <dt className="text-xs text-muted-foreground">
                    {t('profile.email')}
                  </dt>
                  <dd className="mt-0.5 font-medium">
                    {profile.email || '—'}
                  </dd>
                </div>
                <div>
                  <dt className="text-xs text-muted-foreground">
                    {t('profile.userId')}
                  </dt>
                  <dd className="mt-0.5">
                    <code className="rounded bg-muted px-1 py-0.5 text-xs">
                      {profile.id}
                    </code>
                  </dd>
                </div>
                <div>
                  <dt className="text-xs text-muted-foreground">
                    {t('profile.role')}
                  </dt>
                  <dd className="mt-0.5">
                    {profile.is_admin ? (
                      <Badge className="gap-1">
                        <ShieldCheck className="size-3" />
                        {t('profile.admin')}
                      </Badge>
                    ) : (
                      <Badge variant="secondary">{t('profile.member')}</Badge>
                    )}
                  </dd>
                </div>
                <div>
                  <dt className="text-xs text-muted-foreground">
                    {t('profile.created')}
                  </dt>
                  <dd className="mt-0.5 font-medium">
                    {profile.created
                      ? new Date(profile.created).toLocaleDateString()
                      : '—'}
                  </dd>
                </div>
              </dl>
            </div>
          )}
        </StatusBlock>
      </Panel>

      <Panel title={t('bind.title')} icon={Link2}>
        <div className="space-y-3">
          {switched && (
            <Alert variant="destructive">
              <AlertTitle>{t('bind.switchedTitle')}</AlertTitle>
              <AlertDescription>{t('bind.switchedDesc')}</AlertDescription>
            </Alert>
          )}
          <p className="text-sm text-muted-foreground">{t('bind.copy')}</p>
          <div className="grid gap-3 sm:grid-cols-2">
            <BindCard
              provider="github"
              bound={profile?.github_bound ?? false}
              login={profile?.github_login ?? ''}
              redirect={() => void linkProvider('github', `/${lang}/dashboard`)}
            />
            <BindCard
              provider="gitea"
              bound={profile?.gitea_bound ?? false}
              login={profile?.gitea_login ?? ''}
              redirect={() => void linkProvider('gitea', `/${lang}/dashboard`)}
            />
          </div>
        </div>
      </Panel>

      <Panel title={t('usage.title')} icon={Gauge}>
        {usage.error ? (
          // A 503 here just means the metering store is unavailable —
          // degrade the panel instead of failing the whole page.
          <p className="text-sm text-muted-foreground">
            {t('usage.unavailable')}
          </p>
        ) : (
          <StatusBlock loading={usage.isLoading} error="" empty={!usage.data}>
            {usage.data && (
              <div className="space-y-3">
                <div className="flex items-baseline justify-between gap-3 text-sm">
                  <span className="text-muted-foreground">
                    {t('usage.agenticSearch')}
                  </span>
                  <span className="font-medium">
                    {t('usage.todayOfLimit', {
                      today: usage.data.today,
                      limit: usage.data.limit,
                    })}
                  </span>
                </div>
                <div className="h-2 overflow-hidden rounded-full bg-muted">
                  <div
                    className="h-full rounded-full bg-primary transition-all"
                    style={{
                      width: `${Math.min(
                        100,
                        usage.data.limit > 0
                          ? (usage.data.today / usage.data.limit) * 100
                          : 0,
                      )}%`,
                    }}
                  />
                </div>
                <p className="text-xs text-muted-foreground">
                  {t('usage.llmTokens', { count: usage.data.llm_tokens })}
                </p>
              </div>
            )}
          </StatusBlock>
        )}
      </Panel>

      <Panel title={t('searchKeys.title')} icon={Search}>
        <SearchKeysPanel />
      </Panel>

      <Panel title={t('pat.title')} icon={KeyRound}>
        <div className="flex flex-wrap items-center justify-between gap-3">
          <p className="text-sm text-muted-foreground">{t('pat.copy')}</p>
          <Button asChild variant="outline" size="sm">
            <Link to="/$lang/pat" params={{ lang }}>
              {t('pat.manage')}
            </Link>
          </Button>
        </div>
      </Panel>
    </section>
  )
}

// SearchKeysPanel manages the caller's third-party search API keys:
// one row per backend that accepts a user key. Secret-field pattern
// from PluginConfigForm — password input, empty means "leave
// unchanged", masked hint shows what is stored.
function SearchKeysPanel() {
  const { t } = useTranslation('dashboard')
  const keysQuery = useMySearchKeys()
  const backendsQuery = useSearchBackends(true)
  const saveMutation = useSaveSearchKey()
  const deleteMutation = useDeleteSearchKey()
  const [drafts, setDrafts] = useState<Record<string, string>>({})

  const keysEnabled = keysQuery.data?.enabled ?? true
  const storedByBackend = new Map(
    (keysQuery.data?.keys ?? []).map((k) => [k.backend, k]),
  )
  const configurable =
    backendsQuery.data?.backends.filter((b) => b.user_key) ?? []

  return (
    <div className="space-y-3">
      <p className="text-sm text-muted-foreground">{t('searchKeys.copy')}</p>
      {keysQuery.error ? (
        <p className="text-sm text-muted-foreground">
          {t('searchKeys.listUnavailable')}
        </p>
      ) : keysQuery.isLoading ? (
        <p className="text-sm text-muted-foreground">{t('searchKeys.loading')}</p>
      ) : !keysEnabled ? (
        <p className="text-sm text-muted-foreground">{t('searchKeys.disabled')}</p>
      ) : configurable.length === 0 ? (
        <p className="text-sm text-muted-foreground">{t('searchKeys.empty')}</p>
      ) : (
        <div className="space-y-3">
          {configurable.map((b) => {
            const stored = storedByBackend.get(b.name)
            const draft = drafts[b.name] ?? ''
            const busy =
              (saveMutation.isPending && saveMutation.variables?.backend === b.name) ||
              (deleteMutation.isPending && deleteMutation.variables === b.name)
            return (
              <div
                key={b.name}
                className="space-y-2 rounded-lg border border-border p-4"
              >
                <div className="flex flex-wrap items-center gap-2">
                  <span className="text-sm font-medium">{b.label}</span>
                  <Badge variant={b.requires_key ? 'default' : 'secondary'}>
                    {b.requires_key
                      ? t('searchKeys.required')
                      : t('searchKeys.optional')}
                  </Badge>
                  {stored ? (
                    <Badge variant="outline" className="font-mono">
                      {stored.hint}
                    </Badge>
                  ) : (
                    <Badge variant="outline">{t('searchKeys.notConfigured')}</Badge>
                  )}
                </div>
                <div className="flex flex-wrap items-end gap-2">
                  <div className="min-w-52 flex-1 space-y-1.5">
                    <Label htmlFor={`key-${b.name}`} className="text-xs">
                      {t('searchKeys.keyLabel')}
                    </Label>
                    <Input
                      id={`key-${b.name}`}
                      type="password"
                      autoComplete="off"
                      placeholder={stored ? t('searchKeys.secretHint') : ''}
                      value={draft}
                      onChange={(e) =>
                        setDrafts((prev) => ({ ...prev, [b.name]: e.target.value }))
                      }
                    />
                  </div>
                  <Button
                    size="sm"
                    disabled={!draft.trim() || busy}
                    onClick={() =>
                      saveMutation.mutate(
                        { backend: b.name, key: draft.trim() },
                        {
                          onSuccess: () => {
                            toast.success(t('searchKeys.saved', { backend: b.label }))
                            setDrafts((prev) => ({ ...prev, [b.name]: '' }))
                          },
                          onError: (e) => toast.error(e.message),
                        },
                      )
                    }
                  >
                    {busy ? (
                      <Loader2 className="size-4 animate-spin" />
                    ) : (
                      <Save className="size-4" />
                    )}
                    {t('searchKeys.save')}
                  </Button>
                  {stored && (
                    <Button
                      size="sm"
                      variant="outline"
                      disabled={busy}
                      onClick={() => {
                        if (!window.confirm(t('searchKeys.confirmDelete', { backend: b.label }))) return
                        deleteMutation.mutate(b.name, {
                          onSuccess: () =>
                            toast.success(t('searchKeys.deleted', { backend: b.label })),
                          onError: (e) => toast.error(e.message),
                        })
                      }}
                    >
                      <Trash2 className="size-4" />
                      {t('searchKeys.delete')}
                    </Button>
                  )}
                </div>
                {stored?.updated_at && (
                  <p className="text-xs text-muted-foreground">
                    {t('searchKeys.updatedAt', {
                      date: new Date(stored.updated_at).toLocaleString(),
                    })}
                  </p>
                )}
              </div>
            )
          })}
        </div>
      )}
    </div>
  )
}

// BindCard is one provider's binding sub-panel: userinfo when bound, a
// bind button when not. The bind click starts the link-mode OAuth round
// trip (see lib/auth.ts::linkProvider) — the page navigates away to the
// provider and comes back to this dashboard afterwards.
function BindCard({
  provider,
  bound,
  login,
  redirect,
}: {
  provider: OAuth2ProviderName
  bound: boolean
  login: string
  redirect: () => void
}) {
  const { t } = useTranslation('dashboard')
  const [busy, setBusy] = useState(false)
  const Icon = provider === 'github' ? Github : GitFork

  function handleBind() {
    setBusy(true)
    redirect()
    // On success the tab navigates away before this fires; reaching the
    // timeout means the provider lookup threw — re-enable for a retry.
    setTimeout(() => setBusy(false), 5_000)
  }

  return (
    <div className="flex flex-col gap-3 rounded-lg border border-border p-4">
      <div className="flex items-center justify-between gap-2">
        <span className="flex items-center gap-2 text-sm font-medium">
          <Icon className="size-4 text-muted-foreground" />
          {provider === 'github' ? 'GitHub' : 'Gitea'}
        </span>
        {bound ? (
          <Badge variant="outline" className="gap-1">
            <ShieldCheck className="size-3" />
            {t('bind.bound')}
          </Badge>
        ) : (
          <Badge variant="secondary">{t('bind.notBound')}</Badge>
        )}
      </div>
      {bound ? (
        <p className="text-sm">
          {login ? (
            <code className="rounded bg-muted px-1 py-0.5 text-xs">
              @{login}
            </code>
          ) : (
            <span className="text-muted-foreground">—</span>
          )}
        </p>
      ) : (
        <p className="text-xs text-muted-foreground">
          {t('bind.hint', {
            provider: provider === 'github' ? 'GitHub' : 'Gitea',
          })}
        </p>
      )}
      {!bound && (
        <Button size="sm" variant="outline" disabled={busy} onClick={handleBind}>
          {busy ? (
            <Loader2 className="size-4 animate-spin" />
          ) : (
            <Link2 className="size-4" />
          )}
          {provider === 'github' ? t('bind.bindGithub') : t('bind.bindGitea')}
        </Button>
      )}
    </div>
  )
}
