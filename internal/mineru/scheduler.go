package mineru

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/safego"
)

// scheduler.go: the daily 00:00 auto-conversion batch driver.
//
// Once per local midnight the Scheduler walks the registry's
// needs-mineru queue (papers with a PDF but no markdown) and drives
// Converter.Ensure for each row, serially, until one of the stop
// conditions fires:
//
//   - StopReasonQueueEmpty — every row is either converted or was
//     already attempted during this run (permanently-failing papers
//     stay in the queue, so "no un-attempted rows left" also counts
//     as drained; the failed-today count in the run log says which).
//   - StopReasonDisabled — the converter is not enabled (switch off,
//     no tokens, no public S3 endpoint). Re-evaluated fresh at every
//     midnight tick.
//   - StopReasonQuota — a job failed with ErrDailyLimit, i.e. every
//     key in the KeyRing reported today's quota spent. The run aborts
//     immediately; the next midnight tick retries automatically.
//   - StopReasonTokenError — a job failed with a token-level fatal
//     (HTTP 401/403, MinerU A0202/A0211). Every subsequent submission
//     would fail identically, so continuing would just burn the queue
//     into per-paper cooldowns; abort and wait for an operator (or a
//     config reload on the next day) instead.
//   - StopReasonFetchError — the registry queue read itself failed.
//   - StopReasonShutdown — the process is terminating.
//   - StopReasonDailyCap — successful conversions (scheduled + manual
//     runs share one counter) hit dailyCap for the current local
//     calendar day. The counter resets at local midnight, so the next
//     midnight tick resumes automatically.
//
// Runs are mutually exclusive (singleflight): a midnight tick that
// fires while a previous run is still in progress is skipped (logged)
// and the in-flight run continues; RunNow coalesces through the same
// guard, so a manual trigger during the nightly batch returns
// started=false with reason "already_running".
//
// Why 00:01 and not 00:00 sharp: the converter holds daily-limit
// failures on cooldown until "next 00:01 local" (nextLocalDailyReset).
// A run firing at 00:00:00 would re-Observe yesterday's still-cooling
// ErrDailyLimit jobs and abort instantly, wasting the whole day after
// any quota-exhausted day. Reusing the same reset boundary keeps the
// two clocks coherent.
//
// Runs are SERIAL: one Ensure at a time, waiting for each job to reach
// a terminal state before submitting the next. The converter's own
// concurrency (MaxConcurrentJobs) is sized for interactive HTTP
// traffic; the nightly batch deliberately stays polite to the MinerU
// free API — throughput is bounded by MinerU server-side anyway.
//
// Safe for concurrent use. The Scheduler never mutates the Converter
// or registry beyond what Ensure/UpsertMD already do.
type Scheduler struct {
	conv   converterDriver
	queue  queueReader
	logger *slog.Logger
	now    func() time.Time

	tickInterval time.Duration // >0: fire every interval (tests); 0: daily at local midnight reset
	batchSize    int           // per-fetch NeedsMineru limit
	itemSpacing  time.Duration // pause between items + terminal-state poll interval
	dailyCap     int           // max successful conversions per local calendar day; <=0 = unlimited

	// baseCtx outlives any single caller: manual (RunNow) batches
	// execute on it so a disconnecting HTTP caller doesn't kill the
	// run. Stop cancels it.
	baseCtx    context.Context
	baseCancel context.CancelFunc

	mu             sync.Mutex
	started        bool
	cancel         context.CancelFunc
	running        bool // the singleflight slot: exactly one batch run at a time
	nextRunAt      *time.Time
	lastStart      *time.Time
	lastFinish     *time.Time
	lastStop       StopReason
	lastStats      runStats
	capDay         string // local calendar day (YYYY-MM-DD) convertedToday belongs to
	convertedToday int    // successful conversions since local midnight, shared by scheduled + manual runs

	wg sync.WaitGroup
}

// converterDriver is the slice of *Converter the scheduler drives,
// factored out (as internal/ingest did with registryWriter) so tests
// can fake the conversion pipeline without MinerU. *Converter
// satisfies it implicitly.
type converterDriver interface {
	Enabled() bool
	Ensure(ctx context.Context, canonical string) *Job
	Lookup(canonical string) (*Job, bool)
}

