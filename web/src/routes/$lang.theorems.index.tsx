import { createFileRoute, Link } from '@tanstack/react-router'
import { useMemo, useState } from 'react'
import { useMutation, useQueryClient } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { CheckCircle2, GitPullRequestArrow, ListTree, RefreshCw, XCircle } from 'lucide-react'
import { toast } from 'sonner'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from '@/components/ui/select'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { pullTheorems, type Theorem } from '@/lib/api'
import {
  useTheoremFamilies,
  useTheorems,
  useTheoremsSyncStatus,
  useTheoremStats,
} from '@/lib/queries'

export const Route = createFileRoute('/$lang/theorems/')({
  component: TheoremsPage,
})

const ALL = '__all__'

function TheoremsPage() {
  const { t } = useTranslation('theorems')
  const lang = useLang()
  const queryClient = useQueryClient()

  const [familyId, setFamilyId] = useState('')
  const [auditStatus, setAuditStatus] = useState('')
  const [text, setText] = useState('')

  const families = useTheoremFamilies()
  const stats = useTheoremStats()
  const sync = useTheoremsSyncStatus()
  const theorems = useTheorems(familyId, auditStatus)

  const auditOptions = useMemo(
    () => Object.keys(stats.data?.by_audit_status ?? {}).sort(),
    [stats.data],
  )

  // Server applies family_id / audit_status; the free-text box filters the
  // returned page client-side over lean_fqn + statement_paraphrase.
  const rows = useMemo(() => {
    const q = text.trim().toLowerCase()
    const items = theorems.data?.theorems ?? []
    if (!q) return items
    return items.filter(
      (thm) =>
        thm.lean_fqn.toLowerCase().includes(q) ||
        (thm.statement_paraphrase ?? '').toLowerCase().includes(q),
    )
  }, [theorems.data, text])

  const pull = useMutation({
    mutationFn: pullTheorems,
    onSuccess: (res) => {
      toast.success(
        res.changed
          ? t('pull.changed', { from: res.old_commit, to: res.new_commit })
          : t('pull.upToDate'),
      )
      void queryClient.invalidateQueries({ queryKey: ['theorems'] })
    },
    onError: (err: Error) => toast.error(t('pull.failed', { error: err.message })),
  })

  const git = sync.data?.git

  return (
    <section className="space-y-6">
      <div className="flex flex-wrap items-start justify-between gap-4">
        <PageHeader eyebrow={t('eyebrow')} title={t('title')} copy={t('subtitle')} />
        <div className="flex flex-col items-end gap-1">
          <Button
            onClick={() => pull.mutate()}
            disabled={pull.isPending}
            variant="outline"
            size="sm"
          >
            <GitPullRequestArrow className="size-4" />
            {pull.isPending ? t('pull.running') : t('pull.button')}
          </Button>
          {git?.enabled && (
            <span className="text-xs text-muted-foreground">
              {git.branch ?? '—'} · {git.commit ?? '—'}
            </span>
          )}
        </div>
      </div>

      <Panel title={t('overview.title')} icon={ListTree} suffix={`${stats.data?.total ?? 0}`}>
        <div className="flex flex-wrap gap-2 text-sm">
          <Badge variant="secondary">
            {t('overview.total')}: {stats.data?.total ?? 0}
          </Badge>
          <Badge variant="secondary">
            {t('overview.sorryFree')}: {stats.data?.sorry_free ?? 0}
          </Badge>
          <Badge variant="secondary">
            {t('overview.families')}: {stats.data?.families ?? 0}
          </Badge>
          <Badge variant="secondary">
            {t('overview.certified')}: {stats.data?.certified ?? 0}
          </Badge>
        </div>
      </Panel>

      <div className="flex flex-wrap items-center gap-3">
        <Input
          value={text}
          onChange={(e) => setText(e.target.value)}
          placeholder={t('filters.searchPlaceholder')}
          className="max-w-xs"
        />
        <Select
          value={familyId || ALL}
          onValueChange={(v) => setFamilyId(v === ALL ? '' : v)}
        >
          <SelectTrigger className="w-[220px]">
            <SelectValue placeholder={t('filters.family')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('filters.allFamilies')}</SelectItem>
            {(families.data?.families ?? []).map((f) => (
              <SelectItem key={f.id} value={f.id}>
                {f.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select
          value={auditStatus || ALL}
          onValueChange={(v) => setAuditStatus(v === ALL ? '' : v)}
        >
          <SelectTrigger className="w-[180px]">
            <SelectValue placeholder={t('filters.audit')} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value={ALL}>{t('filters.allAudit')}</SelectItem>
            {auditOptions.map((s) => (
              <SelectItem key={s} value={s}>
                {s}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        {theorems.isFetching && (
          <RefreshCw className="size-4 animate-spin text-muted-foreground" />
        )}
      </div>

      <StatusBlock
        loading={theorems.isLoading}
        error={theorems.error?.message ?? ''}
        empty={rows.length === 0}
        emptyMessage={t('empty')}
      >
        <div className="overflow-x-auto rounded-xl border border-border">
          <table className="w-full text-sm">
            <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
              <tr>
                <th className="px-4 py-2.5 font-medium">{t('cols.fqn')}</th>
                <th className="px-4 py-2.5 font-medium">{t('cols.family')}</th>
                <th className="px-4 py-2.5 font-medium">{t('cols.kind')}</th>
                <th className="px-4 py-2.5 font-medium">{t('cols.audit')}</th>
                <th className="px-4 py-2.5 font-medium">{t('cols.sorryFree')}</th>
                <th className="px-4 py-2.5 font-medium">{t('cols.statement')}</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((thm) => (
                <TheoremRow key={thm.lean_fqn} thm={thm} lang={lang} />
              ))}
            </tbody>
          </table>
        </div>
        <p className="mt-2 text-xs text-muted-foreground">
          {t('count', { shown: rows.length, total: theorems.data?.total ?? 0 })}
        </p>
      </StatusBlock>
    </section>
  )
}

function TheoremRow({ thm, lang }: { thm: Theorem; lang: string }) {
  return (
    <tr className="border-t border-border transition-colors hover:bg-accent/30">
      <td className="px-4 py-2.5 align-top">
        <Link
          to="/$lang/theorems/theorem/$"
          params={{ lang, _splat: thm.lean_fqn }}
          className="font-mono text-xs font-medium text-primary hover:underline"
        >
          {thm.lean_fqn}
        </Link>
      </td>
      <td className="px-4 py-2.5 align-top text-muted-foreground">
        {thm.family_id ?? '—'}
      </td>
      <td className="px-4 py-2.5 align-top">
        {thm.kind ? <Badge variant="outline">{thm.kind}</Badge> : '—'}
      </td>
      <td className="px-4 py-2.5 align-top">
        {thm.audit_status ? (
          <Badge variant="secondary">{thm.audit_status}</Badge>
        ) : (
          '—'
        )}
      </td>
      <td className="px-4 py-2.5 align-top">
        {thm.sorry_free ? (
          <CheckCircle2 className="size-4 text-emerald-500" />
        ) : (
          <XCircle className="size-4 text-muted-foreground" />
        )}
      </td>
      <td className="max-w-md px-4 py-2.5 align-top text-muted-foreground">
        {thm.statement_paraphrase ?? '—'}
      </td>
    </tr>
  )
}
