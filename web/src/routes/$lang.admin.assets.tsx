import { useEffect, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useQuery, type UseQueryResult } from '@tanstack/react-query'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import {
  AlertCircle,
  ArrowLeft,
  ChevronRight,
  Clipboard,
  Download,
  Eye,
  Link2,
  Search,
} from 'lucide-react'

import { Alert, AlertDescription, AlertTitle } from '@/components/ui/alert'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Card } from '@/components/ui/card'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { PageHeader } from '@/components/page-header'
import { StatusBlock } from '@/components/status-block'
import {
  adminAssetURL,
  authHeaders,
  type AdminAssetEntry,
  type AdminAssetSearchResponse,
  type AdminAssetURLResponse,
} from '@/lib/api'
import {
  useAdminAssetList,
  useAdminAssetSearch,
  useAdminWhoami,
} from '@/lib/queries'

export const Route = createFileRoute('/$lang/admin/assets')({
  component: AdminAssetsPage,
})

type AssetSearchHit = AdminAssetSearchResponse['papers'][number]
type PreviewTarget = { paperId: string; kind: AdminAssetEntry['kind'] }
type URLTarget = { paperId: string; entry: AdminAssetEntry }

const SEARCH_DEBOUNCE_MS = 500

function useDebouncedValue<T>(value: T, delayMs: number): T {
  const [debounced, setDebounced] = useState(value)
  useEffect(() => {
    const timer = setTimeout(() => setDebounced(value), delayMs)
    return () => clearTimeout(timer)
  }, [value, delayMs])
  return debounced
}

function assetInlineURL(paperId: string, kind: string): string {
  return `/api/admin/assets/${encodeURIComponent(paperId)}/${encodeURIComponent(kind)}/inline`
}

// Text fetch for the markdown preview. /api/* authenticates via the
// Authorization bearer header only, so attach it explicitly.
async function fetchAssetText(url: string): Promise<string> {
  const response = await fetch(url, { headers: { ...authHeaders() } })
  if (!response.ok) {
    throw new Error(`${response.status} ${response.statusText}`)
  }
  return response.text()
}

