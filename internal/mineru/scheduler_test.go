package mineru

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// fakeConverter implements converterDriver. Ensure returns whatever the
// jobs map holds for the canonical (default: a Done job), so tests
// drive terminal states synchronously without a MinerU round-trip.
type fakeConverter struct {
	enabled bool

	mu    sync.Mutex
	calls []string
	jobs  map[string]*Job
}

func (f *fakeConverter) Enabled() bool { return f.enabled }

func (f *fakeConverter) Ensure(_ context.Context, canonical string) *Job {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, canonical)
	if j, ok := f.jobs[canonical]; ok {
		return j
	}
	return &Job{Canonical: canonical, State: JobStateDone, FinishedAt: time.Now()}
}

func (f *fakeConverter) Lookup(canonical string) (*Job, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.jobs[canonical]
	return j, ok
}

func (f *fakeConverter) ensureCalls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

// fakeQueue implements queueReader. Each NeedsMineru call pops the
// next scripted batch; once the script is exhausted it returns empty
// (simulating converted rows dropping out of the queue via UpsertMD).
type fakeQueue struct {
	mu      sync.Mutex
	batches [][]registry.NeedsMineruRow
	err     error
	calls   int
}

func (q *fakeQueue) NeedsMineru(_ context.Context, limit int) ([]registry.NeedsMineruRow, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.calls++
	if q.err != nil {
		return nil, q.err
	}
	if len(q.batches) == 0 {
		return nil, nil
	}
	rows := q.batches[0]
	q.batches = q.batches[1:]
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows, nil
}

func (q *fakeQueue) fetchCalls() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.calls
}

func queueRows(ids ...string) []registry.NeedsMineruRow {
	rows := make([]registry.NeedsMineruRow, 0, len(ids))
	for i, id := range ids {
		rows = append(rows, registry.NeedsMineruRow{
			PaperID: fmt.Sprintf("paper-%d", i),
			ArxivID: id,
			Version: 1,
		})
	}
	return rows
}

func newTestScheduler(conv converterDriver, q queueReader, opts ...SchedulerOption) *Scheduler {
	base := []SchedulerOption{WithItemSpacing(0)}
	return NewScheduler(conv, q, slog.New(slog.NewTextHandler(io.Discard, nil)), append(base, opts...)...)
}

// fakeClock is a manually-advanced clock for day-rollover tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// blockingConverter blocks inside Ensure until block is closed (or the
// ctx is cancelled), so tests can hold a run "in progress" while they
// probe the singleflight guard.
type blockingConverter struct {
	mu      sync.Mutex
	calls   []string
	entered chan struct{} // receives once per Ensure call
	block   chan struct{}
}

func newBlockingConverter() *blockingConverter {
	return &blockingConverter{
		entered: make(chan struct{}, 16),
		block:   make(chan struct{}),
	}
}

func (b *blockingConverter) Enabled() bool { return true }

func (b *blockingConverter) Ensure(ctx context.Context, canonical string) *Job {
	b.mu.Lock()
	b.calls = append(b.calls, canonical)
	b.mu.Unlock()
	b.entered <- struct{}{}
	select {
	case <-b.block:
	case <-ctx.Done():
	}
	return &Job{Canonical: canonical, State: JobStateDone, FinishedAt: time.Now()}
}

func (b *blockingConverter) Lookup(canonical string) (*Job, bool) {
	return &Job{Canonical: canonical, State: JobStateDone, FinishedAt: time.Now()}, true
}

func (b *blockingConverter) ensureCallCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.calls)
}

// waitFor polls cond until it holds or the timeout elapses.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestSchedulerTickEmptyQueue: midnight tick with an empty queue runs,
// converts nothing, and schedules the next run.
func TestSchedulerTickEmptyQueue(t *testing.T) {
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{}}
	q := &fakeQueue{}
	s := newTestScheduler(conv, q, WithTickInterval(5*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s.Start(ctx)
	defer s.Stop()

	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := s.Snapshot()
		if snap.LastRunFinished != nil && snap.NextRunAt != nil {
			if snap.LastStopReason != StopReasonQueueEmpty {
				t.Fatalf("expected stop reason %q, got %q", StopReasonQueueEmpty, snap.LastStopReason)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for first run; snapshot=%+v", snap)
		}
		time.Sleep(time.Millisecond)
	}

	if got := conv.ensureCalls(); len(got) != 0 {
		t.Fatalf("expected no Ensure calls on empty queue, got %v", got)
	}
}

// TestSchedulerRunConvertsQueue: a non-empty queue is walked until the
// fetch comes back empty; every row gets exactly one Ensure.
func TestSchedulerRunConvertsQueue(t *testing.T) {
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{}}
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1", "2301.00002v2", "2301.00003v1"),
		// second fetch returns empty → queue drained
	}}
	s := newTestScheduler(conv, q)

	s.runBatch(context.Background())

	want := []string{"2301.00001v1", "2301.00002v2", "2301.00003v1"}
	got := conv.ensureCalls()
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Ensure calls = %v, want %v", got, want)
	}
	snap := s.Snapshot()
	if snap.LastStopReason != StopReasonQueueEmpty {
		t.Fatalf("stop reason = %q, want %q", snap.LastStopReason, StopReasonQueueEmpty)
	}
	if snap.LastRun.Processed != 3 || snap.LastRun.Converted != 3 || snap.LastRun.Failed != 0 {
		t.Fatalf("stats = %+v, want processed=3 converted=3 failed=0", snap.LastRun)
	}
	if snap.Running {
		t.Fatal("snapshot still reports running after runBatch returned")
	}
}