// queueReader is the slice of *registry.Store the scheduler reads,
// factored out so tests can fake the catalog without a database.
// *registry.Store satisfies it implicitly.
type queueReader interface {
	NeedsMineru(ctx context.Context, limit int) ([]registry.NeedsMineruRow, error)
}

// StopReason records why a batch run ended. Persisted on the
// Scheduler and surfaced via Snapshot → /api/health.
type StopReason string

const (
	StopReasonQueueEmpty StopReason = "queue_empty"        // nothing left to convert (or all remaining rows failed this run)
	StopReasonDisabled   StopReason = "converter_disabled" // converter.Enabled() == false at tick time
	StopReasonQuota      StopReason = "quota_exhausted"    // ErrDailyLimit: every API key spent; retry next midnight
	StopReasonTokenError StopReason = "token_error"        // token-level fatal (401/403/A0202/A0211); operator action needed
	StopReasonFetchError StopReason = "queue_fetch_error"  // registry NeedsMineru read failed
	StopReasonShutdown   StopReason = "shutdown"           // context cancelled (process terminating)
	StopReasonDailyCap   StopReason = "daily_cap_reached"  // per-day conversion cap hit; resumes at next midnight
)

// DefaultDailyCap caps automatic conversions per local calendar day.
// MinerU's free quota is 5000 submissions/day/token; 4000 leaves
// ~1000 of headroom for interactive (HTTP-driven) conversions on a
// single-token deployment.
const DefaultDailyCap = 4000

// runStats are the per-run tallies surfaced via Snapshot.
type runStats struct {
	Processed int `json:"processed"` // Ensure calls made
	Converted int `json:"converted"` // terminal State=Done
	Failed    int `json:"failed"`    // terminal State=Failed (any kind)
}

// SchedulerSnapshot is a point-in-time view of the scheduler state for
// /api/health. Pointer time fields are nil until the event has
// happened at least once.
type SchedulerSnapshot struct {
	Running         bool       `json:"running"`
	NextRunAt       *time.Time `json:"next_run_at,omitempty"`
	LastRunStarted  *time.Time `json:"last_run_started,omitempty"`
	LastRunFinished *time.Time `json:"last_run_finished,omitempty"`
	LastStopReason  StopReason `json:"last_stop_reason,omitempty"`
	LastRun         runStats   `json:"last_run"`
	// DailyCap is the configured per-calendar-day conversion cap
	// (0 = unlimited). ConvertedToday is the shared counter for the
	// current local day; CapDay is the day it belongs to (YYYY-MM-DD).
	DailyCap       int    `json:"daily_cap"`
	ConvertedToday int    `json:"converted_today"`
	CapDay         string `json:"cap_day"`
}

// SchedulerOption configures a Scheduler.
type SchedulerOption func(*Scheduler)

// WithTickInterval replaces the default daily-at-midnight schedule
// with a fixed interval between run starts. Intended for tests.
func WithTickInterval(d time.Duration) SchedulerOption {
	return func(s *Scheduler) {
		if d > 0 {
			s.tickInterval = d
		}
	}
}

// WithBatchSize sets the per-fetch NeedsMineru limit. Values < 1 fall
// back to the default (200).
func WithBatchSize(n int) SchedulerOption {
	return func(s *Scheduler) {
		if n >= 1 {
			s.batchSize = n
		}
	}
}

// WithItemSpacing sets the pause between consecutive Ensure calls
// (politeness toward the MinerU API) and doubles as the poll interval
// while waiting for a job's terminal state. Default 2s; zero is
// allowed (tests).
func WithItemSpacing(d time.Duration) SchedulerOption {
	return func(s *Scheduler) {
		if d >= 0 {
			s.itemSpacing = d
		}
	}
}

// WithDailyCap sets the maximum number of successful conversions per
// local calendar day, shared by scheduled and manual runs. Default
// DefaultDailyCap (4000); values <= 0 mean unlimited.
func WithDailyCap(n int) SchedulerOption {
	return func(s *Scheduler) {
		if n > 0 {
			s.dailyCap = n
		} else {
			s.dailyCap = 0
		}
	}
}