function AdminAssetsPage() {
  const { t } = useTranslation('admin')
  const { lang } = Route.useParams()
  const whoami = useAdminWhoami()
  const isAdmin = whoami.data?.is_admin ?? false

  const [input, setInput] = useState('')
  const query = useDebouncedValue(input.trim(), SEARCH_DEBOUNCE_MS)
  const results = useAdminAssetSearch(query, isAdmin)

  const [selectedId, setSelectedId] = useState<string | null>(null)
  const [preview, setPreview] = useState<PreviewTarget | null>(null)
  const [urlTarget, setUrlTarget] = useState<URLTarget | null>(null)

  // Markdown previews stream through the inline endpoint as text (the
  // fetch carries the bearer header); PDFs render in an <iframe>, which
  // cannot send headers, so they use a presigned object-store URL.
  const previewText = useQuery({
    queryKey: ['admin-asset-preview', preview?.paperId, preview?.kind],
    queryFn: () => fetchAssetText(assetInlineURL(preview!.paperId, preview!.kind)),
    enabled: preview !== null && preview.kind === 'markdown',
    retry: false,
  })

  const previewURL = useQuery({
    queryKey: ['admin-asset-preview-url', preview?.paperId, preview?.kind],
    queryFn: () => adminAssetURL(preview!.paperId, preview!.kind),
    enabled: preview !== null && preview.kind === 'pdf',
    retry: false,
  })

  const presignedURL = useQuery({
    queryKey: ['admin-asset-url', urlTarget?.paperId, urlTarget?.entry.kind],
    queryFn: () => adminAssetURL(urlTarget!.paperId, urlTarget!.entry.kind),
    enabled: urlTarget !== null,
    retry: false,
  })

  async function copy(text: string) {
    await navigator.clipboard.writeText(text)
    toast.success(t('assets.copied'))
  }

  return (
    <section className="space-y-5">
      <div className="space-y-2">
        <Button asChild variant="ghost" size="sm" className="-ml-2">
          <Link to="/$lang/admin" params={{ lang }}>
            <ArrowLeft className="size-4" /> {t('assets.backToAdmin')}
          </Link>
        </Button>
        <PageHeader
          eyebrow={t('eyebrow')}
          title={t('assets.title')}
          copy={t('assets.subtitle')}
        />
      </div>

      <StatusBlock loading={whoami.isLoading} error="" empty={false}>
        {/* Same session gate as the other admin pages. */}
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
          <div className="space-y-5">
            <div className="relative">
              <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-muted-foreground" />
              <Input
                value={input}
                onChange={(event) => setInput(event.target.value)}
                placeholder={t('assets.searchPlaceholder')}
                aria-label={t('assets.searchPlaceholder')}
                className="pl-9"
              />
            </div>

            {!query ? (
              <p className="text-sm text-muted-foreground">
                {t('assets.searchHint')}
              </p>
            ) : (
              <StatusBlock
                loading={results.isLoading}
                error={results.error?.message ?? ''}
                empty={
                  !results.isLoading &&
                  !results.error &&
                  (results.data?.papers.length ?? 0) === 0
                }
                emptyMessage={t('assets.noResults')}
              >
                <div className="space-y-3">
                  <p className="text-sm text-muted-foreground">
                    {t('assets.resultsCount', {
                      count: results.data?.papers.length ?? 0,
                    })}
                  </p>
                  {results.data?.papers.map((paper) => (
                    <PaperCard
                      key={paper.paper_id}
                      paper={paper}
                      selected={selectedId === paper.paper_id}
                      onToggle={() =>
                        setSelectedId((current) =>
                          current === paper.paper_id ? null : paper.paper_id,
                        )
                      }
                      onPreview={setPreview}
                      onCopyURL={(entry) =>
                        setUrlTarget({ paperId: paper.paper_id, entry })
                      }
                    />
                  ))}
                </div>
              </StatusBlock>
            )}
          </div>
        )}
      </StatusBlock>

      <PreviewDialog
        preview={preview}
        text={previewText}
        url={previewURL}
        onClose={() => setPreview(null)}
      />

      <URLDialog
        target={urlTarget}
        query={presignedURL}
        onCopy={copy}
        onClose={() => setUrlTarget(null)}
      />
    </section>
  )
}

