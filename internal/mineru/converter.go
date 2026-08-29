package mineru

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// Converter is the server-side MinerU driver gated by the opt-in
// QATLAS_PAPER_ACCESS_ENABLED master switch. It is constructed once
// at boot from cmd/qatlasd/main.go and passed into routes.RegisterPapers.
//
// Lifecycle for one paper:
//
//  1. Handler calls Ensure(ctx, canonical). If the markdown is already
//     in the object store the call returns immediately with JobStateDone
//     (cache hit) and counts a cache_hit_total — no MinerU traffic.
//  2. Otherwise Ensure dedupes against any in-flight or recently-failed
//     job for the same canonical id. New requests get queued; concurrent
//     requests piggyback on the same Job. A 60s cooldown on retryable
//     / fatal failures and a "until tomorrow 00:01 local" cooldown on
//     daily-limit failures prevents thundering herds against MinerU.
//  3. A background goroutine acquires a slot in the concurrency
//     semaphore (sized by MINERU_MAX_CONCURRENT_JOBS, default 4),
//     reads the PDF bytes from the object store, pushes them to
//     MinerU's own storage via the file-urls upload channel (no
//     publicly reachable S3 needed), polls the resulting batch until
//     done, downloads the result zip, writes images + markdown via
//     PutWithOptions with IfNoneMatch:"*" (matches upload-mineru's
//     first-writer-wins idempotency), and best-effort calls
//     catalog.UpsertMD.
//  4. The Lookup method is a side-effect-free snapshot used by the
//     markdown/status endpoint.
//
// In-flight jobs are NOT persisted across restart — accepted as a v1
// trade-off per issue #8 ("operator restarts during a conversion are
// rare; resumed jobs are easier to reason about as fresh submissions").
// The IfNoneMatch:"*" write also makes multi-edge double-submission
// safe (first writer wins; loser silently observes 412 and treats it
// as a successful write).
//
// Dedupe / queue / fetch progress are all PER-PROCESS state — two
// edges running their own qatlasd will each maintain their own
// c.jobs map, their own avgConvertDuration window, and their own
// arxiv fetch semaphore. So two simultaneous
// /markdown calls for the same paper that happen to land on
// different edges will trigger TWO MinerU jobs (one each), wasting
// quota; status responses on each edge see only that edge's queue.
// IfNoneMatch:"*" on the markdown write still guarantees byte
// integrity, but the cost is real. Cross-edge dedupe is tracked in
// issue #13 (would require a shared lease lock in RustFS or Neo4j).
// Acceptable for current scale (~2 edges, low traffic); revisit when
// QPS justifies the complexity.
//
// Safe for concurrent use.
type Converter struct {
	cfg     ConverterConfig
	store   objstore.Store
	catalog *registry.Store
	keyRing *KeyRing
	logger  *slog.Logger

	// now/dailyResetAt are indirection hooks so tests can fast-forward
	// "tomorrow 00:01" without sleeping.
	now          func() time.Time
	dailyResetAt func(time.Time) time.Time

	enabled     bool   // false when switch off OR no tokens OR public endpoint missing
	disabledMsg string // non-empty when !enabled, for handler error responses

	sem      chan struct{} // MinerU job slots
	arxivSem chan struct{} // arxiv fetch slots (nil when no fetcher)

	mu   sync.Mutex
	jobs map[string]*Job

	// durationsMu protects the recent-duration ring used for ETA
	// estimation. Kept independent from `mu` so a status poll that
	// only needs a fresh ETA snapshot doesn't contend with Ensure's
	// hot path.
	durationsMu     sync.Mutex
	recentDurations [convertHistoryCap]time.Duration
	recentCount     int // total finishes ever observed (saturates at MaxInt)
	recentIdx       int // next slot to overwrite (round-robin)

	counters Counters
}

// convertHistoryCap is the size of the rolling window used to
// estimate per-job MinerU duration for the queue-ETA field. Twenty
// most-recent jobs is enough to absorb the long-tail variance of
// arXiv paper sizes (typical 1–10 minute range) while still tracking
// systemic shifts (MinerU API slowdown, model upgrades).
const convertHistoryCap = 20

// ConverterConfig is the subset of internal/config.Config the converter
// needs. Passing a flat value avoids an import cycle from config →
// mineru (existing) → … and makes the converter trivially testable.
type ConverterConfig struct {
	PaperAccessEnabled bool
	// MinerUAPITokens is the pool of API tokens to rotate through.
	// At least one non-empty entry enables server-side conversion;
	// when more than one is configured the converter automatically
	// fails over to the next key when the current one reports
	// daily-limit exhaustion, so 3× tokens ≈ 3× daily quota with no
	// operator intervention.
	MinerUAPITokens         []string
	MinerUAPIBaseURL        string
	MinerUModelVersion      string
	MinerULanguage          string
	MinerUIsOCR             bool
	MinerUEnableFormula     bool
	MinerUEnableTable       bool
	MinerUPollInterval      time.Duration
	MinerUTimeout           time.Duration
	MinerUMaxConcurrentJobs int

	// Fetcher, when non-nil, allows the converter to silent-fetch
	// missing PDFs from arxiv.org before driving MinerU. When nil the
	// pre-A2 behavior is preserved: Ensure on a paper with no stored
	// PDF returns ErrFatal "no PDF in store for <id>" so the caller
	// must upload the PDF before requesting markdown. Set the
	// fetcher to opt the deployment into silent fetch + convert (the
	// QATLAS_PAPER_ACCESS_ENABLED master switch enables both ends).
	Fetcher *arxiv.Fetcher

	// ArxivFetchConcurrent bounds the number of in-flight arxiv.org
	// fetches independently from MinerU job concurrency. I/O bound
	// fetches don't block GPU-bound MinerU work, but we still cap
	// them so a thundering herd from 100 simultaneous /pdf calls
	// can't open 100 sockets to arxiv. Default 2; ignored when
	// Fetcher is nil.
	ArxivFetchConcurrent int

	// IndexPusher, when non-nil, receives a qatlas-rag index-build
	// push after each successful conversion write-through (the paper
	// now has markdown, i.e. is ready for indexing). The push is
	// best-effort: failures are logged, never propagated.
	// *rag.RemoteClient satisfies the interface implicitly.
	IndexPusher IndexPusher
}

