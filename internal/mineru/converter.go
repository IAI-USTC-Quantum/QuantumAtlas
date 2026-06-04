package mineru

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"
)

// Converter is the server-side MinerU driver gated by the opt-in
// QATLAS_ASSET_DOWNLOADS_ENABLED master switch. It is constructed once
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
//     presigns the PDF, submits to MinerU, polls until done, downloads
//     the result zip, writes images + markdown via PutWithOptions with
//     IfNoneMatch:"*" (matches upload-mineru's first-writer-wins
//     idempotency), and best-effort calls catalog.UpsertMD.
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
// Safe for concurrent use.
type Converter struct {
	cfg     ConverterConfig
	store   objstore.Store
	catalog *papers.Store
	keyRing *KeyRing
	logger  *slog.Logger

	// now/dailyResetAt are indirection hooks so tests can fast-forward
	// "tomorrow 00:01" without sleeping.
	now          func() time.Time
	dailyResetAt func(time.Time) time.Time

	enabled     bool   // false when switch off OR no tokens OR public endpoint missing
	disabledMsg string // non-empty when !enabled, for handler error responses

	sem chan struct{}

	mu   sync.Mutex
	jobs map[string]*Job

	counters Counters
}

// ConverterConfig is the subset of internal/config.Config the converter
// needs. Passing a flat value avoids an import cycle from config →
// mineru (existing) → … and makes the converter trivially testable.
type ConverterConfig struct {
	AssetDownloadsEnabled bool
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

	// S3PublicEndpoint is the (publicly reachable) RustFS endpoint
	// presigned URLs are signed against. When empty AND switch+token
	// are set, NewConverter logs a WARN and the converter behaves as
	// disabled — MinerU can't reach our internal mesh endpoint and a
	// presigned URL pointing there would always 404 in MinerU.
	S3PublicEndpoint string
}

// JobState is the lifecycle state of one paper's conversion job.
type JobState string

const (
	JobStateQueued  JobState = "queued"
	JobStateRunning JobState = "running"
	JobStateDone    JobState = "done"
	JobStateFailed  JobState = "failed"
)

// Job is one in-flight or recently-completed conversion. Returned by
// Ensure and Lookup. Snapshot value — the converter never mutates a
// Job after handing it to a caller (a fresh Job is created on retry
// after the cooldown elapses).
type Job struct {
	Canonical     string
	State         JobState
	SubmittedAt   time.Time
	StartedAt     time.Time
	FinishedAt    time.Time
	Err           error
	ErrKind       error // ErrFatal / ErrRetryable / ErrDailyLimit, nil otherwise
	CooldownUntil time.Time
}

// Counters is the per-process tally surfaced for the optional /metrics
// or /api/health extras. Read via Snapshot.
type Counters struct {
	Submitted             atomic.Int64
	Succeeded             atomic.Int64
	FailedFatal           atomic.Int64
	FailedRetryable       atomic.Int64
	FailedDailyLimit      atomic.Int64
	CacheHits             atomic.Int64
	CacheMisses           atomic.Int64
	InflightJobs          atomic.Int64
}

// CountersSnapshot is a point-in-time view of the converter counters.
type CountersSnapshot struct {
	Submitted        int64 `json:"submitted"`
	Succeeded        int64 `json:"succeeded"`
	FailedFatal      int64 `json:"failed_fatal"`
	FailedRetryable  int64 `json:"failed_retryable"`
	FailedDailyLimit int64 `json:"failed_daily_limit"`
	CacheHits        int64 `json:"cache_hits"`
	CacheMisses      int64 `json:"cache_misses"`
	InflightJobs     int64 `json:"inflight_jobs"`
}

// Snapshot returns a consistent read of the counters.
func (c *Converter) Snapshot() CountersSnapshot {
	return CountersSnapshot{
		Submitted:        c.counters.Submitted.Load(),
		Succeeded:        c.counters.Succeeded.Load(),
		FailedFatal:      c.counters.FailedFatal.Load(),
		FailedRetryable:  c.counters.FailedRetryable.Load(),
		FailedDailyLimit: c.counters.FailedDailyLimit.Load(),
		CacheHits:        c.counters.CacheHits.Load(),
		CacheMisses:      c.counters.CacheMisses.Load(),
		InflightJobs:     c.counters.InflightJobs.Load(),
	}
}

// FailureCooldown is the back-off applied to fatal / retryable
// failures. 60s matches the issue #8 spec — short enough that an
// operator-fixed transient (e.g. MinerU 502 storm) doesn't keep
// callers blocked for long, long enough that a thundering herd from
// multiple clients can't DoS MinerU on every individual request.
const FailureCooldown = 60 * time.Second

