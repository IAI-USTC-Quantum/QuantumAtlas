package mineru

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// Converter drives source-pinned MinerU V1 parsing behind the paper-access
// master switch. PDF-only access freezes/verifies bytes without inference or
// API tokens. Content access ignores all legacy outputs and is ready only when
// the exact source SHA and every complete-manifest member verify.
//
// Concurrent callers share per-process jobs, bounded by the conversion and PDF
// fetch semaphores. Token-ring quota rotation and failure cooldowns are retained.
// Full result files are persisted unchanged under a new immutable revision;
// publication/current pointers advance only after durable verification succeeds.
//
// Jobs/leases are not shared across edges or persisted across process restarts.
// Complete immutable bundles survive restarts; partial packages are not cache
// hits. Cross-edge requests can still consume duplicate provider quota.
// Safe for concurrent use.
type Converter struct {
	cfg     ConverterConfig
	store   objstore.Store
	catalog *registry.Store
	sources SourceCatalog
	aliases map[string]string // canonical/source lookup alias -> SHA-pinned job key
	keyRing *KeyRing
	tokens  *TokenStore
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

	// MinerUTokenEntries overrides MinerUAPITokens when non-nil: the
	// DB-backed pool (token + rotated_at per entry) loaded at boot by
	// TokenStore.BootSync. Nil keeps the plain config-strings boot.
	MinerUTokenEntries []TokenEntry

	// TokenStore, when non-nil, persists admin token mutations (see
	// AddToken / RemoveToken). Nil = config-only boot; admin mutations
	// then apply to the in-memory ring alone and are lost on restart.
	TokenStore *TokenStore

	// Fetcher, when non-nil, allows the converter to silent-fetch
	// missing PDFs from arxiv.org before driving MinerU. When nil the
	// pre-A2 behavior is preserved: Ensure on a paper with no stored
	// PDF returns ErrFatal "no PDF in store for <id>" so the caller
	// must upload the PDF before requesting markdown. Set the
	// fetcher to opt the deployment into silent fetch + convert (the
	// QATLAS_PAPER_ACCESS_ENABLED master switch enables both ends).
	Fetcher *arxiv.Fetcher

	// DOIPDFURL discovers only a known publisher/OA PDF URL for a genuinely
	// absent DOI source. It runs inside the async, deduped fetch-only job;
	// returning no URL means ErrNoDOISource, never arXiv-edition substitution.
	DOIPDFURL func(context.Context, string) (string, error)

	// VerifyDOIPDF validates fresh publisher PDF ownership before any source
	// freeze/publication. Stored frozen sources never revalidate over network.
	VerifyDOIPDF func(context.Context, string, []byte) error

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

	// MinerUTier is the V1 API tier; v4 model_version is NOT a tier.
	MinerUTier string
	// SourceCatalog allows mocked source/revision storage in tests. Production
	// defaults to the registry passed to NewConverter.
	SourceCatalog SourceCatalog
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
	PaperID         string
	SourceID        string
	RevisionID      string
	SourcePDFSHA256 string
	Canonical       string
	State           JobState
	Phase           Phase
	SubmittedAt     time.Time
	StartedAt       time.Time
	FinishedAt      time.Time
	Err             error
	ErrKind         error // ErrFatal / ErrRetryable / ErrDailyLimit, nil otherwise
	CooldownUntil   time.Time

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
	oaPdfURL        string
	parseAfterFetch bool // set only by an actual content request
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
	if cfg.MinerUTimeout <= 0 {
		cfg.MinerUTimeout = 30 * time.Minute
	}
	if cfg.MinerUPollInterval <= 0 {
		cfg.MinerUPollInterval = time.Second
	}
	if cfg.MinerUTier == "" {
		cfg.MinerUTier = "standard"
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
		aliases:      map[string]string{},
		sources:      cfg.SourceCatalog,
	}
	if c.sources == nil && catalog != nil {
		c.sources = catalog
	}
	if cfg.Fetcher != nil {
		c.arxivSem = make(chan struct{}, arxivConc)
	}
	if cfg.MinerUTokenEntries != nil {
		c.keyRing = NewKeyRingFromEntries(cfg.MinerUTokenEntries, cfg.MinerUAPIBaseURL, c.now)
	} else {
		c.keyRing = NewKeyRing(cfg.MinerUAPITokens, cfg.MinerUAPIBaseURL, c.now)
	}
	c.tokens = cfg.TokenStore
	c.mu.Lock()
	c.recomputeEnabledLocked()
	c.mu.Unlock()

	return c
}