// IndexPusher is the slice of the qatlas-rag client (internal/rag)
// the converter needs, factored out so tests can fake the push.
type IndexPusher interface {
	PushIndex(ctx context.Context, paperID string) error
}

// JobState is the lifecycle state of one paper's conversion job.
type JobState string

const (
	JobStateQueued  JobState = "queued"
	JobStateRunning JobState = "running"
	JobStateDone    JobState = "done"
	JobStateFailed  JobState = "failed"
)

// Phase is a finer-grained progress label that survives across the
// fetch-PDF → convert-MD pipeline. Compared to JobState, Phase tells
// agents whether the PDF has landed yet (`pdf_ready` derived from
// Phase >= PhaseConvertingMD) and what specifically is happening
// inside the long-running State=Running. The markdown handler stays
// state-driven (queued / running → 202); only the status handler
// reads Phase.
type Phase string

const (
	PhaseNone            Phase = ""
	PhaseFetchingPDF     Phase = "fetching_pdf"
	PhaseConvertingMD    Phase = "converting_md"
	PhaseReady           Phase = "ready"
	PhaseErrorFetching   Phase = "error_fetching"
	PhaseErrorConverting Phase = "error_converting"
)

// FetchProgress is the per-job fetch sub-state surfaced to the status
// endpoint while State=Running AND Phase=PhaseFetchingPDF. Populated
// when the converter has to silently pull the PDF from arxiv.org
// before driving MinerU.
type FetchProgress struct {
	StartedAt     time.Time
	CompletedAt   time.Time
	BytesReceived int64
	BytesTotal    int64 // 0 if upstream didn't send Content-Length
	Sha256        string
	Attempts      int
}

// ConvertProgress is the per-job convert sub-state surfaced to the
// status endpoint while State=Running AND Phase=PhaseConvertingMD.
type ConvertProgress struct {
	StartedAt    time.Time
	CompletedAt  time.Time
	MinerUTaskID string // MinerU's batch id from the upload channel
	Stage        string // "submitting" / "running" / "downloading_zip"
	PolledCount  int
}

// QueueSnapshot is the per-process aggregate of "what does the wait
// look like right now" — used to populate the `queue` sub-object on
// status responses so an agent caller can decide whether to keep
// polling, back off, or give up.
//
// Position is 1-indexed counting only the QUEUED jobs ahead of this
// job plus the job itself; the RUNNING jobs (up to MaxConcurrent of
// them) are counted via RunningCount and are NOT included in
// Position. So a fresh queued job behind 4 running + 2 queued sees
// Position=3, RunningCount=4, AheadOfMe=2, EtaSeconds = (running
// remaining + ahead) * avg / MaxConcurrent.
type QueueSnapshot struct {
	Position      int           // 1-indexed slot in the queue (>=1 only while State==Queued)
	AheadOfMe     int           // jobs queued before me (Position - 1 when queued; undefined otherwise)
	RunningCount  int           // jobs currently consuming a MinerU slot
	MaxConcurrent int           // MinerUMaxConcurrentJobs
	EtaSeconds    int64         // estimated seconds until this job starts running; 0 = no estimate
	EtaBasis      string        // "observed_avg_of_N_jobs" / "default_no_history"
	AvgDuration   time.Duration // average over the recent window, zero when no history
}

// Job is one in-flight or recently-completed conversion. Returned by
// Ensure and Lookup. Snapshot value — the converter never mutates a
// Job after handing it to a caller (a fresh Job is created on retry
// after the cooldown elapses).
type Job struct {
	Canonical     string
	State         JobState
	Phase         Phase
	SubmittedAt   time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
	Err           error
	ErrKind       error // ErrFatal / ErrRetryable / ErrDailyLimit, nil otherwise
	CooldownUntil time.Time

	// Fetch is populated when the converter had to fetch the PDF
	// from arxiv as part of fulfilling this job. nil when the PDF
	// was already in store at Ensure time, or when no fetcher is
	// configured.
	Fetch *FetchProgress

	// Convert is populated as soon as the MinerU pipeline starts
	// (Phase=PhaseConvertingMD). nil for /pdf endpoint jobs that
	// skip MinerU.
	Convert *ConvertProgress

	// Queue is a per-snapshot view of how far in the line this job
	// is. Populated by Lookup() and Ensure() return paths; the field
	// is stale-on-arrival the moment the caller observes it (other
	// jobs may finish or be queued in between), but that's fine — it
	// guides agents toward sensible poll intervals, not a strict SLA.
	Queue *QueueSnapshot

	// oaPdfURL is the open-access PDF URL a DOI job fetches from when
	// the DOI-keyed PDF isn't in the store yet. Empty for arxiv jobs
	// and for DOI jobs that convert a pre-existing (contributed) PDF.
	// Unexported: it's job-driver state, not status payload.
	oaPdfURL string
}

// Counters is the per-process tally surfaced for the optional /metrics
// or /api/health extras. Read via Snapshot.
type Counters struct {
	Submitted            atomic.Int64
	Succeeded            atomic.Int64
	FailedFatal          atomic.Int64
	FailedRetryable      atomic.Int64
	FailedDailyLimit     atomic.Int64
	CacheHits            atomic.Int64
	CacheMisses          atomic.Int64
	InflightJobs         atomic.Int64
	ArxivFetches         atomic.Int64
	ArxivFetchSucceeded  atomic.Int64
	ArxivFetchFailed     atomic.Int64
	InflightArxivFetches atomic.Int64
}

// CountersSnapshot is a point-in-time view of the converter counters.
type CountersSnapshot struct {
	Submitted            int64 `json:"submitted"`
	Succeeded            int64 `json:"succeeded"`
	FailedFatal          int64 `json:"failed_fatal"`
	FailedRetryable      int64 `json:"failed_retryable"`
	FailedDailyLimit     int64 `json:"failed_daily_limit"`
	CacheHits            int64 `json:"cache_hits"`
	CacheMisses          int64 `json:"cache_misses"`
	InflightJobs         int64 `json:"inflight_jobs"`
	ArxivFetches         int64 `json:"arxiv_fetches"`
	ArxivFetchSucceeded  int64 `json:"arxiv_fetch_succeeded"`
	ArxivFetchFailed     int64 `json:"arxiv_fetch_failed"`
	InflightArxivFetches int64 `json:"inflight_arxiv_fetches"`
}

