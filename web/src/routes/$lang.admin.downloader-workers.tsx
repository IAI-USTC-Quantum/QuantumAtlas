import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useMutation } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { AlertCircle, ArrowLeft, Copy, KeyRound, Loader2, RefreshCw, Server } from 'lucide-react'
import { toast } from 'sonner'

import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogFooter, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import {
  adminDownloaderEnrollment,
  type AdminDownloaderWorker,
  type DownloaderEnrollment,
  type DownloaderWorkerAction,
} from '@/lib/api'
import { useAdminDownloaderWorkerAction, useAdminDownloaderWorkers, useAdminWhoami } from '@/lib/queries'

export const Route = createFileRoute('/$lang/admin/downloader-workers')({
  component: AdminDownloaderWorkersPage,
})

type Text = (en: string, zh: string) => string

function AdminDownloaderWorkersPage() {
  const { lang } = Route.useParams()
  const { t } = useTranslation('admin')
  const whoami = useAdminWhoami()
  const text: Text = (en, zh) => lang === 'zh' ? zh : en

  return (
    <section className="space-y-5">
      <div className="space-y-2">
        <Button asChild variant="ghost" size="sm" className="-ml-2">
          <Link to="/$lang/admin" params={{ lang }}>
            <ArrowLeft className="size-4" /> {text('Back to admin', '返回管理面板')}
          </Link>
        </Button>
        <PageHeader
          eyebrow={t('eyebrow')}
          title={text('Downloader workers', '下载工作节点')}
          copy={text('Approve outbound workers and monitor their capacity, health and jobs.', '审批主动连接的工作节点，监控容量、健康状态与任务。')}
        />
      </div>
      <StatusBlock loading={whoami.isLoading} error="" empty={false}>
        {whoami.error ? (
          <Alert>
            <AlertCircle className="size-4" />
            <AlertTitle>{t('loginPrompt')}</AlertTitle>
            <AlertDescription className="mt-2">
              <Button asChild size="sm"><Link to="/login">{t('loginButton')}</Link></Button>
            </AlertDescription>
          </Alert>
        ) : !whoami.data?.is_admin ? (
          <Alert>
            <AlertCircle className="size-4" />
            <AlertTitle>{t('adminOnly')}</AlertTitle>
            {whoami.data?.login && <AlertDescription>{t('signedInAs', { login: whoami.data.login })}</AlertDescription>}
          </Alert>
        ) : <WorkerAdmin text={text} />}
      </StatusBlock>
    </section>
  )
}

function WorkerHealth({ worker, now, text }: { worker: AdminDownloaderWorker; now: number; text: Text }) {
  if (worker.status !== 'approved' && worker.status !== 'draining') return null
  const heartbeat = worker.last_seen ? Date.parse(worker.last_seen) : NaN
  if (!Number.isFinite(heartbeat) || now - heartbeat > 90_000) {
    return <Badge variant="destructive">{text('Offline / stale heartbeat', '离线 / 心跳过期')}</Badge>
  }
  return <Badge variant={worker.browser_ok ? 'outline' : 'secondary'}>
    {worker.browser_ok ? text('Online', '在线') : text('Online · browser unavailable', '在线 · 浏览器不可用')}
  </Badge>
}