// recomputeEnabledLocked refreshes the enabled switch after the ring
// size changes at runtime (AddToken / RemoveToken). Inputs: the
// paper_access master switch and the ring size; wording mirrors the
// boot-time messages. Caller must hold c.mu (the ring takes its own
// lock inside Size — lock order is always c.mu → ring).
func (c *Converter) recomputeEnabledLocked() {
	switch {
	case !c.cfg.PaperAccessEnabled:
		c.enabled = false
		c.disabledMsg = "asset downloads disabled (QATLAS_PAPER_ACCESS_ENABLED=false)"
	case c.keyRing.Size() == 0:
		c.enabled = false
		c.disabledMsg = "MinerU not configured (MINERU_API_TOKENS unset); cache-only mode"
	default:
		c.enabled = true
		c.disabledMsg = ""
	}
}

// KeyRingSize returns how many MinerU API tokens are loaded into the
// rotation pool — for inclusion in the startup log so operators can
// see at a glance how many keys are being managed.
func (c *Converter) KeyRingSize() int { return c.keyRing.Size() }

// TokenSnapshot is the admin-facing view of one MinerU API token. The
// raw secret never crosses the process boundary: id (hash prefix)
// addresses it for mutations, masked is a display preview.
type TokenSnapshot struct {
	ID            string     `json:"id"`
	Masked        string     `json:"masked"`
	RotatedAt     time.Time  `json:"rotated_at"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	Available     bool       `json:"available"`
}

// ErrLastToken guards the pool against deleting its final entry —
// that would silently disable the whole server-side conversion
// pipeline. Rotate instead: add the replacement first, then remove.
var ErrLastToken = errors.New("refusing to remove the last MinerU token; add a replacement first")

// ErrEmptyToken is returned by AddToken for blank / whitespace-only
// submissions.
var ErrEmptyToken = errors.New("token must not be empty")

// TokenStoreConfigured reports whether admin token mutations are
// persisted (registry PostgreSQL wired). False means mutations only
// affect the in-memory ring and are lost on restart.
func (c *Converter) TokenStoreConfigured() bool { return c.tokens.Configured() }

// BootSyncTokens seeds the config token list into the store and swaps
// the ring over to the DB-backed pool. Called from the post-migration
// hook in main.go: the registry schema (incl. mineru_tokens) is
// ensured in the background AFTER the converter is constructed, so
// the DB pool can only be reconciled once that migration finished —
// otherwise a fresh database would boot config-only and only pick the
// table up on the SECOND restart.
func (c *Converter) BootSyncTokens(ctx context.Context, cfgTokens []string) error {
	if !c.tokens.Configured() {
		return errors.New("mineru token store: not configured")
	}
	entries, err := c.tokens.BootSync(ctx, cfgTokens)
	if err != nil {
		return err
	}
	c.keyRing.SetEntries(entries)
	c.mu.Lock()
	c.recomputeEnabledLocked()
	c.mu.Unlock()
	return nil
}

// TokensSnapshot lists the live pool in ring order: masked previews,
// rotation timestamps, and daily-quota cooldown state.
func (c *Converter) TokensSnapshot() []TokenSnapshot {
	states := c.keyRing.States()
	out := make([]TokenSnapshot, 0, len(states))
	now := c.now()
	for _, st := range states {
		snap := TokenSnapshot{
			ID:        TokenID(st.Token),
			Masked:    MaskToken(st.Token),
			RotatedAt: st.RotatedAt,
			Available: st.CooldownUntil.IsZero() || !now.Before(st.CooldownUntil),
		}
		if !st.CooldownUntil.IsZero() {
			until := st.CooldownUntil
			snap.CooldownUntil = &until
		}
		out = append(out, snap)
	}
	return out
}

// AddToken rotates a token into the live pool: persisted first (when
// a store is wired, so a crash after the ring update can't lose the
// token), then upserted into the ring, then the enabled switch is
// recomputed — adding the first token to a cache-only deployment
// turns server-side conversion on without a restart. Re-adding a
// known token only refreshes its rotated_at (cooldown untouched).
func (c *Converter) AddToken(ctx context.Context, token string) (TokenSnapshot, error) {
	token = strings.TrimSpace(token)
	if token == "" {
		return TokenSnapshot{}, ErrEmptyToken
	}
	rotatedAt := c.now()
	if c.tokens.Configured() {
		if err := c.tokens.Upsert(ctx, token, rotatedAt); err != nil {
			return TokenSnapshot{}, err
		}
	}
	c.keyRing.AddToken(token, rotatedAt)
	c.mu.Lock()
	c.recomputeEnabledLocked()
	c.mu.Unlock()
	id := TokenID(token)
	for _, snap := range c.TokensSnapshot() {
		if snap.ID == id {
			return snap, nil
		}
	}
	// Unreachable: the token was just upserted into the ring.
	return TokenSnapshot{}, ErrEmptyToken
}

// RemoveToken drops the token addressed by its hash id from the pool
// and the store. Refuses to remove the last entry (ErrLastToken).
// ErrTokenNotFound when no live entry matches the id.
func (c *Converter) RemoveToken(ctx context.Context, id string) error {
	for _, st := range c.keyRing.States() {
		if TokenID(st.Token) != id {
			continue
		}
		if c.keyRing.Size() <= 1 {
			return ErrLastToken
		}
		if c.tokens.Configured() {
			if err := c.tokens.Delete(ctx, st.Token); err != nil && !errors.Is(err, ErrTokenNotFound) {
				return err
			}
		}
		c.keyRing.RemoveToken(st.Token)
		c.mu.Lock()
		c.recomputeEnabledLocked()
		c.mu.Unlock()
		return nil
	}
	return ErrTokenNotFound
}

// Enabled reports whether the converter will actually drive MinerU on
// cache miss. False when the switch is off or no API token is
// configured. The handler falls back to "cache only; 503 on miss"
// when false. Mu-guarded: the pool can gain / lose its last token at
// runtime via the admin token surface.
func (c *Converter) Enabled() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.enabled
}

// DisabledReason returns a human-readable explanation of why the
// converter is disabled, suitable for the body of a 503 response.
// Empty when Enabled() == true.
func (c *Converter) DisabledReason() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.disabledMsg
}

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
	requested := canonical
	c.mu.Lock()
	if key, ok := c.aliases[canonical]; ok {
		canonical = key
	}
	j, ok := c.jobs[canonical]
	if !ok {
		c.mu.Unlock()
		return nil, false
	}
	cp := cloneJob(j)
	if requested != canonical && !strings.HasPrefix(requested, "source:") {
		cp.Canonical = strings.TrimPrefix(requested, "doi:")
	}
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

	select {
	case c.sem <- struct{}{}:
	case <-ctx.Done():
		kind, cooldown := c.classifyFailure(ctx.Err())
		c.transition(jobKey, func(j *Job) {
			j.State = JobStateFailed
			j.Err = ctx.Err()
			j.ErrKind = kind
			j.CooldownUntil = cooldown
			j.FinishedAt = c.now()
		})
		return
	}
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
