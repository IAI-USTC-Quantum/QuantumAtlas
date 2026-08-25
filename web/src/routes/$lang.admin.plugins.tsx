import { useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { useQueryClient } from '@tanstack/react-query'
import { toast } from 'sonner'
import { AlertCircle, ArrowLeft, Puzzle, RefreshCcw } from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
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
import type {
  AdminPluginConfigResult,
  AdminPluginManifest,
  AdminPluginManifestField,
  PluginSummary,
} from '@/lib/api'
import {
  useAdminPluginManifest,
  useAdminPluginSaveConfig,
  useAdminPlugins,
  useAdminWhoami,
} from '@/lib/queries'

export const Route = createFileRoute('/$lang/admin/plugins')({
  component: AdminPluginsPage,
})

function AdminPluginsPage() {
  const { t } = useTranslation('admin')
  const { lang } = Route.useParams()
  const whoami = useAdminWhoami()
  const isAdmin = whoami.data?.is_admin ?? false
  const plugins = useAdminPlugins(isAdmin)

  return (
    <section className="space-y-5">
      <div className="space-y-2">
        <Button asChild variant="ghost" size="sm" className="-ml-2">
          <Link to="/$lang/admin" params={{ lang }}>
            <ArrowLeft className="size-4" /> {t('plugins.backToAdmin')}
          </Link>
        </Button>
        <PageHeader
          eyebrow={t('eyebrow')}
          title={t('plugins.title')}
          copy={t('plugins.subtitle')}
        />
      </div>

      <StatusBlock loading={whoami.isLoading} error="" empty={false}>
        {/* Same session gate as the main admin page: a whoami failure means
            no browser session, so offer sign-in instead of a raw error. */}
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
        ) : whoami.data && !isAdmin ? (
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
          <div className="space-y-8">
            <Panel title={t('plugins.loaded')} icon={Puzzle}>
              <StatusBlock
                loading={plugins.isLoading}
                error={plugins.error?.message ?? ''}
                empty={
                  !plugins.isLoading && !plugins.data?.plugins.length
                }
                emptyMessage={t('plugins.empty')}
              >
                <PluginsTable plugins={plugins.data?.plugins ?? []} />
              </StatusBlock>
            </Panel>

            <div className="space-y-4">
              <h2 className="text-sm font-medium uppercase tracking-wide text-muted-foreground">
                {t('plugins.config.heading')}
              </h2>
              {(plugins.data?.plugins ?? []).map((plugin) => (
                <PluginConfigCard key={plugin.id} plugin={plugin} />
              ))}
            </div>
          </div>
        )}
      </StatusBlock>
    </section>
  )
}

// --- Loaded plugins table ---------------------------------------------------

function StatusBadge({ status }: { status: string }) {
  const { t } = useTranslation('admin')
  // connected = green, disconnected/disabled = gray, incompatible = red.
  const style =
    status === 'connected'
      ? 'border-green-600/40 bg-green-600/10 text-green-700 dark:text-green-400'
      : status === 'incompatible'
        ? 'border-destructive/40 bg-destructive/10 text-destructive'
        : 'border-border bg-muted text-muted-foreground'
  return (
    <Badge variant="outline" className={style}>
      {t(`plugins.status.${status}`, { defaultValue: status })}
    </Badge>
  )
}