// WithClock replaces the wall clock. Intended for tests that need to
// cross a local-midnight boundary (daily-counter rollover) without
// sleeping.
func WithClock(now func() time.Time) SchedulerOption {
	return func(s *Scheduler) {
		if now != nil {
			s.now = now
		}
	}
}

// NewScheduler builds the daily batch driver over the converter and
// the registry queue. Both are interface seams; production passes
// *Converter and *registry.Store.
func NewScheduler(conv converterDriver, reg queueReader, logger *slog.Logger, opts ...SchedulerOption) *Scheduler {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Scheduler{
		conv:        conv,
		queue:       reg,
		logger:      logger,
		now:         time.Now,
		batchSize:   200,
		itemSpacing: 2 * time.Second,
		dailyCap:    DefaultDailyCap,
	}
	s.baseCtx, s.baseCancel = context.WithCancel(context.Background())
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Start launches the scheduling loop in a background goroutine. The
// first run fires at the next local midnight reset (or after
// tickInterval when WithTickInterval is set). Idempotent — a second
// call is a no-op. The loop exits when ctx is cancelled or Stop is
// called.
func (s *Scheduler) Start(ctx context.Context) {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()

	ctx, cancel := context.WithCancel(ctx)
	s.mu.Lock()
	s.cancel = cancel
	s.mu.Unlock()

	s.wg.Add(1)
	// Waited goroutine (Stop blocks on wg), so safego.Go is wrong here
	// per its contract — use the LogPanic pattern instead.
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				safego.LogPanic("mineru.scheduler", r)
			}
		}()
		s.loop(ctx)
	}()
}

// Stop cancels the scheduling loop and any in-flight run, then blocks
// until all goroutines have exited. An in-flight run aborts at the
// next Ensure / poll boundary.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	cancel := s.cancel
	s.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	s.baseCancel()
	s.wg.Wait()
}

// Snapshot returns a consistent read of the scheduler state. Cheap —
// safe to call from /api/health on every request.
func (s *Scheduler) Snapshot() SchedulerSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := func(t *time.Time) *time.Time {
		if t == nil {
			return nil
		}
		v := *t
		return &v
	}
	// Normalize the daily counter for display: a stale capDay means the
	// day rolled over since the last conversion — report today's key
	// with a zero count (the stored counter resets lazily on the next
	// noteConverted).
	capDay, converted := s.capDay, s.convertedToday
	if day := todayKey(s.now()); capDay != day {
		capDay, converted = day, 0
	}
	return SchedulerSnapshot{
		Running:         s.running,
		NextRunAt:       cp(s.nextRunAt),
		LastRunStarted:  cp(s.lastStart),
		LastRunFinished: cp(s.lastFinish),
		LastStopReason:  s.lastStop,
		LastRun:         s.lastStats,
		DailyCap:        s.dailyCap,
		ConvertedToday:  converted,
		CapDay:          capDay,
	}
}

// RunNow triggers a batch run immediately, in the background, coalesced
// through the same singleflight slot the midnight loop uses. The run
// executes on the scheduler's own lifecycle context (cancelled by
// Stop), so a disconnecting HTTP caller doesn't kill it; the caller's
// ctx is only consulted up front — an already-cancelled ctx is a no-op.
//
// Returns (true, "started") when a new run was launched. Otherwise
// started=false with a machine-readable reason: "already_running",
// "converter_disabled", or "context_cancelled".
func (s *Scheduler) RunNow(ctx context.Context) (started bool, reason string) {
	if ctx.Err() != nil {
		return false, "context_cancelled"
	}
	if !s.conv.Enabled() {
		return false, string(StopReasonDisabled)
	}
	if !s.tryBeginRun() {
		return false, "already_running"
	}
	s.wg.Add(1)
	// Waited goroutine (Stop blocks on wg) — LogPanic pattern, same as
	// the loop goroutine in Start.
	go func() {
		defer s.wg.Done()
		defer func() {
			if r := recover(); r != nil {
				safego.LogPanic("mineru.scheduler.manual-run", r)
			}
		}()
		s.runBatch(s.baseCtx)
	}()
	return true, "started"
}

