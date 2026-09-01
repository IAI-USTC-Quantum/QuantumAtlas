import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import {
  AlertCircle,
  ArrowLeft,
  CircleCheck,
  CircleOff,
  ShieldCheck,
  UsersRound,
} from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { adminUpdateUser, type AdminUser } from '@/lib/api'
import { useAdminUsers, useAdminWhoami, useMe } from '@/lib/queries'

export const Route = createFileRoute('/$lang/admin/users')({
  component: AdminUsersPage,
})

function AdminUsersPage() {
  const { t } = useTranslation('admin')
  const { lang } = Route.useParams()
  const whoami = useAdminWhoami()
  const me = useMe()
  // Mirrors the server-side userAdminGuard: env admin OR is_admin OR
  // is_superadmin.
  const canManage = whoami.data?.is_user_admin ?? false
  // Drives the is_admin action column (server: superadmin only).
  const isSuper = whoami.data?.is_superadmin ?? false
  const users = useAdminUsers(canManage)

  return (
    <section className="space-y-5">
      <div className="space-y-2">
        <Button asChild variant="ghost" size="sm" className="-ml-2">
          <Link to="/$lang/admin" params={{ lang }}>
            <ArrowLeft className="size-4" /> {t('users.back')}
          </Link>
        </Button>
        <PageHeader
          eyebrow={t('eyebrow')}
          title={t('users.title')}
          copy={t('users.subtitle')}
        />
      </div>

      <StatusBlock loading={whoami.isLoading} error="" empty={false}>
        {/* Same session gate as the other admin pages: a whoami failure
            means no browser session, so offer sign-in. */}
        {whoami.error ? (
          <Alert>
            <AlertCircle className="size-4" />
            <AlertTitle>{t('loginPrompt')}</AlertTitle>
            <AlertDescription className="mt-2">
              <Button asChild size="sm">
                <Link to="/login">{t('loginButton')}</Link>
              </Button>
            </AlertDescription>
          </Alert>
        ) : whoami.data && !canManage ? (
          <Alert>
            <AlertCircle className="size-4" />
            <AlertTitle>{t('adminOnly')}</AlertTitle>
            {whoami.data.login && (
              <AlertDescription>
                {t('signedInAs', { login: whoami.data.login })}
              </AlertDescription>
            )}
          </Alert>
        ) : (
          <Panel
            title={t('users.title')}
            icon={UsersRound}
            suffix={String(users.data?.total ?? 0)}
          >
            <StatusBlock
              loading={users.isLoading}
              error={users.error?.message ?? ''}
              empty={!users.isLoading && !users.error && (users.data?.users.length ?? 0) === 0}
              emptyMessage={t('users.empty')}
            >
              {users.data && (
                <div className="overflow-x-auto rounded-lg border border-border">
                  <table className="w-full text-sm">
                    <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                      <tr>
                        <th className="px-4 py-2 font-medium">{t('users.cols.user')}</th>
                        <th className="px-4 py-2 font-medium">{t('users.cols.roles')}</th>
                        <th className="px-4 py-2 font-medium">{t('users.cols.status')}</th>
                        <th className="px-4 py-2 font-medium">{t('users.cols.created')}</th>
                        <th className="px-4 py-2 font-medium">{t('users.cols.actions')}</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border">
                      {users.data.users.map((user) => (
                        <UserRow
                          key={user.id}
                          user={user}
                          selfId={me.data?.id ?? ''}
                          isSuper={isSuper}
                        />
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </StatusBlock>
          </Panel>
        )}
      </StatusBlock>
    </section>
  )
}

// UserRow renders one users record plus its action cell. Client-side
// mirroring of the server's self-protection rules (nobody disables
// themselves / flips their own is_admin; admins can't touch
// superadmins) — the server rejects anything we fail to hide anyway.
function UserRow({
  user,
  selfId,
  isSuper,
}: {
  user: AdminUser
  selfId: string
  isSuper: boolean
}) {
  const { t } = useTranslation('admin')
  const qc = useQueryClient()
  const self = user.id === selfId

  const invalidate = () => {
    void qc.invalidateQueries({ queryKey: ['admin-users'] })
    // The disabled flag also affects what the target can do elsewhere;
    // whoami is cheap to refresh for the acting admin only if they hit
    // themselves, but a blanket refresh keeps flags consistent.
    void qc.invalidateQueries({ queryKey: ['admin-whoami'] })
  }

  const availabilityMutation = useMutation({
    mutationFn: () =>
      adminUpdateUser(user.id, { disabled: !user.disabled }),
    onSuccess: (updated) => {
      toast.success(
        updated.disabled ? t('users.toasts.disabled') : t('users.toasts.enabled'),
      )
      invalidate()
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const adminFlagMutation = useMutation({
    mutationFn: () =>
      adminUpdateUser(user.id, { is_admin: !user.is_admin }),
    onSuccess: (updated) => {
      toast.success(
        updated.is_admin
          ? t('users.toasts.madeAdmin', { login: user.github_login || user.gitea_login || user.email })
          : t('users.toasts.removedAdmin', { login: user.github_login || user.gitea_login || user.email }),
      )
      invalidate()
    },
    onError: (err: Error) => toast.error(err.message),
  })

  const busy = availabilityMutation.isPending || adminFlagMutation.isPending
  // Non-superadmins cannot disable superadmins (server 403s).
  const availabilityBlocked = self || (!isSuper && user.is_superadmin)

  return (
    <tr className="align-top">
      <td className="min-w-64 px-4 py-2">
        <span className="flex items-center gap-2 font-medium">
          {user.name || user.github_login || user.gitea_login || user.email}
          {self && (
            <Badge variant="outline">{t('users.you')}</Badge>
          )}
        </span>
        <code className="mt-0.5 block text-xs text-muted-foreground">
          {user.github_login
            ? `@${user.github_login}`
            : user.gitea_login
              ? `@${user.gitea_login}`
              : '—'}
        </code>
        <span className="block text-xs text-muted-foreground">{user.email}</span>
      </td>
      <td className="px-4 py-2">
        <div className="flex flex-wrap gap-1.5">
          {user.is_superadmin ? (
            <Badge variant="default" className="gap-1">
              <ShieldCheck className="size-3" /> {t('users.superadminRole')}
            </Badge>
          ) : user.is_admin ? (
            <Badge variant="secondary" className="gap-1">
              <ShieldCheck className="size-3" /> {t('users.adminRole')}
            </Badge>
          ) : (
            <span className="text-xs text-muted-foreground">—</span>
          )}
        </div>
      </td>
      <td className="px-4 py-2">
        {user.disabled ? (
          <Badge variant="destructive" className="gap-1">
            <CircleOff className="size-3" /> {t('users.disabledBadge')}
          </Badge>
        ) : (
          <Badge variant="outline" className="gap-1">
            <CircleCheck className="size-3" /> {t('users.activeBadge')}
          </Badge>
        )}
      </td>
      <td className="whitespace-nowrap px-4 py-2 text-muted-foreground">
        {formatCreated(user.created)}
      </td>
      <td className="px-4 py-2">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            size="sm"
            variant={user.disabled ? 'outline' : 'destructive'}
            disabled={busy || availabilityBlocked}
            title={
              self
                ? t('users.selfHint')
                : !isSuper && user.is_superadmin
                  ? t('users.protectedHint')
                  : undefined
            }
            onClick={() => availabilityMutation.mutate()}
          >
            {user.disabled ? t('users.enable') : t('users.disable')}
          </Button>
          {isSuper && !self && (
            <Button
              size="sm"
              variant="outline"
              disabled={busy}
              onClick={() => adminFlagMutation.mutate()}
            >
              {user.is_admin ? t('users.removeAdmin') : t('users.makeAdmin')}
            </Button>
          )}
        </div>
      </td>
    </tr>
  )
}

function formatCreated(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleDateString()
}