// Snapshot returns a consistent read of the counters.
func (c *Converter) Snapshot() CountersSnapshot {
	return CountersSnapshot{
		Submitted:            c.counters.Submitted.Load(),
		Succeeded:            c.counters.Succeeded.Load(),
		FailedFatal:          c.counters.FailedFatal.Load(),
		FailedRetryable:      c.counters.FailedRetryable.Load(),
		FailedDailyLimit:     c.counters.FailedDailyLimit.Load(),
		CacheHits:            c.counters.CacheHits.Load(),
		CacheMisses:          c.counters.CacheMisses.Load(),
		InflightJobs:         c.counters.InflightJobs.Load(),
		ArxivFetches:         c.counters.ArxivFetches.Load(),
		ArxivFetchSucceeded:  c.counters.ArxivFetchSucceeded.Load(),
		ArxivFetchFailed:     c.counters.ArxivFetchFailed.Load(),
		InflightArxivFetches: c.counters.InflightArxivFetches.Load(),
	}
}

// FailureCooldown is the back-off applied to fatal / retryable
// failures. 60s matches the issue #8 spec — short enough that an
// operator-fixed transient (e.g. MinerU 502 storm) doesn't keep
// callers blocked for long, long enough that a thundering herd from
// multiple clients can't DoS MinerU on every individual request.
const FailureCooldown = 60 * time.Second

// NewConverter builds a Converter from cfg. Always constructible — the
// returned value is non-nil even when PaperAccessEnabled=false, so
// callers don't have to nil-check. Use Enabled() to learn whether the
// converter will actually drive MinerU.
//
// When the switch is on but the deployment can't drive MinerU
// (no API tokens configured), NewConverter leaves Enabled() false.
// Callers (the markdown handler) translate that into 503 on cache miss.
func NewConverter(cfg ConverterConfig, store objstore.Store, catalog *registry.Store, logger *slog.Logger) *Converter {
	if logger == nil {
		logger = slog.Default()
	}
	maxConc := cfg.MinerUMaxConcurrentJobs
	if maxConc < 1 {
		maxConc = 1
	}
	arxivConc := cfg.ArxivFetchConcurrent
	if arxivConc < 1 {
		arxivConc = 2
	}
	c := &Converter{
		cfg:          cfg,
		store:        store,
		catalog:      catalog,
		logger:       logger,
		now:          time.Now,
		dailyResetAt: nextLocalDailyReset,
		sem:          make(chan struct{}, maxConc),
		jobs:         map[string]*Job{},
	}
	if cfg.Fetcher != nil {
		c.arxivSem = make(chan struct{}, arxivConc)
	}
	c.keyRing = NewKeyRing(cfg.MinerUAPITokens, cfg.MinerUAPIBaseURL, c.now)

	switch {
	case !cfg.PaperAccessEnabled:
		c.enabled = false
		c.disabledMsg = "asset downloads disabled (QATLAS_PAPER_ACCESS_ENABLED=false)"
	case c.keyRing.Size() == 0:
		c.enabled = false
		c.disabledMsg = "MinerU not configured (MINERU_API_TOKENS unset); cache-only mode"
	default:
		c.enabled = true
	}

	return c
}

// KeyRingSize returns how many MinerU API tokens are loaded into the
// rotation pool — for inclusion in the startup log so operators can
// see at a glance how many keys are being managed.
func (c *Converter) KeyRingSize() int { return c.keyRing.Size() }

// Enabled reports whether the converter will actually drive MinerU on
// cache miss. False when the switch is off or no API token is
// configured. The handler falls back to "cache only; 503 on miss"
// when false.
func (c *Converter) Enabled() bool { return c.enabled }

// DisabledReason returns a human-readable explanation of why the
// converter is disabled, suitable for the body of a 503 response.
// Empty when Enabled() == true.
func (c *Converter) DisabledReason() string { return c.disabledMsg }

// FetchEnabled reports whether an arXiv PDF fetcher is wired in. The
// converter can be Enabled() (tokens configured) yet still lack a
// fetcher; in that case a cache-miss /pdf request cannot obtain
// the bytes, and the handler should answer 503 (server capability gap)
// rather than 404 (paper gone).
func (c *Converter) FetchEnabled() bool { return c.cfg.Fetcher != nil }

// MaxConcurrentJobs returns the operator-configured semaphore size,
// for inclusion in the startup log line and any /metrics surface.
func (c *Converter) MaxConcurrentJobs() int { return cap(c.sem) }

// Timeout returns the per-job total timeout, for inclusion in the
// startup log line.
func (c *Converter) Timeout() time.Duration { return c.cfg.MinerUTimeout }

// Lookup returns a snapshot of the in-flight / recently-failed job for
// canonical, if any. Returns (nil, false) when there is no current
// record. Side-effect-free — safe to call from the status endpoint.
//
// Populates the Queue sub-object so the status endpoint can render
// position/ETA without separately calling queueSnapshotFor.
func (c *Converter) Lookup(canonical string) (*Job, bool) {
	c.mu.Lock()
	j, ok := c.jobs[canonical]
	if !ok {
		c.mu.Unlock()
		return nil, false
	}
	cp := *j
	c.mu.Unlock()
	// queueSnapshotFor takes its own lock; release c.mu first to
	// avoid contention on hot status polls.
	if cp.State == JobStateQueued || cp.State == JobStateRunning {
		qs := c.queueSnapshotFor(canonical, cp)
		cp.Queue = &qs
	}
	return &cp, true
}

// recordConvertDuration appends d to the recent-duration ring used
// by queueSnapshotFor's ETA estimation. Called from the run-success
// path; intentionally a no-op for non-positive durations so clock
// skew can't poison the average.
func (c *Converter) recordConvertDuration(d time.Duration) {
	if d <= 0 {
		return
	}
	c.durationsMu.Lock()
	defer c.durationsMu.Unlock()
	c.recentDurations[c.recentIdx] = d
	c.recentIdx = (c.recentIdx + 1) % convertHistoryCap
	c.recentCount++
}

// avgConvertDuration returns the mean of the rolling window plus the
// count of samples used. Returns (0, 0) when the ring is empty.
func (c *Converter) avgConvertDuration() (time.Duration, int) {
	c.durationsMu.Lock()
	defer c.durationsMu.Unlock()
	n := c.recentCount
	if n > convertHistoryCap {
		n = convertHistoryCap
	}
	if n == 0 {
		return 0, 0
	}
	var sum time.Duration
	for i := 0; i < n; i++ {
		sum += c.recentDurations[i]
	}
	return sum / time.Duration(n), n
}

