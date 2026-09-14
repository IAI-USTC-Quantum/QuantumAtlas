import { mkdirSync, writeFileSync } from 'node:fs'
import type { FullResult, Reporter, TestCase, TestResult } from '@playwright/test/reporter'
import type { ProbeResult } from './probe'

type Measurement = ProbeResult & {
  label: string; caseId: string; cpuRate: number; cacheState: string; repeat: number
  dimensions: Record<string, unknown>; testStatus: string
  [key: string]: unknown
}
const metricNames = ['openToDOMMs', 'openToPaintOpportunityMs', 'maxLongTaskMs', 'totalLongTaskMs',
  'maxHeartbeatGapMs', 'maxFrameGapMs', 'domElements', 'formulaCount'] as const
function stats(values: number[]) {
  if (!values.length) return null
  const sorted = [...values].sort((a, b) => a - b)
  const middle = Math.floor(sorted.length / 2)
  return { median: sorted.length % 2 ? sorted[middle] : (sorted[middle - 1] + sorted[middle]) / 2,
    max: Math.max(...values), min: Math.min(...values) }
}

export default class PerformanceReporter implements Reporter {
  private samples: Measurement[] = []
  private tests: { title: string; status: string; errors: string[] }[] = []
  constructor(private options: { outputDir: string }) {}

  onTestEnd(test: TestCase, result: TestResult) {
    this.tests.push({ title: test.title, status: result.status, errors: result.errors.map((error) => error.message ?? String(error)) })
    for (const attachment of result.attachments) {
      if (attachment.name.startsWith('measurement-') && attachment.body) {
        this.samples.push({ ...JSON.parse(attachment.body.toString('utf8')), testStatus: result.status })
      }
    }
  }

  onEnd(result: FullResult) {
    // --list invokes reporter hooks too; discovery must not overwrite evidence.
    if (!this.tests.length) return
    mkdirSync(this.options.outputDir, { recursive: true })
    const groups = new Map<string, Measurement[]>()
    for (const sample of this.samples) {
      // Never pool failure/rejection timings with successfully rendered documents.
      const key = [sample.caseId, sample.cpuRate, sample.cacheState, sample.outcome].join('/')
      groups.set(key, [...(groups.get(key) ?? []), sample])
    }
    const summary = Array.from(groups, ([key, samples]) => ({
      key, caseId: samples[0].caseId, cpuRate: samples[0].cpuRate,
      cacheState: samples[0].cacheState, outcome: samples[0].outcome,
      n: samples.length, failedTests: samples.filter((sample) => sample.testStatus !== 'passed').length,
      dimensions: samples[0].dimensions,
      metrics: Object.fromEntries(metricNames.map((name) => [name,
        stats(samples.map((sample) => sample[name]).filter((value): value is number => typeof value === 'number'))])),
    }))
    const generatedAt = new Date().toISOString()
    writeFileSync(`${this.options.outputDir}/samples.json`, JSON.stringify({ generatedAt, status: result.status, samples: this.samples, tests: this.tests }, null, 2) + '\n')
    writeFileSync(`${this.options.outputDir}/summary.json`, JSON.stringify({ generatedAt, status: result.status, summary }, null, 2) + '\n')
    const lines = [
      '# Synthetic Markdown responsiveness measurements', '',
      `Suite status: **${result.status}**. ${this.samples.length} measured opens.`, '',
      'All times are milliseconds, median / maximum over the indicated n. Rejections are NOT successful renders. These are lab observations, not universal latency pass/fail thresholds.', '',
      '| Case | CPU | Cache state | Outcome | n | Click→DOM | Click→paint opportunity | Longest task | Heartbeat gap | Frame gap | DOM elements | Formulas |',
      '| --- | ---: | --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |',
    ]
    for (const row of summary) {
      const cell = (name: typeof metricNames[number]) => {
        const value = row.metrics[name]
        return value ? `${value.median.toFixed(1)} / ${value.max.toFixed(1)}` : 'n/a'
      }
      lines.push(`| ${row.caseId} | ${row.cpuRate}x | ${row.cacheState} | ${row.outcome} | ${row.n} | ${cell('openToDOMMs')} | ${cell('openToPaintOpportunityMs')} | ${cell('maxLongTaskMs')} | ${cell('maxHeartbeatGapMs')} | ${cell('maxFrameGapMs')} | ${cell('domElements')} | ${cell('formulaCount')} |`)
    }
    lines.push('', 'Metadata, corpus dimensions/SHA-256, dist fingerprint, browser version and individual observations: `samples.json`. Full fixture request logs and semantic assertion status: `playwright.json`.', '')
    writeFileSync(`${this.options.outputDir}/summary.md`, lines.join('\n'))
    console.log(`\nPerformance data: ${this.options.outputDir}/{samples.json,summary.json,summary.md,playwright.json}`)
  }
}