// tryBeginRun atomically claims the singleflight run slot. True means
// the caller owns the run and MUST funnel into runBatch (which releases
// the slot in its defer). False means a run is already active.
func (s *Scheduler) tryBeginRun() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return false
	}
	s.running = true
	started := s.now()
	s.lastStart = &started
	return true
}

// capReached reports whether today's shared conversion counter has hit
// dailyCap. The counter rolls over lazily: a stale capDay (yesterday)
// reads as not-reached regardless of the stored count.
func (s *Scheduler) capReached() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.dailyCap <= 0 {
		return false
	}
	if s.capDay != todayKey(s.now()) {
		return false
	}
	return s.convertedToday >= s.dailyCap
}

// noteConverted increments today's shared conversion counter, rolling
// the day over at local midnight. Called once per successful
// conversion, from scheduled and manual runs alike.
func (s *Scheduler) noteConverted() {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := todayKey(s.now())
	if s.capDay != day {
		s.capDay = day
		s.convertedToday = 0
	}
	s.convertedToday++
}

// todayKey returns the local calendar-day bucket key for t. The daily
// conversion counter resets when this key changes.
func todayKey(t time.Time) string { return t.Format("2006-01-02") }

// loop waits for the next scheduled instant, fires a run, repeat.
func (s *Scheduler) loop(ctx context.Context) {
	for {
		wait := s.untilNextRun(s.now())
		next := s.now().Add(wait)
		s.mu.Lock()
		s.nextRunAt = &next
		s.mu.Unlock()

		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		// Singleflight: a tick firing while a previous run (yesterday's
		// over-long batch, or a manual RunNow) is still active is
		// skipped — never run two batches concurrently.
		if !s.tryBeginRun() {
			s.logger.Info("mineru scheduler: tick skipped, previous run still in progress")
		} else {
			s.runBatch(ctx)
		}
		if ctx.Err() != nil {
			return
		}
	}
}

// untilNextRun returns how long to wait before the next batch run.
// Default: until the next local 00:01 (the converter's daily-quota
// reset boundary — see the type doc). WithTickInterval overrides with
// a fixed interval.
func (s *Scheduler) untilNextRun(now time.Time) time.Duration {
	if s.tickInterval > 0 {
		return s.tickInterval
	}
	return nextLocalDailyReset(now).Sub(now)
}