// TestSchedulerRunAbortsOnQuota: a daily-limit failure mid-run aborts
// immediately — later rows are never Ensured.
func TestSchedulerRunAbortsOnQuota(t *testing.T) {
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{
		"2301.00002v1": {
			Canonical: "2301.00002v1",
			State:     JobStateFailed,
			Err:       &Error{Msg: "server quota exhausted", Kind: ErrDailyLimit},
			ErrKind:   ErrDailyLimit,
		},
	}}
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1", "2301.00002v1", "2301.00003v1"),
	}}
	s := newTestScheduler(conv, q)

	s.runBatch(context.Background())

	got := conv.ensureCalls()
	want := []string{"2301.00001v1", "2301.00002v1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Ensure calls = %v, want %v (row 3 must be untouched)", got, want)
	}
	snap := s.Snapshot()
	if snap.LastStopReason != StopReasonQuota {
		t.Fatalf("stop reason = %q, want %q", snap.LastStopReason, StopReasonQuota)
	}
	if snap.LastRun.Converted != 1 || snap.LastRun.Failed != 1 {
		t.Fatalf("stats = %+v, want converted=1 failed=1", snap.LastRun)
	}
}

// TestSchedulerRunAbortsOnTokenError: a token-level fatal (HTTP 401)
// aborts the run with StopReasonTokenError, distinct from per-paper
// fatals which let the run continue.
func TestSchedulerRunAbortsOnTokenError(t *testing.T) {
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{
		"2301.00001v1": {
			Canonical: "2301.00001v1",
			State:     JobStateFailed,
			Err:       &Error{Msg: "authentication failed", HTTPStatus: 401, Kind: ErrFatal},
			ErrKind:   ErrFatal,
		},
	}}
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1", "2301.00002v1"),
	}}
	s := newTestScheduler(conv, q)

	s.runBatch(context.Background())

	if got := conv.ensureCalls(); len(got) != 1 {
		t.Fatalf("Ensure calls = %v, want exactly 1 (token error aborts)", got)
	}
	if snap := s.Snapshot(); snap.LastStopReason != StopReasonTokenError {
		t.Fatalf("stop reason = %q, want %q", snap.LastStopReason, StopReasonTokenError)
	}
}

// TestSchedulerRunContinuesPastPaperFatal: a per-paper fatal (bad PDF)
// does NOT abort the run; the remaining rows are still processed and
// the failed row is not resubmitted within the same run.
func TestSchedulerRunContinuesPastPaperFatal(t *testing.T) {
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{
		"2301.00001v1": {
			Canonical: "2301.00001v1",
			State:     JobStateFailed,
			Err:       &Error{Msg: "bad PDF", Code: "-60003", Kind: ErrFatal},
			ErrKind:   ErrFatal,
		},
	}}
	// The failed row stays in the queue (no UpsertMD), so the second
	// fetch returns the same first row plus nothing new → the run must
	// terminate via the attempted-set guard, not by resubmitting.
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1", "2301.00002v1"),
		queueRows("2301.00001v1"),
	}}
	s := newTestScheduler(conv, q)

	s.runBatch(context.Background())

	got := conv.ensureCalls()
	want := []string{"2301.00001v1", "2301.00002v1"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("Ensure calls = %v, want %v (failed row not resubmitted)", got, want)
	}
	snap := s.Snapshot()
	if snap.LastStopReason != StopReasonQueueEmpty {
		t.Fatalf("stop reason = %q, want %q", snap.LastStopReason, StopReasonQueueEmpty)
	}
	if snap.LastRun.Converted != 1 || snap.LastRun.Failed != 1 {
		t.Fatalf("stats = %+v, want converted=1 failed=1", snap.LastRun)
	}
}