function PluginsTable({ plugins }: { plugins: PluginSummary[] }) {
  const { t } = useTranslation('admin')

  return (
    <div className="overflow-x-auto rounded-lg border border-border">
      <table className="w-full text-sm">
        <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
          <tr>
            <th className="px-4 py-2 font-medium">{t('plugins.cols.name')}</th>
            <th className="px-4 py-2 font-medium">{t('plugins.cols.id')}</th>
            <th className="px-4 py-2 font-medium">
              {t('plugins.cols.version')}
            </th>
            <th className="px-4 py-2 font-medium">{t('plugins.cols.kind')}</th>
            <th className="px-4 py-2 font-medium">
              {t('plugins.cols.transport')}
            </th>
            <th className="px-4 py-2 font-medium">
              {t('plugins.cols.status')}
            </th>
            <th className="px-4 py-2 font-medium">{t('plugins.cols.error')}</th>
            <th className="px-4 py-2 font-medium">
              {t('plugins.cols.capabilities')}
            </th>
            <th className="px-4 py-2 font-medium">{t('plugins.cols.needs')}</th>
          </tr>
        </thead>
        <tbody className="divide-y divide-border">
          {plugins.map((plugin) => (
            <tr key={plugin.id}>
              <td className="px-4 py-2 font-medium">
                {plugin.name || plugin.id}
              </td>
              <td className="px-4 py-2">
                <code className="text-xs">{plugin.id}</code>
              </td>
              <td className="px-4 py-2 font-mono text-xs text-muted-foreground">
                {plugin.version ?? '—'}
              </td>
              <td className="px-4 py-2 text-muted-foreground">
                {plugin.kind ?? '—'}
              </td>
              <td className="px-4 py-2 text-muted-foreground">
                {plugin.transport ?? '—'}
              </td>
              <td className="px-4 py-2">
                <StatusBadge status={plugin.status} />
              </td>
              <td className="max-w-64 px-4 py-2 text-xs text-destructive">
                {plugin.error || '—'}
              </td>
              <td className="px-4 py-2">
                <TagList values={plugin.contributes?.capabilities} />
              </td>
              <td className="px-4 py-2">
                <TagList values={plugin.needs} />
              </td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  )
}

function TagList({ values }: { values?: string[] }) {
  if (!values?.length) {
    return <span className="text-muted-foreground">—</span>
  }
  return (
    <span className="flex flex-wrap gap-1">
      {values.map((value) => (
        <Badge key={value} variant="secondary" className="font-mono text-xs">
          {value}
        </Badge>
      ))}
    </span>
  )
}

// --- Per-plugin config card --------------------------------------------------
//
// Every plugin is probed for an admin manifest. A 404 means "this plugin
// has no admin page" (a one-line note, not an error); 503 means the
// plugin is unreachable. A fetched manifest renders as a config form.

function PluginConfigCard({ plugin }: { plugin: PluginSummary }) {
  const { t } = useTranslation('admin')
  const manifest = useAdminPluginManifest(plugin.id, true)

  if (manifest.isLoading) {
    return (
      <StatusBlock loading error="" empty={false}>
        {null}
      </StatusBlock>
    )
  }

  if (manifest.error || !manifest.data) {
    const message = manifest.error?.message ?? ''
    const unreachable = message.startsWith('503')
    return (
      <Panel
        title={plugin.name || plugin.id}
        icon={Puzzle}
        suffix={plugin.version}
      >
        <p className="text-sm text-muted-foreground">
          {unreachable
            ? t('plugins.config.unreachable')
            : t('plugins.config.noAdminPage')}
        </p>
      </Panel>
    )
  }

  return <PluginConfigForm plugin={plugin} manifest={manifest.data} />
}

function fieldInitial(field: AdminPluginManifestField): string {
  if (field.secret) return ''
  if (field.value === null || field.value === undefined) return ''
  return String(field.value)
}

function PluginConfigForm({
  plugin,
  manifest,
}: {
  plugin: PluginSummary
  manifest: AdminPluginManifest
}) {
  const { t } = useTranslation('admin')
  const qc = useQueryClient()
  const save = useAdminPluginSaveConfig(plugin.id)
  // Only user edits live here; the rendered value falls back to the
  // manifest's current value, so a refetch after save resets cleanly.
  const [edits, setEdits] = useState<Record<string, string>>({})
  const [result, setResult] = useState<AdminPluginConfigResult | null>(null)

  const fields = manifest.sections.flatMap((section) => section.fields)

  function currentValue(field: AdminPluginManifestField): string {
    return edits[field.key] ?? fieldInitial(field)
  }

  function isDirty(field: AdminPluginManifestField): boolean {
    const edited = edits[field.key]
    if (edited === undefined) return false
    // Secret fields: empty means "leave unchanged".
    if (field.secret) return edited.trim() !== ''
    return edited !== fieldInitial(field)
  }

  function buildUpdates(): Record<string, unknown> {
    const updates: Record<string, unknown> = {}
    for (const field of fields) {
      if (!isDirty(field)) continue
      const raw = currentValue(field)
      switch (field.type) {
        case 'int':
          updates[field.key] = parseInt(raw, 10)
          break
        case 'float':
          updates[field.key] = parseFloat(raw)
          break
        case 'bool':
          updates[field.key] = raw === 'true'
          break
        default:
          updates[field.key] = raw
      }
    }
    return updates
  }

  const updates = buildUpdates()
  const dirtyCount = Object.keys(updates).length
  const valid = fields.every(
    (field) =>
      !isDirty(field) ||
      (field.type !== 'int' && field.type !== 'float') ||
      !Number.isNaN(
        field.type === 'int'
          ? parseInt(currentValue(field), 10)
          : parseFloat(currentValue(field)),
      ),
  )

  return (
    <Panel
      title={manifest.title || plugin.name || plugin.id}
      icon={Puzzle}
      suffix={manifest.version ?? plugin.version}
    >
      <div className="space-y-6">
        {manifest.status && (
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <Badge
              variant="outline"
              className={
                manifest.status.agent_configured
                  ? 'border-green-600/40 bg-green-600/10 text-green-700 dark:text-green-400'
                  : 'border-border bg-muted text-muted-foreground'
              }
            >
              {manifest.status.agent_configured
                ? t('plugins.config.agentConfigured')
                : t('plugins.config.agentNotConfigured')}
            </Badge>
            {Object.entries(manifest.status.backends ?? {}).map(
              ([name, ok]) => (
                <Badge
                  key={name}
                  variant="outline"
                  className={
                    ok
                      ? 'border-green-600/40 bg-green-600/10 text-green-700 dark:text-green-400'
                      : 'border-border bg-muted text-muted-foreground'
                  }
                >
                  {name}: {ok ? '✓' : '—'}
                </Badge>
              ),
            )}
          </div>
        )}

        {manifest.sections.map((section) => (
          <div key={section.key} className="space-y-3">
            <div>
              <h3 className="text-sm font-semibold">{section.title}</h3>
              {section.description && (
                <p className="text-sm text-muted-foreground">
                  {section.description}
                </p>
              )}
            </div>
            <div className="grid gap-4 sm:grid-cols-2">
              {section.fields.map((field) => (
                <ConfigField
                  key={field.key}
                  field={field}
                  value={currentValue(field)}
                  onChange={(value) => {
                    setResult(null)
                    setEdits((prev) => ({ ...prev, [field.key]: value }))
                  }}
                />
              ))}
            </div>
          </div>
        ))}

        {result && (
          <Alert>
            {result.restart_required ? (
              <RefreshCcw className="size-4" />
            ) : (
              <AlertCircle className="size-4" />
            )}
            <AlertTitle>{t('plugins.config.appliedTitle')}</AlertTitle>
            <AlertDescription>
              <span className="block">
                {t('plugins.config.applied', {
                  keys: result.applied.join(', '),
                })}
              </span>
              {result.restart_required && (
                <span className="block">{t('plugins.config.restartHint')}</span>
              )}
            </AlertDescription>
          </Alert>
        )}

        <div className="flex items-center gap-2">
          <Button
            size="sm"
            disabled={dirtyCount === 0 || !valid || save.isPending}
            onClick={() =>
              save.mutate(updates, {
                onSuccess: (data) => {
                  setEdits({})
                  setResult(data)
                  toast.success(t('plugins.config.saved'))
                  void qc.invalidateQueries({
                    queryKey: ['admin-plugin-manifest', plugin.id],
                  })
                },
              })
            }
          >
            {t('plugins.config.save')}
          </Button>
          {save.error && (
            <span className="text-xs text-destructive">
              {save.error.message}
            </span>
          )}
        </div>
      </div>
    </Panel>
  )
}

function ConfigField({
  field,
  value,
  onChange,
}: {
  field: AdminPluginManifestField
  value: string
  onChange: (value: string) => void
}) {
  const { t } = useTranslation('admin')
  const inputId = `cfg-${field.key}`

  return (
    <div className="space-y-1.5">
      <Label htmlFor={inputId}>{field.label}</Label>
      {field.type === 'bool' ? (
        <Select value={value || 'false'} onValueChange={onChange}>
          <SelectTrigger id={inputId} className="w-full">
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="true">true</SelectItem>
            <SelectItem value="false">false</SelectItem>
          </SelectContent>
        </Select>
      ) : field.secret ? (
        <Input
          id={inputId}
          type="password"
          value={value}
          placeholder={
            typeof field.value === 'string' && field.value
              ? field.value
              : t('plugins.config.secretUnset')
          }
          onChange={(e) => onChange(e.target.value)}
        />
      ) : (
        <Input
          id={inputId}
          type={field.type === 'int' || field.type === 'float' ? 'number' : 'text'}
          value={value}
          onChange={(e) => onChange(e.target.value)}
        />
      )}
      {field.description && (
        <p className="text-xs text-muted-foreground">{field.description}</p>
      )}
      {field.secret && (
        <p className="text-xs text-muted-foreground">
          {t('plugins.config.secretHint')}
        </p>
      )}
    </div>
  )
}