// NewConverter builds a Converter from cfg. Always constructible — the
// returned value is non-nil even when AssetDownloadsEnabled=false, so
// callers don't have to nil-check. Use Enabled() to learn whether the
// converter will actually drive MinerU.
//
// When the switch is on but the deployment can't drive MinerU end-to-
// end (no tokens or S3 public endpoint missing), a single WARN is
// logged at construction time and Enabled() returns false. Callers
// (the markdown handler) translate that into 503 on cache miss.
func NewConverter(cfg ConverterConfig, store objstore.Store, catalog *papers.Store, logger *slog.Logger) *Converter {
	if logger == nil {
		logger = slog.Default()
	}
	maxConc := cfg.MinerUMaxConcurrentJobs
	if maxConc < 1 {
		maxConc = 1
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
	c.keyRing = NewKeyRing(cfg.MinerUAPITokens, cfg.MinerUAPIBaseURL, c.now)

	switch {
	case !cfg.AssetDownloadsEnabled:
		c.enabled = false
		c.disabledMsg = "asset downloads disabled (QATLAS_ASSET_DOWNLOADS_ENABLED=false)"
	case c.keyRing.Size() == 0:
		c.enabled = false
		c.disabledMsg = "MinerU not configured (MINERU_API_TOKENS unset); cache-only mode"
	case cfg.S3PublicEndpoint == "":
		c.enabled = false
		c.disabledMsg = "QATLAS_S3_PUBLIC_ENDPOINT not set; MinerU cannot reach internal RustFS — cache-only mode"
		logger.Warn("mineru converter degraded to cache-only: QATLAS_S3_PUBLIC_ENDPOINT not set; presigned URLs would point at an unreachable internal host. Set QATLAS_S3_PUBLIC_ENDPOINT to the RustFS host MinerU can reach to enable server-side conversion.")
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
// cache miss. False in any of: switch off, token unset, S3 public
// endpoint unset. The handler falls back to "cache only; 503 on miss"
// when false.
func (c *Converter) Enabled() bool { return c.enabled }

// DisabledReason returns a human-readable explanation of why the
// converter is disabled, suitable for the body of a 503 response.
// Empty when Enabled() == true.
func (c *Converter) DisabledReason() string { return c.disabledMsg }

// MaxConcurrentJobs returns the operator-configured semaphore size,
// for inclusion in the startup log line and any /metrics surface.
func (c *Converter) MaxConcurrentJobs() int { return cap(c.sem) }

// Timeout returns the per-job total timeout, for inclusion in the
// startup log line.
func (c *Converter) Timeout() time.Duration { return c.cfg.MinerUTimeout }

// Lookup returns a snapshot of the in-flight / recently-failed job for
// canonical, if any. Returns (nil, false) when there is no current
// record. Side-effect-free — safe to call from the status endpoint.
func (c *Converter) Lookup(canonical string) (*Job, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	j, ok := c.jobs[canonical]
	if !ok {
		return nil, false
	}
	// Return a defensive copy so the caller can't mutate our state.
	cp := *j
	return &cp, true
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
	mdKey := paperassets.AssetKey("markdown", canonical)
	if _, exists, err := c.store.Stat(ctx, mdKey); err == nil && exists {
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

// run is the per-job background driver. Acquires a semaphore slot,
// runs the full MinerU pipeline, and updates c.jobs[canonical] with
// the terminal state. Errors are captured into the Job; this method
// never panics into the calling goroutine.
func (c *Converter) run(canonical string) {
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

	c.transition(canonical, func(j *Job) {
		j.State = JobStateRunning
		j.StartedAt = c.now()
	})

	c.counters.Submitted.Add(1)
	err := c.runOnce(ctx, canonical)
	if err == nil {
		c.counters.Succeeded.Add(1)
		c.transition(canonical, func(j *Job) {
			j.State = JobStateDone
			j.FinishedAt = c.now()
		})
		return
	}

	kind, cooldown := c.classifyFailure(err)
	c.transition(canonical, func(j *Job) {
		j.State = JobStateFailed
		j.FinishedAt = c.now()
		j.Err = err
		j.ErrKind = kind
		j.CooldownUntil = cooldown
	})
	c.logger.Warn("mineru conversion failed",
		"arxiv_id", canonical,
		"kind", kindLabel(kind),
		"err", err.Error(),
		"cooldown_until", cooldown.Format(time.RFC3339),
	)
}

// runOnce is one full conversion attempt. Loops across the KeyRing on
// daily-limit errors so a single exhausted key does not stop the
// whole conversion — only when EVERY key reports daily-limit does the
// paper-level cooldown kick in. Returns a typed *Error on MinerU
// failures so c.classifyFailure can route into the correct counter +
// cooldown bucket.
func (c *Converter) runOnce(ctx context.Context, canonical string) error {
	pdfKey := paperassets.AssetKey("pdf", canonical)
	if _, exists, err := c.store.Stat(ctx, pdfKey); err != nil {
		return fmt.Errorf("stat pdf: %w", err)
	} else if !exists {
		return &Error{Msg: "no PDF in store for " + canonical, Kind: ErrFatal}
	}

	pdfURL, supported, err := c.store.PresignGet(ctx, pdfKey, 30*time.Minute)
	if err != nil {
		return fmt.Errorf("presign pdf: %w", err)
	}
	if !supported || pdfURL == "" {
		return &Error{Msg: "object store cannot presign PDF (LocalStore?); cannot drive MinerU", Kind: ErrFatal}
	}

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

		taskID, err := client.SubmitURLTask(ctx, pdfURL, SubmitOptions{
			ModelVersion:  c.cfg.MinerUModelVersion,
			Language:      c.cfg.MinerULanguage,
			EnableFormula: c.cfg.MinerUEnableFormula,
			EnableTable:   c.cfg.MinerUEnableTable,
			IsOCR:         c.cfg.MinerUIsOCR,
			DataID:        canonical,
		})
		if err != nil {
			if errors.Is(err, ErrDailyLimit) {
				c.keyRing.MarkDailyLimit(slot, c.dailyResetAt(c.now()))
				c.logger.Warn("mineru: key exhausted, rotating", "slot", slot, "remaining", c.keyRing.AvailableSlots())
				continue
			}
			return err
		}

		zipURL, err := c.pollUntilDone(ctx, client, taskID)
		if err != nil {
			if errors.Is(err, ErrDailyLimit) {
				c.keyRing.MarkDailyLimit(slot, c.dailyResetAt(c.now()))
				c.logger.Warn("mineru: key exhausted during poll, rotating", "slot", slot, "remaining", c.keyRing.AvailableSlots())
				continue
			}
			return err
		}

		result, err := client.FetchResult(ctx, zipURL)
		if err != nil {
			return err
		}

		return c.writeResult(ctx, canonical, result)
	}
}

// pollUntilDone polls MinerU at cfg.MinerUPollInterval until the task
// transitions to done or failed, or the per-job context expires.
// Uses the supplied client so the caller controls which token / which
// key-ring slot the polling traffic charges against.
func (c *Converter) pollUntilDone(ctx context.Context, client *Client, taskID string) (string, error) {
	tick := time.NewTicker(c.cfg.MinerUPollInterval)
	defer tick.Stop()
	for {
		state, err := client.GetTask(ctx, taskID)
		if err != nil {
			return "", err
		}
		switch state.State {
		case "done":
			if state.FullZipURL == "" {
				return "", &Error{Msg: "task done but full_zip_url empty", Kind: ErrRetryable}
			}
			return state.FullZipURL, nil
		case "failed":
			return "", classifyAPIError("", state.ErrMsg, 0)
		}
		select {
		case <-ctx.Done():
			return "", fmt.Errorf("poll timeout: %w", ctx.Err())
		case <-tick.C:
		}
	}
}

// writeResult writes images first, then markdown, mirroring the
// upload-mineru contributor handler's ordering — readers see markdown
// only after every referenced image is durable. Uses IfNoneMatch:"*"
// so a concurrent edge that wrote markdown first wins (we observe 412
// and treat that as success).
func (c *Converter) writeResult(ctx context.Context, canonical string, result Result) error {
	imagesBase := paperassets.AssetKey("images", canonical)
	for rel, data := range result.Images {
		name := strings.TrimPrefix(rel, "images/")
		if name == "" || strings.Contains(name, "..") {
			continue
		}
		imgKey := imagesBase + "/" + name
		ct := mime.TypeByExtension(path.Ext(name))
		if ct == "" {
			ct = "application/octet-stream"
		}
		_, err := c.store.PutWithOptions(ctx, imgKey, bytes.NewReader(data), int64(len(data)), objstore.PutOptions{
			ContentType: ct,
			IfNoneMatch: "*",
		})
		if err != nil && !errors.Is(err, objstore.ErrPreconditionFailed) {
			return fmt.Errorf("put image %s: %w", name, err)
		}
	}

	mdKey := paperassets.AssetKey("markdown", canonical)
	mdSize := int64(len(result.Markdown))
	_, err := c.store.PutWithOptions(ctx, mdKey, bytes.NewReader(result.Markdown), mdSize, objstore.PutOptions{
		ContentType: "text/markdown; charset=utf-8",
		IfNoneMatch: "*",
	})
	if err != nil && !errors.Is(err, objstore.ErrPreconditionFailed) {
		return fmt.Errorf("put markdown: %w", err)
	}

	// Catalog write-through is best-effort: a Neo4j outage shouldn't
	// turn a successful S3 write into a 502 to the caller. Matches the
	// upload-mineru handler's tolerance.
	if c.catalog != nil {
		sum := sha256.Sum256(result.Markdown)
		mdSha := hex.EncodeToString(sum[:])
		if uErr := c.catalog.UpsertMD(ctx, canonical, mdSha, mdSize, ""); uErr != nil &&
			!errors.Is(uErr, papers.ErrCatalogUnavailable) {
			c.logger.Warn("papers: UpsertMD write-through failed after conversion",
				"arxiv_id", canonical, "error", uErr)
		}
	}

	c.logger.Info("mineru conversion succeeded",
		"arxiv_id", canonical,
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

// drainReader is a small helper for tests that want to discard a
// response body cleanly.
func drainReader(r io.Reader) { _, _ = io.Copy(io.Discard, r) }