// queueSnapshotFor computes the queue position + ETA for `job` against
// the current in-flight jobs map. Caller already holds a Job copy so
// we know its SubmittedAt for ordering.
//
// Position is computed by counting QUEUED jobs ahead of this one (by
// SubmittedAt), plus this job itself. Running jobs are NOT counted in
// position — they occupy slots, not queue length.
//
// ETA formula: `eta = ceil((ahead_of_me + running_count) / max_concurrent) * avg`.
// When the average is unknown (no history yet) we fall back to half
// the per-job timeout — a conservative upper bound that won't promise
// completion faster than reality.
func (c *Converter) queueSnapshotFor(canonical string, job Job) QueueSnapshot {
	maxConc := cap(c.sem)
	if maxConc < 1 {
		maxConc = 1
	}
	avg, samples := c.avgConvertDuration()
	basis := fmt.Sprintf("observed_avg_of_%d_jobs", samples)
	if samples == 0 {
		// Use half the per-job timeout as a placeholder. Better than
		// promising 0 seconds; honest about the lack of data.
		avg = c.cfg.MinerUTimeout / 2
		basis = "default_no_history"
	}

	c.mu.Lock()
	var ahead, running int
	for k, other := range c.jobs {
		switch other.State {
		case JobStateRunning:
			running++
		case JobStateQueued:
			// Order by SubmittedAt; tie-break by canonical so two
			// jobs queued at the exact same instant get deterministic
			// ordering.
			if k == canonical {
				continue
			}
			if other.SubmittedAt.Before(job.SubmittedAt) ||
				(other.SubmittedAt.Equal(job.SubmittedAt) && k < canonical) {
				ahead++
			}
		}
	}
	c.mu.Unlock()

	qs := QueueSnapshot{
		AheadOfMe:     ahead,
		RunningCount:  running,
		MaxConcurrent: maxConc,
		AvgDuration:   avg,
		EtaBasis:      basis,
	}
	if job.State == JobStateQueued {
		qs.Position = ahead + 1
		// Add the remaining running jobs to the "still ahead" count
		// for ETA: they all need to finish (or at least free a slot)
		// before this job can start. Worst case = running slot
		// duration of avg.
		// effectiveAhead = ahead + max(0, running - 0) is just
		// (ahead + running); divide by max_concurrent and round up.
		effective := ahead + running
		batches := (effective + maxConc - 1) / maxConc
		if batches < 1 {
			batches = 1
		}
		qs.EtaSeconds = int64((time.Duration(batches) * avg).Seconds())
	}
	return qs
}

// Ensure starts (or piggybacks on) a conversion for canonical and
// returns a Job describing the current state. The caller is expected
// to translate the Job state into an HTTP response (cached → 200,
// queued/running → 202, failed → 502, …).
//
// Ensure performs a cache-hit short-circuit before touching MinerU: if
// the markdown is already in the object store it returns
// JobStateDone immediately and bumps cache_hit_total. Cache misses
// bump cache_miss_total before any dedup / submission work.
//
// Safe for concurrent use; concurrent Ensure calls for the same
// canonical are deduped to a single MinerU submission.
func (c *Converter) Ensure(ctx context.Context, canonical string) *Job {
	if _, _, exists, err := paperassets.LocateAssetByID(ctx, c.store, "markdown", canonical); err == nil && exists {
		c.counters.CacheHits.Add(1)
		return &Job{Canonical: canonical, State: JobStateDone, FinishedAt: c.now()}
	}
	c.counters.CacheMisses.Add(1)

	if !c.enabled {
		return &Job{
			Canonical: canonical,
			State:     JobStateFailed,
			Err:       errors.New(c.disabledMsg),
			ErrKind:   ErrFatal,
		}
	}

	now := c.now()

	c.mu.Lock()
	if existing, ok := c.jobs[canonical]; ok {
		switch existing.State {
		case JobStateQueued, JobStateRunning:
			cp := *existing
			c.mu.Unlock()
			return &cp
		case JobStateDone:
			cp := *existing
			c.mu.Unlock()
			return &cp
		case JobStateFailed:
			if !existing.CooldownUntil.IsZero() && now.Before(existing.CooldownUntil) {
				cp := *existing
				c.mu.Unlock()
				return &cp
			}
			// Cooldown expired — fall through, replace the failed
			// record with a fresh queued job.
		}
	}

	job := &Job{
		Canonical:   canonical,
		State:       JobStateQueued,
		SubmittedAt: now,
	}
	c.jobs[canonical] = job
	c.mu.Unlock()

	go c.run(canonical)

	// Return a snapshot of the queued state.
	cp := *job
	return &cp
}

// run is the per-job background driver for arxiv-keyed jobs.
func (c *Converter) run(canonical string) {
	c.runJob(canonical, "arxiv_id", canonical, func(ctx context.Context) error {
		return c.runOnce(ctx, canonical)
	})
}

