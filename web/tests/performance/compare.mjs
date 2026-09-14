// node tests/performance/compare.mjs worker-baseline react-markdown
// Reads only benchmark-generated local data. Never compares rejection latency as
// a successful-render speedup, and refuses failed/incompatible measurement runs.
import { readFileSync, writeFileSync } from 'node:fs'
import { fileURLToPath } from 'node:url'

const labels = process.argv.slice(2)
if (labels.length !== 2 || labels.some((label) => !/^[a-z0-9][a-z0-9_-]*$/.test(label))) {
  throw new Error('Usage: node tests/performance/compare.mjs <baseline-label> <candidate-label>')
}
const root = fileURLToPath(new URL('../../../build/react-markdown-results/', import.meta.url))
const runs = labels.map((label) => JSON.parse(readFileSync(`${root}/${label}/samples.json`, 'utf8')))
for (let index = 0; index < runs.length; index++) {
  const run = runs[index]
  if (run.status !== 'passed' || !run.samples.length || run.tests.some((test) => test.status !== 'passed')) {
    throw new Error(`${labels[index]} is incomplete or has failed semantic checks; inspect it before comparison.`)
  }
}
function groups(run) {
  const grouped = new Map()
  for (const sample of run.samples) {
    const key = [sample.caseId, sample.cpuRate, sample.cacheState].join('/')
    if (!grouped.has(key)) grouped.set(key, [])
    grouped.get(key).push(sample)
  }
  return grouped
}
const [before, after] = runs.map(groups)
if (before.size !== after.size || [...before.keys()].some((key) => !after.has(key))) {
  throw new Error('Before/after matrix differs; rerun an identical case/CPU/cache-state matrix.')
}
const stats = (samples, metric) => {
  const values = samples.map((sample) => sample[metric]).filter((value) => typeof value === 'number').sort((a, b) => a - b)
  if (!values.length) return null
  const mid = Math.floor(values.length / 2)
  return { median: values.length % 2 ? values[mid] : (values[mid - 1] + values[mid]) / 2, max: Math.max(...values) }
}
const metrics = ['openToDOMMs', 'openToPaintOpportunityMs', 'maxLongTaskMs', 'maxHeartbeatGapMs', 'maxFrameGapMs', 'domElements', 'formulaCount']
const rows = []
for (const [key, oldSamples] of before) {
  const newSamples = after.get(key)
  if (oldSamples.length !== 3 || newSamples.length !== 3) throw new Error(`${key}: need three measurements in each run`)
  for (const sample of [...oldSamples, ...newSamples]) {
    for (const field of ['corpusVersion', 'browserVersion', 'playwrightVersion', 'node', 'platform', 'arch', 'cpuModel', 'logicalCPUs']) {
      if (sample[field] !== oldSamples[0][field]) throw new Error(`${key}: incompatible ${field}`)
    }
    if (JSON.stringify(sample.dimensions) !== JSON.stringify(oldSamples[0].dimensions)) throw new Error(`${key}: input corpus differs`)
    if (JSON.stringify(sample.viewport) !== JSON.stringify(oldSamples[0].viewport)) throw new Error(`${key}: viewport differs`)
  }
  const outcomes = (samples) => [...new Set(samples.map((sample) => sample.outcome))].join(', ')
  const oldOutcome = outcomes(oldSamples)
  const newOutcome = outcomes(newSamples)
  rows.push({
    key, beforeOutcome: oldOutcome, afterOutcome: newOutcome,
    comparableSuccessfulRenders: oldOutcome === 'success' && newOutcome === 'success',
    before: Object.fromEntries(metrics.map((metric) => [metric, stats(oldSamples, metric)])),
    after: Object.fromEntries(metrics.map((metric) => [metric, stats(newSamples, metric)])),
  })
}
const cell = (value) => value ? `${value.median.toFixed(1)} / ${value.max.toFixed(1)}` : 'n/a'
const lines = [
  `# Synthetic comparison: ${labels[0]} → ${labels[1]}`, '',
  'Milliseconds, median / maximum, n=3 per row per renderer. These are lab observations, not universal latency guarantees. Rejection timings are not successful-render performance.', '',
  '| Case / CPU / cache | Baseline → candidate outcome | Click→paint before | Click→paint after | Long task before | Long task after | Heartbeat before | Heartbeat after |',
  '| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |',
]
for (const row of rows) {
  lines.push(`| ${row.key} | ${row.beforeOutcome} → ${row.afterOutcome} | ${cell(row.before.openToPaintOpportunityMs)} | ${cell(row.after.openToPaintOpportunityMs)} | ${cell(row.before.maxLongTaskMs)} | ${cell(row.after.maxLongTaskMs)} | ${cell(row.before.maxHeartbeatGapMs)} | ${cell(row.after.maxHeartbeatGapMs)} |`)
}
lines.push('', `Baseline dist SHA-256: ${runs[0].samples[0].distSha256}`, `Candidate dist SHA-256: ${runs[1].samples[0].distSha256}`, '')
const prefix = `${root}/comparison-${labels[0]}-vs-${labels[1]}`
writeFileSync(`${prefix}.json`, JSON.stringify({ labels, rows }, null, 2) + '\n')
writeFileSync(`${prefix}.md`, lines.join('\n'))
console.log(`${prefix}.json\n${prefix}.md`)
