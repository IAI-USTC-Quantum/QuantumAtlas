# Opt-in paper-preview responsiveness benchmark

This is an empirical **synthetic Chromium lab benchmark**, not a production/backend
integration test or a universal latency gate. It opens the actual paper route and
clicks the actual **Preview Markdown** button against a separately built `web/dist`.
It reuses `tests/browser/fixtures.ts` without modifying it. Every API response and
identity is synthetic; external/unknown network requests, WebSockets, CSP violations,
browser exceptions, dialogs, and missing JS/CSS/fonts fail the fixture. The static
preview has no backend proxy and never reads the application's Vite config or `.env`.

## Reproduce

Use the installed, lockfile-pinned toolchain. Do not run competing builds/test suites
while timing. Check that loopback port **4177** is free; the config refuses to reuse
an existing server. Build the intended renderer separately **before** starting a run.
For before/after work, preserve the old bundle until its baseline run has finished.

From `web/`:

```bash
ss -ltn '( sport = :4177 )'
mkdir -p ../build/react-markdown-results/worker-baseline
set -o pipefail
MARKDOWN_BENCH_LABEL=worker-baseline npx --no-install playwright test \
  -c playwright.performance.config.ts 2>&1 | \
  tee ../build/react-markdown-results/worker-baseline/run.log

# Only after baseline completion, build the changed implementation separately.
mkdir -p ../build/react-markdown-results/react-markdown
MARKDOWN_BENCH_LABEL=react-markdown npx --no-install playwright test \
  -c playwright.performance.config.ts 2>&1 | \
  tee ../build/react-markdown-results/react-markdown/run.log
```

The performance config is explicitly opt-in. The normal browser config's
`testDir: './tests/browser'` excludes these tests. You can also use
`MARKDOWN_BENCH_LABEL=react-markdown npm run test:performance`. The harness itself
never modifies production source, standard API fixtures, or the lockfile.
`MARKDOWN_BENCH_LABEL` is mandatory, path-safe, and determines the results directory.
Choose a **new label** to retain an earlier run: repeated labels overwrite results.
Set `MARKDOWN_BENCH_NATIVE_ONLY=1` for only the six-case native matrix, or use standard
Playwright `--grep` for exploration with a separate label. Full comparisons must use
the same matrix and unchanged corpus. Do not install or switch browser channels
between runs. The harness asserts Playwright **1.62.0** and Chromium
**151.0.7922.34** (bundled revision **1234**). A future intentional toolchain update
must update the explicit pins and acquire a new baseline.

## Corpus and matrix

`corpus.ts` defines `paper-responsiveness-v1` with exact JavaScript **UTF-16 code-unit**
lengths, not approximate byte lengths. It exports no production parser/limit imports:
changing renderer implementation cannot silently change the input. Each observation
includes the exact UTF-16 length, UTF-8 bytes, SHA-256, repeated block count, expected
formulas, tables, code blocks, and headings. A terminal sentinel detects truncation.

- Mixed prose/GFM/fenced code/moderate math: 10k, 50k, 100k, 200k code units.
- Text-only 200k control (paragraphs plus a heading; zero formulas).
- Denser math 50k stress (fractions, square roots, summations).
- Native CPU: all six cases, **three measured repetitions** each.
- CDP 4x CPU slowdown: mixed50k, mixed200k, dense50k, also three repetitions.
- Every repetition measures both `first-open` and `reopen`: **27 tests / 54 opens**.

The formula corpus includes dollar inline/display and parenthesized delimiters;
fenced-code dollar expressions remain literal. It is a controlled paper-like mix,
not a representative sample of all real papers, pathological TeX, deeply nested
Markdown, huge tables, images, or arbitrary Unicode distributions. Real-world
validation remains necessary. CPU slowdown is CDP emulation, not equivalent to a
particular mobile device or all execution resources (including workers) being slowed
the same way.

## Measurement protocol