// runJob is the shared per-job background driver. Acquires a semaphore
// slot, runs the pipeline via once, and updates c.jobs[jobKey] with the
// terminal state. Errors are captured into the Job; this method never
// panics into the calling goroutine. logKey/logVal select the log field
// naming ("arxiv_id" for arxiv jobs, "doi" for DOI jobs).
func (c *Converter) runJob(jobKey, logKey, logVal string, once func(context.Context) error) {
	// Per-job context bounded by the operator-configured timeout. We
	// don't reuse the HTTP request context because that's gone the
	// instant the caller drops the 202 response.
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.MinerUTimeout)
	defer cancel()

	c.sem <- struct{}{}
	c.counters.InflightJobs.Add(1)
	defer func() {
		<-c.sem
		c.counters.InflightJobs.Add(-1)
	}()

	c.transition(jobKey, func(j *Job) {
		j.State = JobStateRunning
		j.StartedAt = c.now()
	})

	c.counters.Submitted.Add(1)
	err := once(ctx)
	if err == nil {
		c.counters.Succeeded.Add(1)
		// Record convert duration (StartedAt → now) for ETA. We only
		// record on success so a string of fast failures doesn't
		// artificially deflate the average and mislead callers.
		convertDur := time.Duration(0)
		if peek, ok := c.Lookup(jobKey); ok && peek.Convert != nil && !peek.Convert.StartedAt.IsZero() {
			convertDur = c.now().Sub(peek.Convert.StartedAt)
		}
		if convertDur > 0 {
			c.recordConvertDuration(convertDur)
		}
		c.transition(jobKey, func(j *Job) {
			j.State = JobStateDone
			j.Phase = PhaseReady
			j.FinishedAt = c.now()
		})
		return
	}

	// Failure phase is read from the Job's current Phase, set by
	// fetchAndStorePDF / runOnce as they advanced. PhaseFetchingPDF
	// → error_fetching; anything else → error_converting (the more
	// general bucket — fatal-before-fetch errors also land here).
	failPhase := PhaseErrorConverting
	if peek, ok := c.Lookup(jobKey); ok && peek.Phase == PhaseFetchingPDF {
		failPhase = PhaseErrorFetching
	}
	kind, cooldown := c.classifyFailure(err)
	c.transition(jobKey, func(j *Job) {
		j.State = JobStateFailed
		j.Phase = failPhase
		j.FinishedAt = c.now()
		j.Err = err
		j.ErrKind = kind
		j.CooldownUntil = cooldown
	})
	c.logger.Warn("mineru conversion failed",
		logKey, logVal,
		"phase", string(failPhase),
		"kind", kindLabel(kind),
		"err", err.Error(),
		"cooldown_until", cooldown.Format(time.RFC3339),
	)
}

// runOnce is one full conversion attempt. If the PDF is missing AND a
// fetcher is configured, it pulls the PDF from arxiv first (tracked
// via PhaseFetchingPDF + FetchProgress), writes it to the object
// store, then hands off to convertStoredPDF for the MinerU step
// (PhaseConvertingMD + ConvertProgress). When the PDF is already in
// store the fetch step is skipped and Phase advances directly to
// PhaseConvertingMD.
func (c *Converter) runOnce(ctx context.Context, canonical string) error {
	pdfKey, _, exists, err := paperassets.LocateAssetByID(ctx, c.store, "pdf", canonical)
	if err != nil {
		return fmt.Errorf("stat pdf: %w", err)
	}
	if !exists {
		// Try to silently fetch from arxiv when a fetcher is wired.
		// When no fetcher is configured we preserve the pre-A2
		// behaviour and surface the missing PDF as a fatal error.
		if c.cfg.Fetcher == nil {
			return &Error{Msg: "no PDF in store for " + canonical, Kind: ErrFatal}
		}
		if err := c.fetchAndStorePDF(ctx, canonical); err != nil {
			return err
		}
		// Re-locate after fetch — write may have landed under either
		// post-A1 layout depending on id shape.
		pdfKey, _, exists, err = paperassets.LocateAssetByID(ctx, c.store, "pdf", canonical)
		if err != nil {
			return fmt.Errorf("stat pdf (post-fetch): %w", err)
		}
		if !exists {
			return &Error{Msg: "pdf not found after silent fetch (race? concurrent delete?) for " + canonical, Kind: ErrRetryable}
		}
	}

	// File name MinerU will see: must carry a real .pdf suffix (the
	// parser is chosen by extension) and must not contain path
	// separators (old-style canonicals like "quant-ph/9508027v2" do).
	uploadName := strings.ReplaceAll(canonical, "/", "_") + ".pdf"

	return c.convertStoredPDF(ctx, canonical, pdfKey, uploadName, canonical,
		func(ctx context.Context, result Result) error {
			return c.writeResult(ctx, canonical, result)
		})
}

// convertStoredPDF drives the MinerU pipeline for a PDF already present
// in the object store at pdfKey: upload via the file-urls channel, poll
// the batch, download the result zip, and hand it to writeFn for
// persistence. Shared by the arxiv path (runOnce) and the DOI path
// (runOnceDOI) — jobKey keys the Job transitions, dataID is what MinerU
// echoes back on batch results (arxiv: the canonical id; DOI: the
// "doi:<doi>" job key).
//
// Loops across the KeyRing on daily-limit errors so a single exhausted
// key does not stop the whole conversion — only when EVERY key reports
// daily-limit does the paper-level cooldown kick in. Returns a typed
// *Error on MinerU failures so c.classifyFailure can route into the
// correct counter + cooldown bucket.
func (c *Converter) convertStoredPDF(ctx context.Context, jobKey, pdfKey, uploadName, dataID string, writeFn func(context.Context, Result) error) error {
	// Transition into the convert phase. Allocate ConvertProgress so
	// the status handler has something to render before the first
	// MinerU round-trip.
	c.transition(jobKey, func(j *Job) {
		j.Phase = PhaseConvertingMD
		if j.Convert == nil {
			j.Convert = &ConvertProgress{StartedAt: c.now(), Stage: "submitting"}
		}
	})

	// Try every key in the ring until one succeeds or all return
	// daily-limit. A single key failure other than daily-limit
	// (transport hiccup, bad PDF, …) propagates out immediately
	// because it's not a key-pool problem.
	for {
		client, slot, ok := c.keyRing.Acquire()
		if !ok {
			// All keys exhausted. Surface a clear, operator-aimed message
			// so the API caller knows it's a service-wide quota issue,
			// not a problem with their specific paper.
			recovery := c.keyRing.SoonestRecovery()
			msg := "server quota exhausted: all " +
				strconv.Itoa(c.keyRing.Size()) +
				" MinerU API keys have reached today's daily limit"
			if !recovery.IsZero() {
				msg += " — service resumes at " + recovery.Local().Format(time.RFC3339)
			}
			return &Error{Msg: msg, Kind: ErrDailyLimit}
		}

		batchID, uploadURLs, err := client.ApplyUploadURLs(ctx, []string{uploadName}, SubmitOptions{
			ModelVersion:  c.cfg.MinerUModelVersion,
			Language:      c.cfg.MinerULanguage,
			EnableFormula: c.cfg.MinerUEnableFormula,
			EnableTable:   c.cfg.MinerUEnableTable,
			IsOCR:         c.cfg.MinerUIsOCR,
			DataID:        dataID,
		})
		if err != nil {
			if errors.Is(err, ErrDailyLimit) {
				c.keyRing.MarkDailyLimit(slot, c.dailyResetAt(c.now()))
				c.logger.Warn("mineru: key exhausted, rotating", "slot", slot, "remaining", c.keyRing.AvailableSlots())
				continue
			}
			return err
		}

		// Stream the PDF out of the object store and push it to
		// MinerU's presigned upload URL. No publicly reachable S3
		// endpoint needed — MinerU never fetches from us.
		rc, info, err := c.store.Get(ctx, pdfKey)
		if err != nil {
			return fmt.Errorf("read pdf from store: %w", err)
		}
		uploadErr := client.UploadFile(ctx, uploadURLs[0], rc, info.Size)
		rc.Close()
		if uploadErr != nil {
			if errors.Is(uploadErr, ErrDailyLimit) {
				c.keyRing.MarkDailyLimit(slot, c.dailyResetAt(c.now()))
				c.logger.Warn("mineru: key exhausted on upload, rotating", "slot", slot, "remaining", c.keyRing.AvailableSlots())
				continue
			}
			return uploadErr
		}

		c.transition(jobKey, func(j *Job) {
			if j.Convert != nil {
				j.Convert.MinerUTaskID = batchID
				j.Convert.Stage = "running"
			}
		})

		zipURL, err := c.pollUntilDone(ctx, client, batchID, jobKey, dataID)
		if err != nil {
			if errors.Is(err, ErrDailyLimit) {
				c.keyRing.MarkDailyLimit(slot, c.dailyResetAt(c.now()))
				c.logger.Warn("mineru: key exhausted during poll, rotating", "slot", slot, "remaining", c.keyRing.AvailableSlots())
				continue
			}
			return err
		}

		c.transition(jobKey, func(j *Job) {
			if j.Convert != nil {
				j.Convert.Stage = "downloading_zip"
			}
		})

		result, err := client.FetchResult(ctx, zipURL)
		if err != nil {
			return err
		}

		if err := writeFn(ctx, result); err != nil {
			return err
		}
		c.transition(jobKey, func(j *Job) {
			if j.Convert != nil {
				j.Convert.CompletedAt = c.now()
			}
		})
		return nil
	}
}