// runBatch executes one full pass over the needs-mineru queue. Called
// by the loop at each scheduled instant and by RunNow for manual
// triggers; both claim the singleflight slot via tryBeginRun first
// (tests may call runBatch directly, which bypasses the guard). The
// slot is released in the defer below.
func (s *Scheduler) runBatch(ctx context.Context) {
	started := s.now()

	reason := StopReasonQueueEmpty
	var stats runStats
	defer func() {
		finished := s.now()
		s.mu.Lock()
		s.running = false
		s.lastFinish = &finished
		s.lastStop = reason
		s.lastStats = stats
		s.mu.Unlock()
		s.logger.Info("mineru scheduler: run finished",
			"stop_reason", string(reason),
			"processed", stats.Processed,
			"converted", stats.Converted,
			"failed", stats.Failed,
			"duration_seconds", finished.Sub(started).Seconds(),
		)
	}()

	if !s.conv.Enabled() {
		reason = StopReasonDisabled
		s.logger.Info("mineru scheduler: converter not enabled, skipping run")
		return
	}

	// attempted guards against an infinite loop over permanently-
	// failing papers: a fatal failure (bad PDF, >200 pages) never
	// leaves the needs-mineru queue (UpsertMD never fires), so without
	// this set the "re-fetch until empty" loop would resubmit the same
	// rows forever.
	attempted := map[string]struct{}{}

	for {
		rows, err := s.queue.NeedsMineru(ctx, s.batchSize)
		if err != nil {
			if ctx.Err() != nil {
				reason = StopReasonShutdown
			} else {
				reason = StopReasonFetchError
				s.logger.Warn("mineru scheduler: needs-mineru fetch failed", "error", err)
			}
			return
		}

		fresh := make([]registry.NeedsMineruRow, 0, len(rows))
		for _, row := range rows {
			if _, seen := attempted[row.ArxivID]; !seen {
				fresh = append(fresh, row)
			}
		}
		if len(fresh) == 0 {
			reason = StopReasonQueueEmpty
			return
		}

		for _, row := range fresh {
			if ctx.Err() != nil {
				reason = StopReasonShutdown
				return
			}
			// Shared per-day cap (scheduled + manual runs). Checked
			// before each submission so the counter never exceeds the
			// cap; noteConverted below bumps it on success.
			if s.capReached() {
				reason = StopReasonDailyCap
				s.logger.Info("mineru scheduler: daily conversion cap reached, stopping run; resumes at next midnight",
					"daily_cap", s.dailyCap,
				)
				return
			}
			attempted[row.ArxivID] = struct{}{}
			stats.Processed++

			// row.ArxivID is the full versioned id (bare id + asset
			// version), the same canonical shape the markdown handler
			// passes to Ensure.
			job := s.conv.Ensure(ctx, row.ArxivID)
			job = s.waitTerminal(ctx, row.ArxivID, job)
			if job == nil {
				reason = StopReasonShutdown
				return
			}

			switch {
			case job.State == JobStateDone:
				stats.Converted++
				s.noteConverted()
			case errors.Is(job.ErrKind, ErrDailyLimit):
				stats.Failed++
				reason = StopReasonQuota
				s.logger.Warn("mineru scheduler: daily quota exhausted, aborting run; retry at next midnight",
					"arxiv_id", row.ArxivID,
					"err", jobErrString(job),
				)
				return
			case isTokenError(job.Err):
				stats.Failed++
				reason = StopReasonTokenError
				s.logger.Warn("mineru scheduler: MinerU token error, aborting run; check MINERU_API_TOKENS",
					"arxiv_id", row.ArxivID,
					"err", jobErrString(job),
				)
				return
			default:
				// Per-paper fatal / retryable failure — the converter
				// already put the job on a 60s cooldown. Count it and
				// move on; the attempted set keeps this run from
				// resubmitting it.
				stats.Failed++
				s.logger.Warn("mineru scheduler: conversion failed, continuing run",
					"arxiv_id", row.ArxivID,
					"kind", kindLabel(job.ErrKind),
					"err", jobErrString(job),
				)
			}

			select {
			case <-ctx.Done():
				reason = StopReasonShutdown
				return
			case <-time.After(s.itemSpacing):
			}
		}
	}
}

// waitTerminal polls Lookup until the job reaches a terminal state
// (Done / Failed). Ensure returns immediately with Queued for fresh
// submissions — the actual conversion runs in the converter's
// background goroutine — so the scheduler must wait here to observe
// the failure kind before deciding whether to continue the run.
// Returns nil when ctx is cancelled mid-wait. A vanished job record
// (defensive; shouldn't happen) returns the last known snapshot.
func (s *Scheduler) waitTerminal(ctx context.Context, canonical string, job *Job) *Job {
	for job != nil && (job.State == JobStateQueued || job.State == JobStateRunning) {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(s.itemSpacing):
		}
		j, ok := s.conv.Lookup(canonical)
		if !ok {
			return job
		}
		job = j
	}
	return job
}

// isTokenError reports whether err is a token-level fatal: HTTP
// 401/403 or MinerU codes A0202 (bad token) / A0211 (expired token).
// These classify as ErrFatal per-paper but are really deployment-wide
// — every subsequent submission would fail identically.
func isTokenError(err error) bool {
	var me *Error
	if !errors.As(err, &me) {
		return false
	}
	if me.HTTPStatus == 401 || me.HTTPStatus == 403 {
		return true
	}
	return me.Code == "A0202" || me.Code == "A0211"
}

func jobErrString(j *Job) string {
	if j == nil || j.Err == nil {
		return ""
	}
	return j.Err.Error()
}