function WorkerAdmin({ text }: { text: Text }) {
  const snapshot = useAdminDownloaderWorkers(true)
  const workers = snapshot.data?.workers ?? []
  const jobs = snapshot.data?.jobs ?? []
  const action = useAdminDownloaderWorkerAction()
  const [selection, setSelection] = useState<{ worker: AdminDownloaderWorker; action: DownloaderWorkerAction } | null>(null)
  const selectedWorker = workers.find((worker) => worker.id === selection?.worker.id)
  const actionAllowed = !!selection && !!selectedWorker && workerActions(selectedWorker.status).includes(selection.action)
  const labels: Record<DownloaderWorkerAction, string> = {
    approve: text('Approve', '批准'), reject: text('Reject', '拒绝'),
    drain: text('Drain', '排空'), enable: text('Enable', '启用'), revoke: text('Revoke', '撤销'),
  }
  const descriptions: Record<DownloaderWorkerAction, string> = {
    approve: text('Only approve a worker whose identity you have verified. Approval allows it to lease download jobs.', '请仅批准已核实身份的节点。批准后该节点可以领取下载任务。'),
    reject: text('Reject this registration. The worker will not be allowed to lease jobs.', '拒绝此次注册，该节点将无法领取任务。'),
    drain: text('Stop issuing new leases to this worker while its current jobs finish.', '停止向此节点分配新任务，等待当前任务完成。'),
    enable: text('Allow this drained worker to lease jobs again.', '允许此排空中的节点重新领取任务。'),
    revoke: text('Revoke this worker’s access. Use drain instead if current jobs should finish first.', '撤销此节点的访问权限。如需等待当前任务完成，请使用排空。'),
  }

  return (
    <div className="space-y-5">
      <EnrollmentPanel text={text} />
      <Panel title={text('Workers', '工作节点')} icon={Server} suffix={String(workers.length)}>
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <p className="max-w-3xl text-sm text-muted-foreground">
            {text('Registration is pending until approved. Only approved, healthy workers can lease new jobs; draining stops new leases. Health values reflect the last reported heartbeat, not a live connectivity check.', '注册后需等待审批。仅已批准且健康的节点可领取新任务；排空会停止新任务分配。健康数据来自最近一次心跳，不代表实时连接状态。')}
          </p>
          <Button size="sm" variant="outline" disabled={snapshot.isFetching} onClick={() => void snapshot.refetch()}>
            <RefreshCw className={`size-4 ${snapshot.isFetching ? 'animate-spin' : ''}`} />
            {text('Refresh', '刷新')}
          </Button>
        </div>
        <p className="mb-3 text-xs text-muted-foreground">
          {text('Refreshes every 10 seconds · Last successful snapshot:', '每 10 秒刷新 · 最近成功刷新：')}{' '}
          {snapshot.dataUpdatedAt ? new Date(snapshot.dataUpdatedAt).toLocaleString() : '—'}
        </p>
        <StatusBlock loading={snapshot.isLoading} error={snapshot.error?.message ?? ''} empty={!workers.length} emptyMessage={text('No workers registered. Create an enrollment token to connect one.', '尚无已注册节点。请创建注册令牌以连接节点。')}>
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  {[text('Worker / status', '节点 / 状态'), text('Last heartbeat', '最近心跳'), text('Running / capacity', '运行中 / 容量'), text('Browser', '浏览器'), text('Disk free / spool', '磁盘可用 / 暂存'), text('Last error', '最近错误'), text('Actions', '操作')].map((label) => <th key={label} scope="col" className="px-4 py-2 font-medium">{label}</th>)}
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {workers.map((worker) => (
                  <tr key={worker.id} className="align-top">
                    <td className="min-w-48 px-4 py-3">
                      <div className="break-words font-medium">{worker.name || worker.id}</div>
                      <code className="mb-2 block break-all text-xs text-muted-foreground">{worker.id}</code>
                      <div className="flex flex-wrap gap-1">
                        <WorkerStatus status={worker.status} text={text} />
                        <WorkerHealth worker={worker} now={snapshot.dataUpdatedAt} text={text} />
                      </div>
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 text-muted-foreground">{formatTime(worker.last_seen)}</td>
                    <td className="whitespace-nowrap px-4 py-3 tabular-nums">{worker.running ?? '—'} / {worker.capacity ?? '—'}</td>
                    <td className="px-4 py-3">
                      <Badge variant={worker.browser_ok === false ? 'destructive' : 'outline'}>
                        {worker.browser_ok === true ? text('Ready', '就绪') : worker.browser_ok === false ? text('Not ready', '未就绪') : text('Unknown', '未知')}
                      </Badge>
                    </td>
                    <td className="whitespace-nowrap px-4 py-3 tabular-nums">{formatBytes(worker.disk_free_bytes)} / {formatBytes(worker.spool_bytes)}</td>
                    <td className="min-w-64 max-w-sm px-4 py-3"><span className="whitespace-pre-wrap break-words text-xs text-destructive">{worker.last_error || '—'}</span></td>
                    <td className="min-w-48 px-4 py-3">
                      <div className="flex flex-wrap gap-2">
                        {workerActions(worker.status).map((verb) => (
                          <Button key={verb} size="sm" variant={verb === 'revoke' || verb === 'reject' ? 'destructive' : 'outline'} disabled={action.isPending} onClick={() => { action.reset(); setSelection({ worker, action: verb }) }}>
                            {labels[verb]}
                          </Button>
                        ))}
                        {(worker.status === 'rejected' || worker.status === 'revoked') && <span className="text-xs text-muted-foreground">{text('Re-enrollment required', '需要重新注册')}</span>}
                      </div>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </StatusBlock>
      </Panel>

      <Panel title={text('Worker jobs & errors', '节点任务与错误')} icon={Loader2} suffix={String(jobs.length)}>
        <StatusBlock loading={snapshot.isLoading} error={snapshot.error?.message ?? ''} empty={!jobs.length} emptyMessage={text('No worker jobs yet.', '暂无节点任务。')}>
          <div className="overflow-x-auto rounded-lg border border-border">
            <table className="w-full text-sm">
              <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                <tr>
                  {[text('Job / identifier', '任务 / 标识'), text('Worker', '节点'), text('State', '状态'), text('Updated', '更新时间'), text('Error', '错误')].map((label) => <th key={label} scope="col" className="px-4 py-2 font-medium">{label}</th>)}
                </tr>
              </thead>
              <tbody className="divide-y divide-border">
                {jobs.map((job) => (
                  <tr key={job.id} className="align-top">
                    <td className="min-w-56 px-4 py-3"><span className="break-all">{job.identifier || '—'}</span><code className="block break-all text-xs text-muted-foreground">{job.id}</code></td>
                    <td className="min-w-40 px-4 py-3"><span>{workers.find((worker) => worker.id === job.worker_id)?.name || '—'}</span><code className="block break-all text-xs text-muted-foreground">{job.worker_id || text('Unassigned', '未分配')}</code></td>
                    <td className="px-4 py-3"><Badge variant={job.error || job.state === 'failed' ? 'destructive' : 'secondary'}>{job.state}</Badge></td>
                    <td className="whitespace-nowrap px-4 py-3 text-muted-foreground">{formatTime(job.updated_at)}</td>
                    <td className="min-w-64 max-w-lg px-4 py-3"><span className="whitespace-pre-wrap break-words text-xs text-destructive">{job.error || '—'}</span></td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </StatusBlock>
      </Panel>

      <Dialog open={selection !== null} onOpenChange={(open) => { if (!open && !action.isPending) setSelection(null) }}>
        <DialogContent showCloseButton={!action.isPending}>
          <DialogHeader>
            <DialogTitle>{selection && `${labels[selection.action]} — ${selection.worker.name || selection.worker.id}`}</DialogTitle>
            <DialogDescription>{selection && descriptions[selection.action]}</DialogDescription>
          </DialogHeader>
          <code className="break-all text-xs text-muted-foreground">{selection?.worker.id}</code>
          {!actionAllowed && <p role="alert" className="text-sm text-destructive">{text('Worker status changed. Close this dialog and refresh before trying again.', '节点状态已变化。请关闭对话框并刷新后重试。')}</p>}
          {action.error && <p role="alert" className="text-sm text-destructive">{action.error.message}</p>}
          <DialogFooter>
            <Button variant="outline" disabled={action.isPending} onClick={() => setSelection(null)}>{text('Cancel', '取消')}</Button>
            <Button variant={selection?.action === 'revoke' || selection?.action === 'reject' ? 'destructive' : 'default'} disabled={action.isPending || !actionAllowed || !!snapshot.error} onClick={() => {
              if (!selection || !actionAllowed) return
              action.mutate({ id: selection.worker.id, action: selection.action }, {
                onSuccess: () => { toast.success(text('Worker updated', '节点已更新')); setSelection(null) },
              })
            }}>
              {action.isPending && <Loader2 className="size-4 animate-spin" />}
              {selection ? labels[selection.action] : text('Confirm', '确认')}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function EnrollmentPanel({ text }: { text: Text }) {
  // Keep the one-time secret out of query/mutation data and browser storage.
  // Leaving this admin-only component or dismissing the token clears it.
  const [enrollment, setEnrollment] = useState<DownloaderEnrollment | null>(null)
  const create = useMutation({
    mutationFn: async () => { setEnrollment(await adminDownloaderEnrollment()) },
    retry: false,
  })

  async function copyToken() {
    if (!enrollment) return
    try {
      await navigator.clipboard.writeText(enrollment.token)
      toast.success(text('Token copied', '令牌已复制'))
    } catch {
      toast.error(text('Clipboard unavailable. Select and copy the token manually.', '剪贴板不可用，请手动选择并复制令牌。'))
    }
  }

  return (
    <Panel title={text('Enroll a worker', '注册工作节点')} icon={KeyRound}>
      <p className="mb-3 text-sm text-muted-foreground">{text('Create a single-use, expiring enrollment token and share it securely with the worker operator. Workers connect outbound; registration still requires your approval before any jobs can be leased.', '创建一次性、有时限的注册令牌，并安全地交给节点运维人员。节点主动向服务器连接；注册后仍须经您批准才能领取任务。')}</p>
      <Button size="sm" disabled={create.isPending || !!enrollment} onClick={() => create.mutate()}>
        {create.isPending ? <Loader2 className="size-4 animate-spin" /> : <KeyRound className="size-4" />}
        {text('Create enrollment token', '创建注册令牌')}
      </Button>
      <Dialog open={enrollment !== null} onOpenChange={(open) => { if (!open) setEnrollment(null) }}>
        <DialogContent className="max-h-[90vh] overflow-y-auto sm:max-w-2xl">
          <DialogHeader>
            <DialogTitle>{text('Save this token now', '请立即保存此令牌')}</DialogTitle>
            <DialogDescription>{text('Shown only here; it cannot be retrieved again. Closing this dialog clears the displayed token but does not revoke it.', '令牌仅在此显示，之后无法再次获取。关闭对话框将清除显示的令牌，但不会使其失效。')}</DialogDescription>
          </DialogHeader>
          {enrollment && <>
            <label htmlFor="worker-enrollment-token" className="block text-sm font-medium">{text('One-time enrollment token', '一次性注册令牌')}</label>
            <Input id="worker-enrollment-token" readOnly autoComplete="off" spellCheck={false} value={enrollment.token} className="font-mono" onFocus={(event) => event.target.select()} />
            <p className="text-sm text-muted-foreground">{text('Expires:', '有效期至：')} {formatTime(enrollment.expires_at)}</p>
            <p className="text-sm">{text('Configure the worker with these environment variables. Replace the server URL and token placeholders; do not commit secrets to source control.', '通过以下环境变量配置节点。请替换服务器地址与令牌占位符，勿将密钥提交到代码仓库。')}</p>
            <pre className="overflow-x-auto rounded-md bg-muted p-3 text-xs"><code>{`DL_WORKER_MASTER_URL=https://your-qatlas-server.example
DL_WORKER_ENROLLMENT_TOKEN=<one-time-token>
DL_WORKER_NAME=downloader-01
DL_WORKER_DATA_DIR=/var/lib/qatlas-downloader
DL_WORKER_CONCURRENCY=2`}</code></pre>
            <p className="text-sm text-muted-foreground">{text('Persist the data directory for worker credentials and spool files. After registration, verify the worker’s identity here and approve it. Enrollment alone does not permit job leasing.', '请持久化数据目录以保存节点凭据与暂存文件。注册后，请在此核实节点身份并批准。仅完成注册并不允许领取任务。')}</p>
          </>}
          <DialogFooter>
            <Button size="sm" variant="outline" onClick={() => void copyToken()}><Copy className="size-4" />{text('Copy token', '复制令牌')}</Button>
            <Button size="sm" onClick={() => setEnrollment(null)}>{text('Done — clear token', '完成 — 清除令牌')}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {create.error && <p role="alert" className="mt-2 text-sm text-destructive">{create.error.message}</p>}
    </Panel>
  )
}

function workerActions(status: string): DownloaderWorkerAction[] {
  switch (status) {
    case 'pending': return ['approve', 'reject']
    case 'approved': return ['drain', 'revoke']
    case 'draining': return ['enable', 'revoke']
    default: return []
  }
}

function WorkerStatus({ status, text }: { status: string; text: Text }) {
  const labels: Record<string, string> = {
    pending: text('Pending approval', '待审批'), approved: text('Approved', '已批准'),
    rejected: text('Rejected', '已拒绝'), revoked: text('Revoked', '已撤销'), draining: text('Draining', '排空中'),
  }
  return <Badge variant={status === 'rejected' || status === 'revoked' ? 'destructive' : status === 'approved' ? 'default' : 'outline'}>{labels[status] ?? status}</Badge>
}

function formatTime(value?: string): string {
  if (!value || value.startsWith('0001-')) return '—'
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}

function formatBytes(value?: number): string {
  if (value == null || !Number.isFinite(value) || value < 0) return '—'
  if (value === 0) return '0 B'
  const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB']
  const index = Math.min(Math.floor(Math.log(value) / Math.log(1024)), units.length - 1)
  return `${(value / 1024 ** index).toLocaleString(undefined, { maximumFractionDigits: 1 })} ${units[index]}`
}
