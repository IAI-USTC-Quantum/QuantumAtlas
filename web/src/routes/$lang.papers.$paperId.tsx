import { createFileRoute, Link } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { ArrowLeft, FileText } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { usePaperDetail } from '@/lib/queries'
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

function formatDate(value?: string): string {
  if (!value) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleString()
}
