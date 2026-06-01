import { Trans, useTranslation } from 'react-i18next'

// Global SPA footer. Surfaces upstream-data-source attribution
// required by docs/about/license-and-attribution.md.
//
// OpenAlex + Crossref are CC0 — attribution is not legally required,
// but both are non-profit infrastructure (OurResearch / Crossref) that
// rely on visible attribution for funding. arXiv is the source of every
// paper entry we surface.
//
// API responses carry the same intent in machine-readable form via the
// X-Attribution header set by the router middleware in cmd/qatlasd/main.go.
export function Footer() {
  const { t } = useTranslation('common')

  return (
    <footer className="border-t border-border bg-background/80 px-4 py-5 text-xs text-muted-foreground sm:px-6 lg:px-8">
      <div className="mx-auto flex max-w-6xl flex-col gap-2 sm:flex-row sm:items-center sm:justify-between">
        <p className="leading-relaxed">
          <Trans
            t={t}
            i18nKey="footer.dataSources"
            components={{
              oa: (
                <a
                  href="https://openalex.org"
                  target="_blank"
                  rel="noreferrer"
                  className="underline underline-offset-2 hover:text-foreground"
                />
              ),
              cr: (
                <a
                  href="https://www.crossref.org"
                  target="_blank"
                  rel="noreferrer"
                  className="underline underline-offset-2 hover:text-foreground"
                />
              ),
              ax: (
                <a
                  href="https://arxiv.org"
                  target="_blank"
                  rel="noreferrer"
                  className="underline underline-offset-2 hover:text-foreground"
                />
              ),
            }}
          />
        </p>
        <p className="flex flex-wrap gap-x-4 gap-y-1">
          <a
            href="https://quantum-atlas.readthedocs.io/zh-cn/latest/about/license-and-attribution/"
            target="_blank"
            rel="noreferrer"
            className="underline underline-offset-2 hover:text-foreground"
          >
            {t('footer.links.license')}
          </a>
          <a
            href="https://quantum-atlas.readthedocs.io/zh-cn/latest/about/terms-of-service/"
            target="_blank"
            rel="noreferrer"
            className="underline underline-offset-2 hover:text-foreground"
          >
            {t('footer.links.tos')}
          </a>
          <a
            href="https://github.com/IAI-USTC-Quantum/QuantumAtlas"
            target="_blank"
            rel="noreferrer"
            className="underline underline-offset-2 hover:text-foreground"
          >
            {t('footer.links.source')}
          </a>
        </p>
      </div>
    </footer>
  )
}