// One search hit: a collapsible card. Expanding it mounts the asset
// table via useAdminAssetList (the fetch only runs while selected).
function PaperCard({
  paper,
  selected,
  onToggle,
  onPreview,
  onCopyURL,
}: {
  paper: AssetSearchHit
  selected: boolean
  onToggle: () => void
  onPreview: (target: PreviewTarget) => void
  onCopyURL: (entry: AdminAssetEntry) => void
}) {
  const { t } = useTranslation('admin')
  const { t: tp } = useTranslation('papers')
  const assets = useAdminAssetList(selected ? paper.paper_id : null)

  return (
    <Card
      className={`py-0 transition-colors ${selected ? 'border-primary/50' : ''}`}
    >
      <button
        type="button"
        onClick={onToggle}
        aria-expanded={selected}
        className="flex w-full cursor-pointer items-center gap-3 px-5 py-4 text-left"
      >
        <ChevronRight
          className={`size-4 shrink-0 text-muted-foreground transition-transform ${selected ? 'rotate-90' : ''}`}
        />
        <span className="min-w-0 flex-1">
          <span className="line-clamp-2 font-medium">
            {paper.title || paper.arxiv_id || paper.doi || paper.paper_id}
          </span>
          <code className="mt-0.5 block truncate text-xs text-muted-foreground">
            {paper.paper_id}
          </code>
        </span>
        <span className="flex shrink-0 flex-wrap items-center justify-end gap-1.5">
          <Badge variant={paper.status === 'ready' ? 'default' : 'secondary'}>
            {tp(`detail.status.${paper.status}`, {
              defaultValue: paper.status,
            })}
          </Badge>
          {paper.arxiv_id && (
            <Badge variant="outline" className="font-mono">
              arXiv:{paper.arxiv_id}
            </Badge>
          )}
          {paper.doi && (
            <Badge variant="outline" className="font-mono">
              doi:{paper.doi}
            </Badge>
          )}
        </span>
      </button>

      {selected && (
        <div className="border-t border-border px-5 py-4">
          <StatusBlock
            loading={assets.isLoading}
            error={assets.error?.message ?? ''}
            empty={
              !assets.isLoading &&
              !assets.error &&
              (assets.data?.assets.length ?? 0) === 0
            }
            emptyMessage={t('assets.noAssets')}
          >
            {assets.data && (
              <div className="overflow-x-auto rounded-lg border border-border">
                <table className="w-full text-sm">
                  <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                    <tr>
                      <th className="px-4 py-2 font-medium">
                        {t('assets.cols.kind')}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t('assets.cols.objectKey')}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t('assets.cols.size')}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t('assets.cols.sha256')}
                      </th>
                      <th className="px-4 py-2 font-medium">
                        {t('assets.cols.actions')}
                      </th>
                    </tr>
                  </thead>
                  <tbody className="divide-y divide-border">
                    {assets.data.assets.map((entry) => (
                      <AssetRow
                        key={`${entry.kind}/${entry.object_key}`}
                        paperId={paper.paper_id}
                        entry={entry}
                        onPreview={onPreview}
                        onCopyURL={onCopyURL}
                      />
                    ))}
                  </tbody>
                </table>
              </div>
            )}
          </StatusBlock>
        </div>
      )}
    </Card>
  )
}

function AssetRow({
  paperId,
  entry,
  onPreview,
  onCopyURL,
}: {
  paperId: string
  entry: AdminAssetEntry
  onPreview: (target: PreviewTarget) => void
  onCopyURL: (entry: AdminAssetEntry) => void
}) {
  const { t } = useTranslation('admin')
  const [downloading, setDownloading] = useState(false)

  async function download() {
    setDownloading(true)
    try {
      const { url } = await adminAssetURL(paperId, entry.kind)
      window.open(url, '_blank', 'noopener,noreferrer')
    } catch (error) {
      toast.error(error instanceof Error ? error.message : String(error))
    } finally {
      setDownloading(false)
    }
  }

  return (
    <tr className="align-top">
      <td className="whitespace-nowrap px-4 py-2">
        <Badge variant="outline" className="font-mono">
          {entry.kind}
        </Badge>
      </td>
      <td className="min-w-72 px-4 py-2">
        <code className="block text-xs break-all">{entry.object_key}</code>
        {entry.content_type && (
          <span className="text-xs text-muted-foreground">
            {entry.content_type}
          </span>
        )}
      </td>
      <td className="whitespace-nowrap px-4 py-2 tabular-nums text-muted-foreground">
        {formatSize(entry.size)}
      </td>
      <td className="whitespace-nowrap px-4 py-2">
        {entry.sha256 ? (
          <code className="font-mono text-xs" title={entry.sha256}>
            {entry.sha256.slice(0, 12)}
          </code>
        ) : (
          <span className="text-muted-foreground">—</span>
        )}
      </td>
      <td className="whitespace-nowrap px-4 py-2">
        <div className="flex flex-wrap items-center gap-2">
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => onPreview({ paperId, kind: entry.kind })}
          >
            <Eye className="size-3.5" /> {t('assets.preview')}
          </Button>
          {/* Browser navigations can't carry the bearer header, so the
              download goes through a freshly minted presigned URL. */}
          <Button
            type="button"
            size="sm"
            variant="outline"
            disabled={downloading}
            onClick={() => void download()}
          >
            <Download className="size-3.5" /> {t('assets.download')}
          </Button>
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => onCopyURL(entry)}
          >
            <Link2 className="size-3.5" /> {t('assets.copyURL')}
          </Button>
        </div>
      </td>
    </tr>
  )
}