// TestSchedulerRunDisabledConverter: with the converter disabled the
// run is a no-op — the queue is not even fetched.
func TestSchedulerRunDisabledConverter(t *testing.T) {
	conv := &fakeConverter{enabled: false, jobs: map[string]*Job{}}
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1"),
	}}
	s := newTestScheduler(conv, q)

	s.runBatch(context.Background())

	if got := conv.ensureCalls(); len(got) != 0 {
		t.Fatalf("expected no Ensure calls when disabled, got %v", got)
	}
	if got := q.fetchCalls(); got != 0 {
		t.Fatalf("expected no queue fetch when disabled, got %d", got)
	}
	if snap := s.Snapshot(); snap.LastStopReason != StopReasonDisabled {
		t.Fatalf("stop reason = %q, want %q", snap.LastStopReason, StopReasonDisabled)
	}
}

// TestSchedulerRunQueueFetchError: a registry read failure ends the
// run with StopReasonFetchError.
func TestSchedulerRunQueueFetchError(t *testing.T) {
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{}}
	q := &fakeQueue{err: errors.New("registry: connection refused")}
	s := newTestScheduler(conv, q)

	s.runBatch(context.Background())

	if got := conv.ensureCalls(); len(got) != 0 {
		t.Fatalf("expected no Ensure calls on fetch error, got %v", got)
	}
	if snap := s.Snapshot(); snap.LastStopReason != StopReasonFetchError {
		t.Fatalf("stop reason = %q, want %q", snap.LastStopReason, StopReasonFetchError)
	}
}

// TestSchedulerStopIsClean: Start + Stop returns promptly and a second
// Start is a no-op-safe lifecycle.
func TestSchedulerStopIsClean(t *testing.T) {
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{}}
	q := &fakeQueue{}
	s := newTestScheduler(conv, q, WithTickInterval(time.Hour))

	s.Start(context.Background())
	s.Start(context.Background()) // idempotent

	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop did not return within 2s")
	}
}

// TestSchedulerDailyCapStopsRun: the run stops at exactly dailyCap
// successful conversions with StopReasonDailyCap; later rows are not
// submitted.
func TestSchedulerDailyCapStopsRun(t *testing.T) {
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{}}
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1", "2301.00002v1", "2301.00003v1"),
	}}
	s := newTestScheduler(conv, q, WithDailyCap(2))

	s.runBatch(context.Background())

	if got := conv.ensureCalls(); len(got) != 2 {
		t.Fatalf("Ensure calls = %v, want exactly 2 (daily cap)", got)
	}
	snap := s.Snapshot()
	if snap.LastStopReason != StopReasonDailyCap {
		t.Fatalf("stop reason = %q, want %q", snap.LastStopReason, StopReasonDailyCap)
	}
	if snap.LastRun.Converted != 2 {
		t.Fatalf("converted = %d, want 2", snap.LastRun.Converted)
	}
	if snap.DailyCap != 2 {
		t.Fatalf("snapshot daily_cap = %d, want 2", snap.DailyCap)
	}
	if snap.ConvertedToday != 2 {
		t.Fatalf("snapshot converted_today = %d, want 2", snap.ConvertedToday)
	}
	if snap.CapDay != todayKey(time.Now()) {
		t.Fatalf("snapshot cap_day = %q, want %q", snap.CapDay, todayKey(time.Now()))
	}
}

// TestSchedulerDailyCapResetsOnDayRollover: the shared counter rolls
// over at local midnight — a capped day stops further runs, the next
// calendar day converts again.
func TestSchedulerDailyCapResetsOnDayRollover(t *testing.T) {
	day1 := time.Date(2026, 8, 24, 10, 0, 0, 0, time.Local)
	clock := &fakeClock{t: day1}
	conv := &fakeConverter{enabled: true, jobs: map[string]*Job{}}
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1"), // run 1: converted
		queueRows("2301.00002v1"), // run 1: cap check stops here
		queueRows("2301.00003v1"), // run 2 (same day): cap check stops immediately
		queueRows("2301.00004v1"), // run 3 (next day): converted
	}}
	s := newTestScheduler(conv, q, WithDailyCap(1), WithClock(clock.now))

	// Run 1: converts row1, hits the cap at row2.
	s.runBatch(context.Background())
	if got := conv.ensureCalls(); len(got) != 1 {
		t.Fatalf("run 1: Ensure calls = %v, want 1", got)
	}
	if snap := s.Snapshot(); snap.LastStopReason != StopReasonDailyCap {
		t.Fatalf("run 1: stop reason = %q, want %q", snap.LastStopReason, StopReasonDailyCap)
	}

	// Run 2, same day: cap still in force — nothing submitted.
	s.runBatch(context.Background())
	if got := conv.ensureCalls(); len(got) != 1 {
		t.Fatalf("run 2: Ensure calls = %v, want still 1 (cap in force)", got)
	}
	snap := s.Snapshot()
	if snap.LastStopReason != StopReasonDailyCap {
		t.Fatalf("run 2: stop reason = %q, want %q", snap.LastStopReason, StopReasonDailyCap)
	}
	if snap.ConvertedToday != 1 || snap.CapDay != "2026-08-24" {
		t.Fatalf("run 2: converted_today=%d cap_day=%q, want 1 / 2026-08-24",
			snap.ConvertedToday, snap.CapDay)
	}

	// Roll the clock over local midnight → counter resets.
	clock.set(day1.Add(25 * time.Hour))
	s.runBatch(context.Background())
	if got := conv.ensureCalls(); len(got) != 2 {
		t.Fatalf("run 3: Ensure calls = %v, want 2 (counter reset after midnight)", got)
	}
	snap = s.Snapshot()
	if snap.ConvertedToday != 1 || snap.CapDay != "2026-08-25" {
		t.Fatalf("run 3: converted_today=%d cap_day=%q, want 1 / 2026-08-25",
			snap.ConvertedToday, snap.CapDay)
	}
	if snap.LastStopReason != StopReasonQueueEmpty {
		t.Fatalf("run 3: stop reason = %q, want %q", snap.LastStopReason, StopReasonQueueEmpty)
	}
}