// fetchAndStorePDF acquires an arxiv-fetch slot, downloads the PDF
// via c.cfg.Fetcher, and writes it to the object store under the
// canonical key with IfNoneMatch:"*" (first-writer-wins idempotency
// matches upload-pdf). The PDF sha256 is attached as object metadata
// so upload-mineru's later cross-check stays consistent. Updates the
// job's Phase + FetchProgress throughout.
//
// All fetch errors are wrapped with errFetchFailed so the caller
// (run / runPDF) can label the failed Phase as PhaseErrorFetching.
func (c *Converter) fetchAndStorePDF(ctx context.Context, canonical string) error {
	parsed, err := paperassets.Parse(canonical)
	if err != nil {
		return &Error{Msg: "invalid canonical for fetch: " + err.Error(), Kind: ErrFatal}
	}

	// Acquire the arxiv-fetch semaphore (independent from MinerU
	// concurrency) so a thundering herd of 100 /pdf requests can't
	// open 100 sockets to arxiv.org. Block until a slot frees.
	select {
	case c.arxivSem <- struct{}{}:
	case <-ctx.Done():
		return &Error{Msg: "arxiv fetch cancelled: " + ctx.Err().Error(), Kind: ErrRetryable}
	}
	c.counters.InflightArxivFetches.Add(1)
	defer func() {
		<-c.arxivSem
		c.counters.InflightArxivFetches.Add(-1)
	}()

	startedAt := c.now()
	c.transition(canonical, func(j *Job) {
		j.Phase = PhaseFetchingPDF
		j.Fetch = &FetchProgress{StartedAt: startedAt}
	})
	c.counters.ArxivFetches.Add(1)

	result, err := c.cfg.Fetcher.Fetch(ctx, parsed)
	if err != nil {
		c.counters.ArxivFetchFailed.Add(1)
		c.transition(canonical, func(j *Job) {
			if j.Fetch != nil {
				j.Fetch.CompletedAt = c.now()
			}
		})
		kind := ErrRetryable
		switch {
		case errors.Is(err, arxiv.ErrNotFound):
			kind = ErrFatal
		case errors.Is(err, arxiv.ErrNotPDF), errors.Is(err, arxiv.ErrTooLarge):
			kind = ErrFatal
		case errors.Is(err, arxiv.ErrRateLimited):
			kind = ErrRetryable
		}
		return &Error{Msg: "fetch from arxiv: " + err.Error(), Kind: kind}
	}

	pdfKey := paperassets.AssetKey("pdf", canonical)
	_, putErr := c.store.PutWithOptions(ctx, pdfKey, result.Body, result.Size, objstore.PutOptions{
		ContentType: "application/pdf",
		IfNoneMatch: "*",
		Metadata: map[string]string{
			"sha256":     result.Sha256,
			"source":     "arxiv-silent-fetch",
			"fetched_by": "qatlasd-converter",
			"fetched_at": c.now().UTC().Format(time.RFC3339),
		},
	})
	if putErr != nil && !errors.Is(putErr, objstore.ErrPreconditionFailed) {
		c.counters.ArxivFetchFailed.Add(1)
		return &Error{Msg: "store pdf after fetch: " + putErr.Error(), Kind: ErrRetryable}
	}

	completedAt := c.now()
	c.transition(canonical, func(j *Job) {
		if j.Fetch == nil {
			j.Fetch = &FetchProgress{StartedAt: startedAt}
		}
		j.Fetch.CompletedAt = completedAt
		j.Fetch.BytesReceived = result.Size
		j.Fetch.BytesTotal = result.Size
		j.Fetch.Sha256 = result.Sha256
		j.Fetch.Attempts = result.Attempts
	})
	c.counters.ArxivFetchSucceeded.Add(1)

	// Registry write-through is best-effort.
	if c.catalog != nil {
		if _, _, uErr := c.catalog.UpsertPDF(ctx,
			registry.PaperRef{ArxivID: canonical},
			registry.ArxivVersionOf(canonical), result.Sha256, result.Size,
			bucketRelKey(pdfKey)); uErr != nil &&
			!errors.Is(uErr, registry.ErrCatalogUnavailable) {
			c.logger.Warn("papers: UpsertPDF write-through after silent fetch failed",
				"arxiv_id", canonical, "error", uErr)
		}
	}

	c.logger.Info("arxiv: silent fetch succeeded",
		"arxiv_id", canonical,
		"bytes", result.Size,
		"sha256", result.Sha256,
		"attempts", result.Attempts,
		"duration_seconds", c.now().Sub(startedAt).Seconds(),
	)
	return nil
}

