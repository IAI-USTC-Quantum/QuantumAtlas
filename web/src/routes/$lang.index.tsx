import { createFileRoute, useNavigate } from '@tanstack/react-router'
import { useTranslation } from 'react-i18next'
import { Activity, BookOpen, FileText, Search } from 'lucide-react'

import { Button } from '@/components/ui/button'
import { MetricGrid } from '@/components/metric-grid'
import { PageListItem } from '@/components/page-list-item'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { usePages, usePaperStats, useStats } from '@/lib/queries'

export const Route = createFileRoute('/$lang/')({
  component: HomePage,
})

function HomePage() {
  const { t } = useTranslation('home')
  const lang = useLang()
  const navigate = useNavigate()
  const stats = useStats()
  const paperStats = usePaperStats()
  const pages = usePages()
  const recent = (pages.data?.pages ?? []).slice(0, 5)

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
              onClick={() => navigate({ to: '/$lang/wiki', params: { lang } })}
            >
              <BookOpen className="size-4" /> {t('ctaWiki')}
            </Button>
          </div>
        </div>
      </div>

      <MetricGrid stats={stats.data} loading={stats.isLoading} />
      {paperStats.data?.available && (
        <MetricGrid
          paperStats={paperStats.data}
          loading={paperStats.isLoading}
        />
      )}

      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title={t('quickActions')} icon={Activity}>
          <div className="space-y-3">
            <Button
              variant="outline"
              className="w-full justify-start"
              onClick={() =>
                navigate({ to: '/$lang/wiki/search', params: { lang } })
              }
            >
              <Search className="size-4" /> {t('searchKnowledge')}
            </Button>
          </div>
        </Panel>

        <Panel title={t('recentPages')} icon={FileText}>
          <StatusBlock
            loading={pages.isLoading}
            error={pages.error?.message ?? ''}
            empty={!recent.length}
          >
            <div className="flex flex-col">
              {recent.map((page) => (
                <PageListItem key={page.id} page={page} />
              ))}
            </div>
          </StatusBlock>
        </Panel>
      </div>
    </section>
  )
}