// Preview dialog: PDFs render in an <iframe> pointed at a presigned
// object-store URL; markdown is fetched as text (with the bearer header)
// and shown verbatim in a <pre>.
function PreviewDialog({
  preview,
  text,
  url,
  onClose,
}: {
  preview: PreviewTarget | null
  text: UseQueryResult<string, Error>
  url: UseQueryResult<AdminAssetURLResponse, Error>
  onClose: () => void
}) {
  const { t } = useTranslation('admin')

  return (
    <Dialog open={preview !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-3xl">
        <DialogHeader>
          <DialogTitle>
            {t('assets.previewTitle', { kind: preview?.kind ?? '' })}
          </DialogTitle>
          <DialogDescription className="font-mono break-all">
            {preview?.paperId}
          </DialogDescription>
        </DialogHeader>
        {preview?.kind === 'pdf' ? (
          <StatusBlock
            loading={url.isLoading}
            error={url.error?.message ?? ''}
            empty={false}
          >
            {url.data && (
              <iframe
                src={url.data.url}
                title={t('assets.previewTitle', { kind: preview.kind })}
                className="w-full rounded-md border border-border"
                style={{ height: 600 }}
              />
            )}
          </StatusBlock>
        ) : preview ? (
          <StatusBlock
            loading={text.isLoading}
            error={text.error?.message ?? ''}
            empty={false}
          >
            <pre className="max-h-[600px] overflow-auto rounded-md border border-border bg-muted/30 p-4 text-xs leading-5 break-words whitespace-pre-wrap">
              {text.data}
            </pre>
          </StatusBlock>
        ) : null}
      </DialogContent>
    </Dialog>
  )
}

// Presigned-URL dialog: mints a 1h direct object-store URL and shows it
// in a copyable field alongside the full S3 key.
function URLDialog({
  target,
  query,
  onCopy,
  onClose,
}: {
  target: URLTarget | null
  query: UseQueryResult<AdminAssetURLResponse, Error>
  onCopy: (text: string) => Promise<void>
  onClose: () => void
}) {
  const { t } = useTranslation('admin')
  const { t: tc } = useTranslation('common')

  return (
    <Dialog open={target !== null} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="sm:max-w-xl">
        <DialogHeader>
          <DialogTitle>{t('assets.urlTitle')}</DialogTitle>
          <DialogDescription>{t('assets.urlDescription')}</DialogDescription>
        </DialogHeader>
        <StatusBlock
          loading={query.isLoading}
          error={query.error?.message ?? ''}
          empty={false}
        >
          {query.data && (
            <div className="space-y-3">
              <div className="rounded-md border border-border bg-muted/60 px-3 py-2.5 font-mono text-xs break-all">
                {query.data.url}
              </div>
              <div className="flex flex-wrap items-center gap-2">
                <Button
                  type="button"
                  size="sm"
                  onClick={() => void onCopy(query.data!.url)}
                >
                  <Clipboard className="size-3.5" /> {tc('actions.copy')}
                </Button>
                <Button
                  type="button"
                  size="sm"
                  variant="secondary"
                  onClick={() => void onCopy(query.data!.object_key)}
                >
                  <Clipboard className="size-3.5" /> {t('assets.copyS3Key')}
                </Button>
              </div>
              <p className="text-xs text-muted-foreground">
                {t('assets.expiresAt', {
                  time: formatExpiry(query.data.expires_at),
                })}
              </p>
            </div>
          )}
        </StatusBlock>
      </DialogContent>
    </Dialog>
  )
}

function formatSize(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 B'
  const units = ['B', 'KB', 'MB', 'GB', 'TB']
  let value = bytes
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

function formatExpiry(value: string): string {
  const date = new Date(value)
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString()
}
