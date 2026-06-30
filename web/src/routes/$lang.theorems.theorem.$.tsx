import { createFileRoute, Link } from '@tanstack/react-router'
import { useState } from 'react'
import { useTranslation } from 'react-i18next'
import {
  ArrowLeft,
  CheckCircle2,
  FileCode2,
  GitFork,
  ScrollText,
  ShieldCheck,
  XCircle,
} from 'lucide-react'

import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { PageHeader } from '@/components/page-header'
import { Panel } from '@/components/panel'
import { StatusBlock } from '@/components/status-block'
import { useLang } from '@/hooks/use-lang'
import { useTheoremDetail, useTheoremSource } from '@/lib/queries'

export const Route = createFileRoute('/$lang/theorems/theorem/$')({
  component: TheoremDetailPage,
})

function TheoremDetailPage() {
  const { t } = useTranslation('theorems')
  const lang = useLang()
  const { _splat } = Route.useParams()
  const fqn = decodeURIComponent(_splat ?? '')
  const detail = useTheoremDetail(fqn || null)
  const [showSource, setShowSource] = useState(false)
  const source = useTheoremSource(fqn || null, showSource)

  const thm = detail.data?.theorem
  const certified = detail.data?.certified

  return (
    <section className="space-y-5">
      <Link
        to="/$lang/theorems"
        params={{ lang }}
        className="inline-flex items-center gap-1.5 text-sm text-muted-foreground hover:text-foreground"
      >
        <ArrowLeft className="size-4" />
        {t('detail.back')}
      </Link>

      <StatusBlock
        loading={detail.isLoading}
        error={detail.error?.message ?? ''}
        empty={!thm}
      >
        {thm && (
          <>
            <PageHeader
              eyebrow={thm.kind ?? t('eyebrow')}
              title={thm.lean_fqn}
              copy={thm.statement_paraphrase}
            />

            <div className="flex flex-wrap gap-2">
              {thm.family_id && <Badge variant="outline">{thm.family_id}</Badge>}
              {thm.audit_status && (
                <Badge variant="secondary">{thm.audit_status}</Badge>
              )}
              <Badge variant={thm.sorry_free ? 'default' : 'outline'}>
                {thm.sorry_free ? (
                  <CheckCircle2 className="size-3.5" />
                ) : (
                  <XCircle className="size-3.5" />
                )}
                {t('detail.sorryFree')}
              </Badge>
              {thm.unit_id && (
                <Badge variant="outline" className="font-mono text-xs">
                  {thm.unit_id}
                </Badge>
              )}
            </div>

            <Panel
              title={t('detail.dependencies')}
              icon={GitFork}
              suffix={`${thm.depends_on?.length ?? 0}`}
            >
              {thm.depends_on && thm.depends_on.length > 0 ? (
                <div className="flex flex-wrap gap-2">
                  {thm.depends_on.map((dep) => (
                    <Link
                      key={dep}
                      to="/$lang/theorems/theorem/$"
                      params={{ lang, _splat: dep }}
                    >
                      <Badge
                        variant="outline"
                        className="cursor-pointer font-mono text-xs hover:border-primary/50 hover:bg-accent/40"
                      >
                        {dep}
                      </Badge>
                    </Link>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">{t('detail.noDeps')}</p>
              )}
            </Panel>

            <Panel
              title={t('detail.axioms')}
              icon={ScrollText}
              suffix={`${thm.axioms_used?.length ?? 0}`}
            >
              {thm.axioms_used && thm.axioms_used.length > 0 ? (
                <div className="flex flex-wrap gap-2">
                  {thm.axioms_used.map((ax) => (
                    <Badge key={ax} variant="outline" className="font-mono text-xs">
                      {ax}
                    </Badge>
                  ))}
                </div>
              ) : (
                <p className="text-sm text-muted-foreground">{t('detail.noAxioms')}</p>
              )}
            </Panel>

            {certified && (
              <Panel title={t('detail.audit')} icon={ShieldCheck}>
                <dl className="grid gap-x-6 gap-y-2 text-sm sm:grid-cols-2">
                  <Field label={t('detail.result')} value={certified.result} />
                  <Field label={t('detail.kernel')} value={certified.kernel} />
                  <Field
                    label={t('detail.faithfulness')}
                    value={certified.claim_faithfulness}
                  />
                  <Field
                    label={t('detail.auditedTs')}
                    value={certified.audited_ts}
                  />
                  {certified.magi && (
                    <Field
                      label={t('detail.magi')}
                      value={`✓ ${certified.magi.faithful ?? 0} · ✗ ${
                        certified.magi.unfaithful ?? 0
                      } · ~ ${certified.magi.abstain ?? 0}`}
                    />
                  )}
                </dl>
              </Panel>
            )}

            <Panel title={t('detail.source')} icon={FileCode2} suffix={thm.file}>
              {!showSource ? (
                <Button variant="outline" size="sm" onClick={() => setShowSource(true)}>
                  <FileCode2 className="size-4" />
                  {t('detail.showSource')}
                </Button>
              ) : (
                <StatusBlock
                  loading={source.isLoading}
                  error={source.error?.message ?? ''}
                  empty={!source.data?.source}
                >
                  <pre className="max-h-[32rem] overflow-auto rounded-lg border border-border bg-muted/40 p-4 text-xs leading-relaxed">
                    <code>{source.data?.source}</code>
                  </pre>
                </StatusBlock>
              )}
            </Panel>
          </>
        )}
      </StatusBlock>
    </section>
  )
}

function Field({ label, value }: { label: string; value?: string }) {
  if (!value) return null
  return (
    <div className="flex flex-col">
      <dt className="text-xs uppercase tracking-wide text-muted-foreground">{label}</dt>
      <dd className="font-medium">{value}</dd>
    </div>
  )
}