// EnsurePDF starts (or piggybacks on) a fetch-only job for canonical.
// Unlike Ensure, this entry point never drives MinerU — it just makes
// sure the PDF bytes are in the object store, returning JobStateDone
// when they are.
//
// Internal asset-preparation path only: the public endpoint it used to
// back (GET /api/papers/{id}/pdf) now answers 410 Gone (plan §B). The
// fetch-only job machinery is retained for the internal pipeline (PDF
// presence in the store is still a conversion prerequisite and is
// surfaced via the pdf_ready debug boolean on /pdf/status).
//
// Behavior:
//   - PDF already in store → return JobStateDone+PhaseReady immediately
//     (CacheHits++).
//   - PDF missing and no fetcher → return JobStateFailed+ErrFatal (no
//     way to obtain the bytes).
//   - PDF missing and fetcher available → dedupe against any in-flight
//     job for the same canonical, queue a background fetch, return
//     JobStateQueued+PhaseFetchingPDF.
//
// Concurrent calls for the same canonical (and concurrent overlap
// with Ensure) all observe the same Job snapshot — when a markdown
// Ensure is already fetching, a /pdf EnsurePDF call piggybacks and
// returns the same in-flight Job.
func (c *Converter) EnsurePDF(ctx context.Context, canonical string) *Job {
	if _, _, exists, err := paperassets.LocateAssetByID(ctx, c.store, "pdf", canonical); err == nil && exists {
		c.counters.CacheHits.Add(1)
		return &Job{Canonical: canonical, State: JobStateDone, Phase: PhaseReady, FinishedAt: c.now()}
	}
	c.counters.CacheMisses.Add(1)

	if c.cfg.Fetcher == nil {
		return &Job{
			Canonical: canonical,
			State:     JobStateFailed,
			Phase:     PhaseErrorFetching,
			Err:       errors.New("no PDF in store and arxiv fetcher not configured"),
			ErrKind:   ErrFatal,
		}
	}

	now := c.now()

	c.mu.Lock()
	if existing, ok := c.jobs[canonical]; ok {
		switch existing.State {
		case JobStateQueued, JobStateRunning:
			cp := *existing
			c.mu.Unlock()
			return &cp
		case JobStateDone:
			cp := *existing
			c.mu.Unlock()
			return &cp
		case JobStateFailed:
			if !existing.CooldownUntil.IsZero() && now.Before(existing.CooldownUntil) {
				cp := *existing
				c.mu.Unlock()
				return &cp
			}
		}
	}

	job := &Job{
		Canonical:   canonical,
		State:       JobStateQueued,
		Phase:       PhaseFetchingPDF,
		SubmittedAt: now,
	}
	c.jobs[canonical] = job
	c.mu.Unlock()

	go c.runPDF(canonical)

	cp := *job
	return &cp
}

// runPDF is the fetch-only background driver used by EnsurePDF.
// Mirrors run() but skips the MinerU pipeline — the PDF lands in the
// store and the job transitions Done+Ready.
func (c *Converter) runPDF(canonical string) {
	ctx, cancel := context.WithTimeout(context.Background(), c.cfg.MinerUTimeout)
	defer cancel()

	c.transition(canonical, func(j *Job) {
		j.State = JobStateRunning
		j.StartedAt = c.now()
	})

	err := c.fetchAndStorePDF(ctx, canonical)
	if err == nil {
		c.transition(canonical, func(j *Job) {
			j.State = JobStateDone
			j.Phase = PhaseReady
			j.FinishedAt = c.now()
		})
		return
	}

	kind, cooldown := c.classifyFailure(err)
	c.transition(canonical, func(j *Job) {
		j.State = JobStateFailed
		j.Phase = PhaseErrorFetching
		j.FinishedAt = c.now()
		j.Err = err
		j.ErrKind = kind
		j.CooldownUntil = cooldown
	})
	c.logger.Warn("arxiv: silent fetch (PDF-only) failed",
		"arxiv_id", canonical,
		"kind", kindLabel(kind),
		"err", err.Error(),
		"cooldown_until", cooldown.Format(time.RFC3339),
	)
}

