import { createFileRoute, Link, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Activity, FileText, Search } from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { MetricGrid } from '@/components/metric-grid'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { usePaperStats, usePapersList } from '@/lib/queries'

export const Route = createFileRoute('/$lang/')({
  component: HomePage,
})

function HomePage() {
  const { t } = useTranslation('home')
  const lang = useLang()
  const navigate = useNavigate()
  const paperStats = usePaperStats()
  const converted = usePapersList({ has_md: true, per_page: 8, page: 1 })
  const convertedItems = converted.data?.items ?? []

  return (
    <section className="space-y-6">
      <div className="rounded-2xl border border-border bg-gradient-to-br from-primary/10 via-background to-accent/30 p-6 sm:p-8">
        <div className="flex flex-col gap-4 lg:flex-row lg:items-end lg:justify-between">
          <div className="max-w-2xl space-y-2">
            <p className="text-xs font-medium uppercase tracking-[0.18em] text-primary">
              {t('eyebrow')}
            </p>
            <h1 className="text-3xl font-semibold tracking-tight text-foreground sm:text-4xl">
              {t('title')}
            </h1>
            <p className="text-sm text-muted-foreground sm:text-base">
              {t('subtitle')}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Button
              onClick={() =>
                navigate({ to: '/$lang/papers/search', params: { lang } })
              }
            >
              <Search className="size-4" /> {t('ctaSearch')}
            </Button>
          </div>
        </div>
      </div>

      {paperStats.data?.available ? (
        <MetricGrid
          paperStats={paperStats.data}
          loading={paperStats.isLoading}
        />
      ) : (
        !paperStats.isLoading && (
          <Panel title={t('registryStats')} icon={Activity}>
            <p className="text-sm text-muted-foreground">
              {t('registryUnavailable')}
            </p>
          </Panel>
        )
      )}

      <Panel
        title={t('convertedPapers')}
        icon={FileText}
        suffix={
          <Link
            to="/$lang/papers"
            params={{ lang }}
            className="text-primary hover:underline"
          >
            {t('viewAll')}
          </Link>
        }
      >
        <StatusBlock
          loading={converted.isLoading}
          error={converted.error?.message ?? ''}
          empty={!converted.isLoading && convertedItems.length === 0}
          emptyMessage={t('convertedPapersEmpty')}
        >
          <div className="grid gap-3 sm:grid-cols-2">
            {convertedItems.map((paper) => (
              <div
                key={paper.paper_id}
                className="rounded-xl border border-border bg-muted/20 p-4"
              >
                <Link
                  to="/$lang/papers/$paperId"
                  params={{ lang, paperId: paper.paper_id }}
                  className="line-clamp-2 font-medium text-foreground hover:text-primary hover:underline"
                  title={paper.title || paper.arxiv_id || paper.paper_id}
                >
                  {paper.title || paper.arxiv_id || paper.paper_id}
                </Link>
                <div className="mt-2 flex items-center justify-between gap-2 text-xs text-muted-foreground">
                  {paper.arxiv_id ? (
                    <Badge variant="secondary" className="font-mono">
                      {paper.arxiv_id}
                    </Badge>
                  ) : (
                    <span />
                  )}
                  <span>{formatDate(paper.created_at)}</span>
                </div>
              </div>
            ))}
          </div>
        </StatusBlock>
      </Panel>

      <Panel title={t('quickActions')} icon={Activity}>
        <div className="space-y-3">
          <Button
            variant="outline"
            className="w-full justify-start"
            onClick={() =>
              navigate({ to: '/$lang/papers/search', params: { lang } })
            }
          >
            <Search className="size-4" /> {t('searchPapers')}
          </Button>
        </div>
      </Panel>
    </section>
  )
}

function formatDate(value?: string): string {
  if (!value) return '—'
  const parsed = new Date(value)
  if (Number.isNaN(parsed.getTime())) return value
  return parsed.toLocaleDateString()
}
