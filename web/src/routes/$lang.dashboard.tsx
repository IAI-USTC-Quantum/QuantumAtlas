import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import {
  Gauge,
  Github,
  KeyRound,
  ShieldCheck,
  UserRound,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { pb } from '@/lib/pb'
import { useMe, useMyUsage } from '@/lib/queries'

export const Route = createFileRoute('/$lang/dashboard')({
  component: DashboardPage,
})

function DashboardPage() {
  const { t } = useTranslation('dashboard')
  const lang = useLang()
  const me = useMe()
  const usage = useMyUsage()

  const profile = me.data
  // avatar is the raw PocketBase file name; resolve it against the
  // record the session already holds (same id as /api/me returns).
  const avatarURL =
    profile?.avatar && pb.authStore.record
      ? pb.files.getURL(pb.authStore.record, profile.avatar)
      : ''
  const displayName =
    profile?.name || profile?.github_login || profile?.email || ''

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