// pollUntilDone polls MinerU's batch-results endpoint at
// cfg.MinerUPollInterval until the file's state transitions to done
// or failed, or the per-job context expires. Uses the supplied client
// so the caller controls which token / which key-ring slot the polling
// traffic charges against. Per-poll progress (PolledCount) is folded
// into the Job's ConvertProgress so the status handler can show
// movement even during long-running jobs.
//
// The upload channel yields exactly one file per batch, so the first
// reported entry is authoritative; DataID is checked when present as a
// sanity guard against dataID (we submit DataID=dataID; jobKey only
// keys the local Job transitions).
func (c *Converter) pollUntilDone(ctx context.Context, client *Client, batchID, jobKey, dataID string) (string, error) {
	tick := time.NewTicker(c.cfg.MinerUPollInterval)
	defer tick.Stop()
	for {
		results, err := client.GetBatch(ctx, batchID)
		if err != nil {
			return "", err
		}
		c.transition(jobKey, func(j *Job) {
			if j.Convert != nil {
				j.Convert.PolledCount++
			}
		})
		if len(results) > 0 {
			st := results[0]
			for _, r := range results {
				if r.DataID == dataID {
					st = r
					break
				}
			}
			switch st.State {
			case "done":
				if st.FullZipURL == "" {
					return "", &Error{Msg: "task done but full_zip_url empty", Kind: ErrRetryable}
				}
				return st.FullZipURL, nil
			case "failed":
				return "", classifyAPIError("", st.ErrMsg, 0)
			}
			// Anything else (waiting-file / pending / running /
			// converting) is in-flight — keep polling.
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("poll timeout: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// writeResult writes images (as a single zip) first, then markdown,
// mirroring the upload-mineru contributor handler's ordering — readers
// see markdown only after the referenced image archive is durable.
// Arxiv-keyed wrapper around writeResultTo.
func (c *Converter) writeResult(ctx context.Context, canonical string, result Result) error {
	mdKey := paperassets.AssetKey("markdown", canonical)
	return c.writeResultTo(ctx,
		paperassets.AssetKey("images", canonical), mdKey, result,
		func(mdSha string, mdSize int64, imageCount int) error {
			return c.upsertMDWriteThrough(ctx, canonical, mdSha, mdSize, imageCount)
		},
		"arxiv_id", canonical,
	)
}

// writeResultDOI is the DOI-keyed writeResult: assets land under the
// "<kind>/doi/<reg>/<suffix>" layout and the catalog write-through goes
// through UpsertMDByDOI (source='published').
func (c *Converter) writeResultDOI(ctx context.Context, doi string, result Result) error {
	mdKey := paperassets.DOIAssetKey("markdown", doi)
	return c.writeResultTo(ctx,
		paperassets.DOIAssetKey("images", doi), mdKey, result,
		func(mdSha string, mdSize int64, imageCount int) error {
			return c.catalog.UpsertMDByDOI(ctx,
				registry.PaperRef{DOI: doi}, mdSha, mdSize,
				bucketRelKey(mdKey),
				bucketRelKey(paperassets.DOIAssetKey("json", doi)),
				imageCount)
		},
		"doi", doi,
	)
}

// writeResultTo is the shared asset-write tail of the MinerU pipeline.
// upsertMD performs the registry write-through (best-effort: failures
// are logged, not propagated). logKey/logVal pick the log field naming.
//
// Uses IfNoneMatch:"*" so a concurrent edge that wrote first wins (we
// observe 412 and treat that as success).
//
// The zip stores images flat (no directory prefix) using STORE
// compression (images are already compressed; re-deflating wastes CPU
// for ~0 savings). File names inside the zip match what MinerU emits
// (typically "images/<sha256>.jpg"); we strip the leading "images/"
// before writing the entry so the zip internal layout is flat — same
// convention the contributor upload path uses.
func (c *Converter) writeResultTo(ctx context.Context, imgKey, mdKey string, result Result, upsertMD func(mdSha string, mdSize int64, imageCount int) error, logKey, logVal string) error {
	// --- Images zip ---
	if len(result.Images) > 0 {
		zipBytes, err := BuildImagesZip(result.Images)
		if err != nil {
			return fmt.Errorf("build images zip: %w", err)
		}

		_, err = c.store.PutWithOptions(ctx, imgKey, bytes.NewReader(zipBytes), int64(len(zipBytes)), objstore.PutOptions{
			ContentType: "application/zip",
			IfNoneMatch: "*",
		})
		if err != nil && !errors.Is(err, objstore.ErrPreconditionFailed) {
			return fmt.Errorf("put images zip: %w", err)
		}
	}

	// --- Markdown ---
	mdSize := int64(len(result.Markdown))
	_, err := c.store.PutWithOptions(ctx, mdKey, bytes.NewReader(result.Markdown), mdSize, objstore.PutOptions{
		ContentType: "text/markdown; charset=utf-8",
		IfNoneMatch: "*",
	})
	if err != nil && !errors.Is(err, objstore.ErrPreconditionFailed) {
		return fmt.Errorf("put markdown: %w", err)
	}

	// Registry write-through is best-effort.
	if c.catalog != nil {
		sum := sha256.Sum256(result.Markdown)
		mdSha := hex.EncodeToString(sum[:])
		if uErr := upsertMD(mdSha, mdSize, len(result.Images)); uErr != nil &&
			!errors.Is(uErr, registry.ErrCatalogUnavailable) {
			c.logger.Warn("papers: UpsertMD write-through failed after conversion",
				logKey, logVal, "error", uErr)
		}
	}

	// The paper has markdown now: push the index build to qatlas-rag.
	// Best-effort — a push failure must not fail the conversion.
	if c.cfg.IndexPusher != nil {
		if err := c.cfg.IndexPusher.PushIndex(ctx, logVal); err != nil {
			c.logger.Warn("mineru: rag index push failed", logKey, logVal, "error", err)
		}
	}

	c.logger.Info("mineru conversion succeeded",
		logKey, logVal,
		"md_bytes", mdSize,
		"image_count", len(result.Images),
	)
	return nil
}

// classifyFailure routes err into one of the counters and computes a
// cooldown deadline. Returns the kind sentinel so the Job carries it
// for the /markdown/status response.
func (c *Converter) classifyFailure(err error) (kind error, cooldown time.Time) {
	now := c.now()
	switch {
	case errors.Is(err, ErrDailyLimit):
		c.counters.FailedDailyLimit.Add(1)
		return ErrDailyLimit, c.dailyResetAt(now)
	case errors.Is(err, ErrNoDOISource):
		// Fatal semantics (nothing will change on retry within the
		// minute), but the Job keeps the ErrNoDOISource sentinel in Err
		// so the handler can render 404 + contrib hint instead of 502.
		c.counters.FailedFatal.Add(1)
		return ErrFatal, now.Add(FailureCooldown)
	case errors.Is(err, ErrFatal):
		c.counters.FailedFatal.Add(1)
		return ErrFatal, now.Add(FailureCooldown)
	case errors.Is(err, ErrRetryable):
		c.counters.FailedRetryable.Add(1)
		return ErrRetryable, now.Add(FailureCooldown)
	default:
		// Unclassified — treat as retryable so we don't permanently
		// give up on transient unknowns.
		c.counters.FailedRetryable.Add(1)
		return ErrRetryable, now.Add(FailureCooldown)
	}
}

// transition applies mutate to the job for canonical under the
// converter mutex. No-op when no job exists (defensive against
// reordering with cooldown expiry).
func (c *Converter) transition(canonical string, mutate func(*Job)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.jobs[canonical]
	if !ok {
		return
	}
	mutate(j)
}

// nextLocalDailyReset returns the next "00:01 local" instant strictly
// after now. Used as the daily-limit cooldown end: holding all
// submissions until ~1 minute past midnight gives MinerU's
// quota reset (we don't know the exact instant) a comfortable buffer.
func nextLocalDailyReset(now time.Time) time.Time {
	y, m, d := now.Date()
	reset := time.Date(y, m, d, 0, 1, 0, 0, now.Location())
	if !reset.After(now) {
		reset = reset.Add(24 * time.Hour)
	}
	return reset
}

func kindLabel(kind error) string {
	switch {
	case errors.Is(kind, ErrFatal):
		return "fatal"
	case errors.Is(kind, ErrRetryable):
		return "retryable"
	case errors.Is(kind, ErrDailyLimit):
		return "daily_limit"
	default:
		return "unknown"
	}
}