// TestSchedulerTickSkippedWhileRunning: with a run in progress, later
// ticks do not start a second run — Ensure stays at exactly one call
// and the in-flight run keeps going.
func TestSchedulerTickSkippedWhileRunning(t *testing.T) {
	conv := newBlockingConverter()
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1"),
	}}
	s := newTestScheduler(conv, q, WithTickInterval(5*time.Millisecond))

	s.Start(context.Background())
	defer s.Stop()

	select {
	case <-conv.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("first run did not reach Ensure")
	}

	// Several tick intervals pass while the run is blocked in Ensure.
	time.Sleep(30 * time.Millisecond)
	if got := conv.ensureCallCount(); got != 1 {
		t.Fatalf("Ensure calls = %d, want 1 (ticks must skip while a run is active)", got)
	}
	if snap := s.Snapshot(); !snap.Running {
		t.Fatal("snapshot running = false, want true (run still in progress)")
	}

	close(conv.block)
}

// TestSchedulerRunNowCoalesces: a manual trigger starts a run; every
// concurrent trigger (sequential or parallel) coalesces onto it with
// started=false, reason=already_running. Once the run finishes, the
// slot frees and the next RunNow starts a fresh run.
func TestSchedulerRunNowCoalesces(t *testing.T) {
	conv := newBlockingConverter()
	q := &fakeQueue{batches: [][]registry.NeedsMineruRow{
		queueRows("2301.00001v1"),
	}}
	s := newTestScheduler(conv, q)
	defer s.Stop()

	started, reason := s.RunNow(context.Background())
	if !started || reason != "started" {
		t.Fatalf("first RunNow = (%v, %q), want (true, started)", started, reason)
	}
	select {
	case <-conv.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("manual run did not reach Ensure")
	}

	if started, reason := s.RunNow(context.Background()); started || reason != "already_running" {
		t.Fatalf("second RunNow = (%v, %q), want (false, already_running)", started, reason)
	}

	var wg sync.WaitGroup
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if started, _ := s.RunNow(context.Background()); started {
				t.Error("concurrent RunNow started a second run")
			}
		}()
	}
	wg.Wait()

	close(conv.block)
	waitFor(t, 2*time.Second, func() bool { return !s.Snapshot().Running }, "first run to finish")

	// Slot is free again after the run completes; the queue is now
	// drained, so this run finishes quickly with queue_empty.
	started, reason = s.RunNow(context.Background())
	if !started || reason != "started" {
		t.Fatalf("RunNow after completion = (%v, %q), want (true, started)", started, reason)
	}
	waitFor(t, 2*time.Second, func() bool { return !s.Snapshot().Running }, "second run to finish")
	if snap := s.Snapshot(); snap.LastStopReason != StopReasonQueueEmpty {
		t.Fatalf("second run stop reason = %q, want %q", snap.LastStopReason, StopReasonQueueEmpty)
	}
}

// TestSchedulerRunNowDisabledConverter: RunNow on a disabled converter
// refuses to start with reason converter_disabled.
func TestSchedulerRunNowDisabledConverter(t *testing.T) {
	conv := &fakeConverter{enabled: false, jobs: map[string]*Job{}}
	s := newTestScheduler(conv, &fakeQueue{})

	started, reason := s.RunNow(context.Background())
	if started || reason != string(StopReasonDisabled) {
		t.Fatalf("RunNow = (%v, %q), want (false, %q)", started, reason, StopReasonDisabled)
	}
	if got := conv.ensureCalls(); len(got) != 0 {
		t.Fatalf("expected no Ensure calls, got %v", got)
	}
}