One serial worker and shared Chromium process; every CPU/case/repetition gets a
**fresh browser context and page**, no persisted storage, and a 1280×900 viewport.
There are no discarded warm-up trials. The page route is loaded and fonts plus two
animation frames settle before timing; initial application/auth bootstrap is
**outside** the preview-open window.

1. `first-open`: fresh page/context, so renderer module and local math fonts have not
   yet been used; the Markdown fixture is fetched after the click. The browser
   process and OS filesystem cache may already be warm. This is not a cold OS boot.
2. `reopen`: after semantic/source checks, close the dialog, settle the page, and
   reopen the same document. Modules and fonts are warm in that page, but the actual
   route discards Markdown query data on close (`gcTime: 0`). The preview remounts
   and the harness asserts one fresh Markdown fetch on **both** opens.
3. The test fixture's request routing and static server `no-store` disable normal
   HTTP caching. These are module/font-cache comparisons, not query/network-cache
   performance measurements. There is no injected network latency.
4. A capture-phase listener records the actual button click **in the browser**.
   A MutationObserver marks the first committed rendered root or explicit rejection.
   No Playwright polling/IPC is included in these timestamps.
5. Force visible preview layout, await `document.fonts.ready`, then two
   `requestAnimationFrame` callbacks. This is a **paint opportunity** after DOM
   readiness and required fonts, not a compositor screenshot guarantee or complete
   rasterization of offscreen content.
6. During that window record main-thread `longtask` entries (tasks ≥50ms), a 10ms
   heartbeat, and frame callback gaps. The observer is drained after the fence;
   that collection delay and all DOM counting/source checks are excluded from timing.
   Tasks overlapping the fence are reported at their full duration, not clipped.
7. Record actual descendant DOM element count (excluding the rendered root), KaTeX
   root count, table/code/heading counts, text length, and terminal sentinel. Formula
   and structure counts, exact Source contents, and network safety are hard semantic
   assertions. Timing values have **no hard latency pass threshold**.

`success`, `rejected`, and operational `timeout` are distinct outcomes. A controlled
rejection must have a visible message, no rendered DOM, and preserve exact Source.
It is **not** counted as successful rendering and its fast fallback timings are
never pooled with successful cases. Watchdog/outer test timeouts detect incomplete
measurements, not a universal responsiveness SLA; JS timers cannot preempt blocked
main-thread code. Any failed semantic test is reflected in the report and must not
be described as a passing benchmark even if it emitted some measurements.

## Evidence and interpretation

Everything is written below the git-ignored
`build/react-markdown-results/<label>/`:

- `samples.json`: individual observations, input dimensions/hashes, dist asset
  fingerprint, environment/browser metadata, test statuses and errors.
- `summary.json`, `summary.md`: grouped median/max/min; outcomes are separated.
- `playwright.json`: full Playwright report including synthetic request logs and
  individual measurement attachments.
- `run.log`: console output when using the `tee` invocation above.
- `artifacts/`: Playwright failure details if any; trace/video/screenshots disabled
  because they add measurement overhead.

After both runs pass their semantic assertions, generate a validated side-by-side
comparison from `web/`:

```bash
node tests/performance/compare.mjs worker-baseline react-markdown
```

It refuses failed/incomplete runs, changed corpus, mismatched three-repeat matrices,
browser/toolchain/environment differences, or changed viewport. The comparison
JSON/Markdown is written in `build/react-markdown-results/`; it marks rows where
outcomes differ as unsuitable for successful-render speedup claims.

With only n=3, medians/maxima describe the observed samples; they do not estimate
p95/p99 or establish statistical significance. Compare like-for-like rows and check
input SHA-256, dist fingerprints, browser version, CPU rate, cache state, semantic
status, and outcomes before interpreting speedups. Main-thread gaps, not just total
render time, determine whether asynchronous progress indicators actually stay
responsive. A migration can improve completion time while producing longer stalls;
a fast safety rejection is not faster successful document rendering. Record both.
