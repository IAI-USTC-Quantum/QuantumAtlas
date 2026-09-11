import { useEffect, useState } from 'react'
import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { toast } from 'sonner'
import { ArrowLeft, Download, Eye, FileText, Link2 } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Dialog, DialogContent, DialogHeader, DialogTitle } from '@/components/ui/dialog'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { useAdminWhoami, usePaperDetail } from '@/lib/queries'
import {
  adminAssetURL,
  assetDownloadPath,
  assetInlinePath,
  authHeaders,
  fetchAssetBlob,
  saveBlob,
} from '@/lib/api'
import { PaperAcquisition } from '@/components/paper-acquisition'

export const Route = createFileRoute('/$lang/papers/$paperId')({
  component: PaperDetailPage,
})

function PaperDetailPage() {
  const { t } = useTranslation('papers')
  const lang = useLang()
  const { paperId } = Route.useParams()
  const detail = usePaperDetail(paperId || null)

  const paper = detail.data

  return (
    <section className="space-y-5">
      <Link
        to="/$lang/papers/search"
        params={{ lang }}
        className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" />
        {t('detail.back')}
      </Link>

      <StatusBlock
        loading={detail.isLoading}
        error={detail.error?.message ?? ''}
        empty={!paper}
      >
        {paper && (
          <>
            <PageHeader
              eyebrow={t('detail.eyebrow')}
              title={paper.title || paper.paper_ref || paper.paper_id}
              copy={paper.authors?.join(', ')}
            />

            <div className="flex flex-wrap gap-2">
              <Badge
                variant={paper.status === 'ready' ? 'default' : 'secondary'}
              >
                {t(`detail.status.${paper.status}`, {
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
              {paper.openalex_id && (
                <Badge variant="outline" className="font-mono">
                  openalex:{paper.openalex_id}
                </Badge>
              )}
              <Badge variant="outline" className="font-mono text-xs">
                {paper.paper_id}
              </Badge>
            </div>

            <Panel title={t('acquisition.title')} icon={FileText}>
              <PaperAcquisition acquisition={paper.acquisition} />
            </Panel>

            <AdminAssetPreview paperId={paper.paper_id} />

            <Panel
              title={t('detail.assets')}
              icon={FileText}
              suffix={`${paper.assets.length}`}
            >
              {paper.assets.length === 0 ? (
                <p className="text-sm text-muted-foreground">
                  {t('detail.noAssets')}
                </p>
              ) : (
                <div className="overflow-x-auto rounded-xl border border-border">
                  <table className="w-full text-sm">
                    <thead className="bg-muted/40 text-left text-xs uppercase tracking-wide text-muted-foreground">
                      <tr>
                        <th className="px-4 py-2.5 font-medium">{t('detail.cols.source')}</th>
                        <th className="px-4 py-2.5 font-medium">{t('detail.cols.version')}</th>
                        <th className="px-4 py-2.5 font-medium">{t('detail.cols.pdfSize')}</th>
                        <th className="px-4 py-2.5 font-medium">{t('detail.cols.markdown')}</th>
                        <th className="px-4 py-2.5 font-medium">{t('detail.cols.images')}</th>
                        <th className="px-4 py-2.5 font-medium">{t('detail.cols.fetchedAt')}</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border">
                      {paper.assets.map((asset) => (
                        <tr key={asset.asset_id}>
                          <td className="px-4 py-2.5">
                            <Badge variant="secondary">{asset.source}</Badge>
                          </td>
                          <td className="px-4 py-2.5 font-mono text-xs tabular-nums">
                            {asset.source === 'arxiv'
                              ? `v${asset.arxiv_version ?? 0}`
                              : '—'}
                          </td>
                          <td className="px-4 py-2.5 tabular-nums">
                            {formatBytes(asset.pdf_size)}
                          </td>
                          <td className="px-4 py-2.5">
                            {asset.mineru_md_path
                              ? t('detail.yes')
                              : t('detail.no')}
                          </td>
                          <td className="px-4 py-2.5 tabular-nums">
                            {asset.image_count ?? 0}
                          </td>
                          <td className="px-4 py-2.5 text-muted-foreground">
                            {formatDate(asset.fetched_at)}
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </Panel>

            <dl className="grid grid-cols-2 gap-2 text-xs text-muted-foreground sm:grid-cols-3">
              <div>
                <dt>{t('detail.createdAt')}</dt>
                <dd className="mt-0.5 text-foreground">
                  {formatDate(paper.created_at)}
                </dd>
              </div>
              <div>
                <dt>{t('detail.updatedAt')}</dt>
                <dd className="mt-0.5 text-foreground">
                  {formatDate(paper.updated_at)}
                </dd>
              </div>
              {paper.paper_ref && (
                <div className="col-span-2 sm:col-span-1">
                  <dt>{t('detail.paperRef')}</dt>
                  <dd className="mt-0.5 break-all font-mono text-foreground">
                    {paper.paper_ref}
                  </dd>
                </div>
              )}
            </dl>
          </>
        )}
      </StatusBlock>
    </section>
  )
}

function formatBytes(size?: number): string {
  if (!size) return '—'
  const units = ['B', 'KiB', 'MiB', 'GiB']
  let value = size
  let unit = 0
  while (value >= 1024 && unit < units.length - 1) {
    value /= 1024
    unit += 1
  }
  return `${value.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`
}

// AdminAssetPreview shows PDF/Markdown preview + download + presigned URL
// for admins on the paper detail page. Hidden for non-admin users.
function AdminAssetPreview({ paperId }: { paperId: string }) {
  const { t } = useTranslation('papers')
  const whoami = useAdminWhoami()
  const isAdmin = whoami.data?.is_admin ?? false
  const [previewKind, setPreviewKind] = useState<'pdf' | 'markdown' | null>(null)
  const [urlKind, setUrlKind] = useState<'pdf' | 'markdown' | null>(null)
  const [presignedUrl, setPresignedUrl] = useState('')
  const [mdText, setMdText] = useState('')
  const [pdfUrl, setPdfUrl] = useState('')
  const [previewError, setPreviewError] = useState('')

  // Release the blob behind the previous object URL whenever it is
  // replaced or the component goes away.
  useEffect(() => {
    return () => {
      if (pdfUrl.startsWith('blob:')) URL.revokeObjectURL(pdfUrl)
    }
  }, [pdfUrl])

  if (!isAdmin) return null

  // /api/* authenticates via the Authorization bearer header only, so
  // every fetch attaches it explicitly. Preview and download STREAM the
  // bytes through the inline/download proxy endpoints into a blob object
  // URL — <iframe>/<a> navigations cannot carry the header, and presigned
  // object-store URLs would point at the internal endpoint unless
  // s3.public_endpoint is configured.
  const fetchMd = async (kind: string) => {
    try {
      const resp = await fetch(assetInlinePath(paperId, kind), {
        headers: { ...authHeaders() },
      })
      if (!resp.ok) throw new Error(`${resp.status} ${resp.statusText}`)
      setMdText(await resp.text())
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : String(err))
    }
  }

  const fetchPdfBlob = async () => {
    try {
      const blob = await fetchAssetBlob(assetInlinePath(paperId, 'pdf'))
      setPdfUrl(URL.createObjectURL(blob))
    } catch (err) {
      setPreviewError(err instanceof Error ? err.message : String(err))
    }
  }

  const openPreview = (kind: 'pdf' | 'markdown') => {
    setPreviewKind(kind)
    setPreviewError('')
    setMdText('')
    setPdfUrl('')
    if (kind === 'markdown') {
      void fetchMd(kind)
    } else {
      void fetchPdfBlob()
    }
  }

  const download = async (kind: 'pdf' | 'markdown') => {
    try {
      const blob = await fetchAssetBlob(assetDownloadPath(paperId, kind))
      saveBlob(blob, `${paperId}.${kind === 'pdf' ? 'pdf' : 'md'}`)
    } catch (err) {
      toast.error(err instanceof Error ? err.message : String(err))
    }
  }

  const copyUrl = async (kind: 'pdf' | 'markdown') => {
    try {
      const res = await adminAssetURL(paperId, kind)
      setPresignedUrl(res.url)
      setUrlKind(kind)
      await navigator.clipboard.writeText(res.url)
    } catch {
      // show error state
    }
  }

  const kinds = ['pdf', 'markdown'] as const

  return (
    <Panel title={t('adminAssets.title')} icon={Eye}>
      <div className="flex flex-wrap gap-3">
        {kinds.map((kind) => (
          <div key={kind} className="flex items-center gap-1.5">
            <Button variant="outline" size="sm" onClick={() => openPreview(kind)}>
              <Eye className="size-3.5" />
              {t(`adminAssets.preview${kind === 'pdf' ? 'Pdf' : 'Md'}`)}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void download(kind)}
            >
              <Download className="size-3.5" />
              {kind === 'pdf' ? 'PDF' : 'MD'}
            </Button>
            <Button
              variant="outline"
              size="sm"
              onClick={() => void copyUrl(kind)}
              title={t('adminAssets.copyUrlHint')}
            >
              <Link2 className="size-3.5" />
              {t('adminAssets.copyUrl')}
            </Button>
          </div>
        ))}
      </div>

      {previewKind && (
        <Dialog open onOpenChange={() => setPreviewKind(null)}>
          <DialogContent className="max-w-4xl">
            <DialogHeader>
              <DialogTitle>
                {previewKind === 'pdf'
                  ? t('adminAssets.pdfPreview')
                  : t('adminAssets.mdPreview')}
              </DialogTitle>
            </DialogHeader>
            {previewError ? (
              <p className="text-sm text-destructive">{previewError}</p>
            ) : previewKind === 'pdf' ? (
              pdfUrl ? (
                <iframe
                  src={pdfUrl}
                  className="h-[600px] w-full rounded-md border border-border"
                  title="PDF Preview"
                />
              ) : (
                <p className="text-sm text-muted-foreground">
                  {t('adminAssets.loading')}
                </p>
              )
            ) : (
              <pre className="max-h-[600px] overflow-auto rounded-md border border-border bg-muted/30 p-4 text-sm leading-relaxed">
                {mdText || t('adminAssets.loading')}
              </pre>
            )}
          </DialogContent>
        </Dialog>
      )}

      {urlKind && presignedUrl && (
        <Dialog open onOpenChange={() => { setUrlKind(null); setPresignedUrl('') }}>
          <DialogContent className="max-w-2xl">
            <DialogHeader>
              <DialogTitle>{t('adminAssets.presignedUrl')}</DialogTitle>
            </DialogHeader>
            <code className="break-all rounded-md bg-muted px-3 py-2 text-xs">
              {presignedUrl}
            </code>
            <p className="text-xs text-muted-foreground">
              {t('adminAssets.urlCopied')}
            </p>
          </DialogContent>
        </Dialog>
      )}
    </Panel>
  )
}

function formatDate(value?: string): string {
  if (!value) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString()
}
