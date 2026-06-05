package mineru

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// fakeStore is a minimal in-memory objstore.Store sufficient for
// converter tests. PresignGet returns a placeholder URL so the
// converter happily submits to MinerU; the test harness inspects
// what arrived at the stub MinerU server, not the actual fetch.
type fakeStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	presign string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		objects: map[string][]byte{},
		presign: "http://fake.invalid/pdf",
	}
}

func (s *fakeStore) put(key string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key] = data
}

func (s *fakeStore) get(key string) ([]byte, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.objects[key]
	return d, ok
}

func (s *fakeStore) Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error) {
	return s.PutWithOptions(ctx, key, r, size, objstore.PutOptions{ContentType: contentType})
}
func (s *fakeStore) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, contentType string, metadata map[string]string) (int64, error) {
	return s.PutWithOptions(ctx, key, r, size, objstore.PutOptions{ContentType: contentType, Metadata: metadata})
}
func (s *fakeStore) PutWithOptions(_ context.Context, key string, r io.Reader, _ int64, po objstore.PutOptions) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if po.IfNoneMatch == "*" {
		if _, ok := s.objects[key]; ok {
			return 0, objstore.ErrPreconditionFailed
		}
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return 0, err
	}
	s.objects[key] = b
	return int64(len(b)), nil
}
func (s *fakeStore) Get(_ context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[key]
	if !ok {
		return nil, objstore.ObjectInfo{}, objstore.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(string(b))), objstore.ObjectInfo{Key: key, Size: int64(len(b))}, nil
}
func (s *fakeStore) Stat(_ context.Context, key string) (objstore.ObjectInfo, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[key]
	if !ok {
		return objstore.ObjectInfo{}, false, nil
	}
	return objstore.ObjectInfo{Key: key, Size: int64(len(b))}, true, nil
}
func (s *fakeStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}
func (s *fakeStore) ListPrefix(_ context.Context, prefix string, _ int) ([]objstore.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []objstore.ObjectInfo
	for k, v := range s.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, objstore.ObjectInfo{Key: k, Size: int64(len(v))})
		}
	}
	return out, nil
}
func (s *fakeStore) PresignGet(_ context.Context, key string, _ time.Duration) (string, bool, error) {
	if s.presign == "" {
		return "", false, nil
	}
	return s.presign + "?key=" + key, true, nil
}

// minerUStub is a configurable MinerU+result-zip backend used by the
// converter tests. It accepts a SubmitURLTask, returns a fixed task_id,
// reports state="done" with a result URL on the second GetTask call,
// then serves the result zip from /result.
type minerUStub struct {
	t           *testing.T
	server      *httptest.Server
	submissions atomic64
	pollCalls   atomic64

	// Configurable behaviour
	taskFailWithMsg string // when non-empty, GetTask returns state=failed with this msg
	submitFailCode  string // when non-empty, SubmitURLTask returns this envelope code
	submitFailMsg   string
	zipBody         []byte
}

type atomic64 struct {
	mu sync.Mutex
	v  int64
}

func (a *atomic64) inc() { a.mu.Lock(); a.v++; a.mu.Unlock() }
func (a *atomic64) load() int64 {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.v
}

func newMinerUStub(t *testing.T) *minerUStub {
	t.Helper()
	stub := &minerUStub{t: t}
	stub.zipBody = buildResultZip(t, "out", "# Hello\n![](images/a.png)\n", map[string]string{
		"images/a.png": "PNGDATA",
	})
	stub.server = httptest.NewServer(http.HandlerFunc(stub.handle))
	return stub
}

func (s *minerUStub) close() { s.server.Close() }
func (s *minerUStub) url() string { return s.server.URL }

func (s *minerUStub) handle(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/v4/extract/task" && r.Method == http.MethodPost:
		s.submissions.inc()
		if s.submitFailCode != "" {
			writeEnvelope(w, s.submitFailCode, s.submitFailMsg, nil)
			return
		}
		writeEnvelope(w, "0", "", map[string]any{"task_id": "tsk-1"})
	case strings.HasPrefix(r.URL.Path, "/api/v4/extract/task/") && r.Method == http.MethodGet:
		s.pollCalls.inc()
		if s.taskFailWithMsg != "" {
			writeEnvelope(w, "0", "", map[string]any{
				"state": "failed", "err_msg": s.taskFailWithMsg,
			})
			return
		}
		writeEnvelope(w, "0", "", map[string]any{
			"state": "done", "full_zip_url": s.server.URL + "/result",
		})
	case r.URL.Path == "/result":
		_, _ = w.Write(s.zipBody)
	default:
		http.NotFound(w, r)
	}
}

func writeEnvelope(w http.ResponseWriter, code, msg string, data map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	body := map[string]any{"code": code, "msg": msg}
	if data != nil {
		body["data"] = data
	}
	_ = json.NewEncoder(w).Encode(body)
}

func makeConverter(t *testing.T, store objstore.Store, stubURL string, tokens ...string) *Converter {
	t.Helper()
	if len(tokens) == 0 {
		tokens = []string{"test-token"}
	}
	cfg := ConverterConfig{
		PaperAccessEnabled:   true,
		MinerUAPITokens:         tokens,
		MinerUAPIBaseURL:        stubURL,
		MinerUModelVersion:      "vlm",
		MinerULanguage:          "en",
		MinerUIsOCR:             false,
		MinerUEnableFormula:     true,
		MinerUEnableTable:       true,
		MinerUPollInterval:      5 * time.Millisecond,
		MinerUTimeout:           5 * time.Second,
		MinerUMaxConcurrentJobs: 2,
		S3PublicEndpoint:        "http://public.invalid",
	}
	return NewConverter(cfg, store, nil, nil)
}

func TestConverter_DisabledWhenSwitchOff(t *testing.T) {
	store := newFakeStore()
	c := NewConverter(ConverterConfig{PaperAccessEnabled: false}, store, nil, nil)
	if c.Enabled() {
		t.Fatal("converter should be disabled when switch is off")
	}
	if c.DisabledReason() == "" {
		t.Fatal("DisabledReason should be populated when disabled")
	}
}

func TestConverter_DisabledWhenTokenMissing(t *testing.T) {
	store := newFakeStore()
	c := NewConverter(ConverterConfig{
		PaperAccessEnabled: true,
		S3PublicEndpoint:      "http://public.invalid",
	}, store, nil, nil)
	if c.Enabled() {
		t.Fatal("converter should be disabled when token missing")
	}
	if !strings.Contains(c.DisabledReason(), "MINERU_API_TOKENS") {
		t.Fatalf("DisabledReason = %q, want hint about MINERU_API_TOKENS", c.DisabledReason())
	}
}

func TestConverter_DisabledWhenPublicEndpointMissing(t *testing.T) {
	store := newFakeStore()
	c := NewConverter(ConverterConfig{
		PaperAccessEnabled: true,
		MinerUAPITokens:       []string{"tok"},
	}, store, nil, nil)
	if c.Enabled() {
		t.Fatal("converter should be disabled when public endpoint missing")
	}
}

func TestConverter_CacheHitShortCircuits(t *testing.T) {
	store := newFakeStore()
	// pre-populate the markdown cache
	store.put("markdown/2401/2401.12345v1.md", []byte("# cached"))

	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())
	if !c.Enabled() {
		t.Fatal("converter should be enabled")
	}

	job := c.Ensure(context.Background(), "2401.12345v1")
	if job.State != JobStateDone {
		t.Fatalf("State = %v, want JobStateDone", job.State)
	}
	if c.Snapshot().CacheHits != 1 {
		t.Errorf("CacheHits = %d, want 1", c.Snapshot().CacheHits)
	}
	if c.Snapshot().Submitted != 0 {
		t.Errorf("Submitted = %d, want 0 (cache hit should not submit)", c.Snapshot().Submitted)
	}
	if stub.submissions.load() != 0 {
		t.Errorf("stub.submissions = %d, want 0", stub.submissions.load())
	}
}

func TestConverter_EnsureSubmitsAndWrites(t *testing.T) {
	store := newFakeStore()
	store.put("pdf/2401/2401.12345v1.pdf", []byte("%PDF-fake"))

	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())

	job := c.Ensure(context.Background(), "2401.12345v1")
	if job.State != JobStateQueued {
		t.Fatalf("initial Ensure state = %v, want queued", job.State)
	}

	// Wait for the background goroutine to complete the job.
	if !waitForJobState(c, "2401.12345v1", JobStateDone, 2*time.Second) {
		final, _ := c.Lookup("2401.12345v1")
		t.Fatalf("job did not reach Done; final = %+v", final)
	}

	// Markdown should now be in the store.
	if _, ok := store.get("markdown/2401/2401.12345v1.md"); !ok {
		t.Errorf("markdown not written to store")
	}
	// Images should be a single zip, not scattered files.
	if _, ok := store.get("images/2401/2401.12345v1.zip"); !ok {
		t.Errorf("images zip not written to store")
	}
	snap := c.Snapshot()
	if snap.Submitted != 1 || snap.Succeeded != 1 {
		t.Errorf("counters = %+v, want submitted=succeeded=1", snap)
	}
}

func TestConverter_DedupesConcurrentEnsure(t *testing.T) {
	store := newFakeStore()
	store.put("pdf/2401/2401.12345v1.pdf", []byte("%PDF-fake"))

	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url())

	const N = 5
	var wg sync.WaitGroup
	for i := 0; i < N; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = c.Ensure(context.Background(), "2401.12345v1")
		}()
	}
	wg.Wait()

	if !waitForJobState(c, "2401.12345v1", JobStateDone, 2*time.Second) {
		t.Fatal("job did not complete")
	}

	if got := stub.submissions.load(); got != 1 {
		t.Errorf("MinerU submissions = %d, want 1 (deduped)", got)
	}
	if got := c.Snapshot().Submitted; got != 1 {
		t.Errorf("Submitted counter = %d, want 1", got)
	}
}

func TestConverter_CooldownAfterFailure(t *testing.T) {
	store := newFakeStore()
	store.put("pdf/2401/2401.12345v1.pdf", []byte("%PDF-fake"))

	stub := newMinerUStub(t)
	defer stub.close()
	stub.taskFailWithMsg = "fake transient failure"

	c := makeConverter(t, store, stub.url())

	c.Ensure(context.Background(), "2401.12345v1")
	if !waitForJobState(c, "2401.12345v1", JobStateFailed, 2*time.Second) {
		t.Fatal("job did not fail")
	}

	// Second Ensure within the cooldown should NOT trigger a new submission.
	c.Ensure(context.Background(), "2401.12345v1")
	time.Sleep(50 * time.Millisecond)
	if got := stub.submissions.load(); got != 1 {
		t.Errorf("submissions = %d, want 1 (second call in cooldown)", got)
	}
	if c.Snapshot().FailedRetryable < 1 {
		t.Errorf("FailedRetryable = %d, want ≥1", c.Snapshot().FailedRetryable)
	}
}

func TestConverter_DailyLimitCooldownEndsAtMidnight(t *testing.T) {
	store := newFakeStore()
	store.put("pdf/2401/2401.12345v1.pdf", []byte("%PDF-fake"))

	stub := newMinerUStub(t)
	defer stub.close()
	stub.taskFailWithMsg = "每日解析任务数量已达上限" // matches dailyLimitKeywords

	c := makeConverter(t, store, stub.url())

	c.Ensure(context.Background(), "2401.12345v1")
	if !waitForJobState(c, "2401.12345v1", JobStateFailed, 2*time.Second) {
		t.Fatal("job did not fail")
	}

	job, _ := c.Lookup("2401.12345v1")
	if job == nil {
		t.Fatal("no job recorded")
	}
	if !errors.Is(job.ErrKind, ErrDailyLimit) {
		t.Errorf("ErrKind = %v, want ErrDailyLimit", job.ErrKind)
	}
	// Cooldown should be at "next 00:01 local" — strictly more than
	// the FailureCooldown for a normal retryable failure.
	if delta := job.CooldownUntil.Sub(time.Now()); delta < 2*FailureCooldown {
		t.Errorf("daily-limit cooldown should be far in the future, got %v", delta)
	}
}

func TestNextLocalDailyReset(t *testing.T) {
	loc := time.FixedZone("test", 0)
	now := time.Date(2026, 5, 30, 12, 0, 0, 0, loc)
	reset := nextLocalDailyReset(now)
	want := time.Date(2026, 5, 31, 0, 1, 0, 0, loc)
	if !reset.Equal(want) {
		t.Errorf("nextLocalDailyReset(noon) = %v, want %v", reset, want)
	}

	// At 00:00 exactly, reset should still advance to 00:01 the *next* day.
	now2 := time.Date(2026, 5, 30, 0, 0, 30, 0, loc)
	reset2 := nextLocalDailyReset(now2)
	want2 := time.Date(2026, 5, 30, 0, 1, 0, 0, loc)
	if !reset2.Equal(want2) {
		t.Errorf("nextLocalDailyReset(00:00:30) = %v, want %v", reset2, want2)
	}
}

func waitForJobState(c *Converter, canonical string, want JobState, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if j, ok := c.Lookup(canonical); ok && j.State == want {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// ============================================================================
// Multi-key fail-over tests (issue #8 follow-up)
// ============================================================================

// minerUStub but tracks which token authorized each request so we can
// assert key rotation actually used a different token.
type tokenAwareStub struct {
	t           *testing.T
	server      *httptest.Server
	submissions atomic64

	// keyState maps token → daily-limit behavior:
	//   "quota" → SubmitURLTask returns daily-limit (-60018)
	//   ""      → normal happy path
	keyStateMu sync.Mutex
	keyState   map[string]string

	// tokensSeen records every Authorization Bearer ever seen.
	tokensSeenMu sync.Mutex
	tokensSeen   []string

	zipBody []byte
}

func newTokenAwareStub(t *testing.T) *tokenAwareStub {
	t.Helper()
	stub := &tokenAwareStub{t: t, keyState: map[string]string{}}
	stub.zipBody = buildResultZip(t, "out", "# Hello\n", nil)
	stub.server = httptest.NewServer(http.HandlerFunc(stub.handle))
	return stub
}

func (s *tokenAwareStub) close() { s.server.Close() }
func (s *tokenAwareStub) url() string { return s.server.URL }

func (s *tokenAwareStub) markQuotaExhausted(token string) {
	s.keyStateMu.Lock(); defer s.keyStateMu.Unlock()
	s.keyState[token] = "quota"
}

func (s *tokenAwareStub) seen() []string {
	s.tokensSeenMu.Lock(); defer s.tokensSeenMu.Unlock()
	out := make([]string, len(s.tokensSeen))
	copy(out, s.tokensSeen)
	return out
}

func (s *tokenAwareStub) handle(w http.ResponseWriter, r *http.Request) {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.tokensSeenMu.Lock(); s.tokensSeen = append(s.tokensSeen, auth); s.tokensSeenMu.Unlock()

	s.keyStateMu.Lock()
	state := s.keyState[auth]
	s.keyStateMu.Unlock()

	switch {
	case r.URL.Path == "/api/v4/extract/task" && r.Method == http.MethodPost:
		s.submissions.inc()
		if state == "quota" {
			// MinerU daily-limit code -60018 — matches errors.go::dailyLimitErrorCodes
			writeEnvelope(w, "-60018", "每日解析任务数量已达上限", nil)
			return
		}
		writeEnvelope(w, "0", "", map[string]any{"task_id": "tsk-1"})
	case strings.HasPrefix(r.URL.Path, "/api/v4/extract/task/") && r.Method == http.MethodGet:
		writeEnvelope(w, "0", "", map[string]any{
			"state": "done", "full_zip_url": s.server.URL + "/result",
		})
	case r.URL.Path == "/result":
		_, _ = w.Write(s.zipBody)
	default:
		http.NotFound(w, r)
	}
}

func TestConverter_FailsOverToNextKey(t *testing.T) {
	store := newFakeStore()
	store.put("pdf/2401/2401.12345v1.pdf", []byte("%PDF-fake"))

	stub := newTokenAwareStub(t)
	defer stub.close()
	stub.markQuotaExhausted("tok-a") // first key exhausted, second should win

	c := makeConverter(t, store, stub.url(), "tok-a", "tok-b")
	c.Ensure(context.Background(), "2401.12345v1")
	if !waitForJobState(c, "2401.12345v1", JobStateDone, 2*time.Second) {
		final, _ := c.Lookup("2401.12345v1")
		t.Fatalf("job did not reach Done with fail-over; final = %+v", final)
	}

	seen := stub.seen()
	if len(seen) < 2 {
		t.Fatalf("expected ≥2 stub hits (one per key), got %d: %v", len(seen), seen)
	}
	// First Submit should have been on tok-a (exhausted), second on tok-b (success).
	if seen[0] != "tok-a" {
		t.Errorf("first submit used %q, want tok-a", seen[0])
	}
	if seen[1] != "tok-b" {
		t.Errorf("after fail-over submit used %q, want tok-b", seen[1])
	}
	// Counters: exactly one Submitted from MinerU's perspective (the tok-b
	// retry); the tok-a 'daily-limit' was a key-pool rotation, not a paper-
	// level failure.
	snap := c.Snapshot()
	if snap.Succeeded != 1 {
		t.Errorf("Succeeded = %d, want 1", snap.Succeeded)
	}
	if snap.FailedDailyLimit != 0 {
		t.Errorf("FailedDailyLimit = %d; the paper succeeded after rotation so paper-level daily-limit shouldn't count", snap.FailedDailyLimit)
	}
}

func TestConverter_AllKeysExhaustedReturnsDailyLimit(t *testing.T) {
	store := newFakeStore()
	store.put("pdf/2401/2401.12345v1.pdf", []byte("%PDF-fake"))

	stub := newTokenAwareStub(t)
	defer stub.close()
	stub.markQuotaExhausted("tok-a")
	stub.markQuotaExhausted("tok-b")

	c := makeConverter(t, store, stub.url(), "tok-a", "tok-b")
	c.Ensure(context.Background(), "2401.12345v1")
	if !waitForJobState(c, "2401.12345v1", JobStateFailed, 2*time.Second) {
		t.Fatal("job did not fail when all keys exhausted")
	}
	job, _ := c.Lookup("2401.12345v1")
	if !errors.Is(job.ErrKind, ErrDailyLimit) {
		t.Errorf("ErrKind = %v, want ErrDailyLimit", job.ErrKind)
	}
	if job.Err == nil || !strings.Contains(job.Err.Error(), "all 2 MinerU API keys") {
		t.Errorf("Err = %v, want message naming the exhausted key count", job.Err)
	}
	// Cooldown should be at next 00:01 (well beyond a normal 60s failure window).
	if delta := time.Until(job.CooldownUntil); delta < 2*FailureCooldown {
		t.Errorf("daily-limit cooldown should be far in the future, got %v", delta)
	}
}

func TestKeyRing_AcquireRoundRobin(t *testing.T) {
	now := time.Now
	ring := NewKeyRing([]string{"a", "b", "c"}, "http://stub", now)
	if got := ring.Size(); got != 3 {
		t.Fatalf("Size = %d, want 3", got)
	}

	// Three Acquires in a row should hit each slot once (round-robin).
	seen := map[int]bool{}
	for i := 0; i < 3; i++ {
		_, slot, ok := ring.Acquire()
		if !ok {
			t.Fatalf("Acquire %d returned !ok", i)
		}
		if seen[slot] {
			t.Errorf("slot %d acquired twice in 3 calls — round-robin broken", slot)
		}
		seen[slot] = true
	}
}

func TestKeyRing_AllInCooldownReturnsNotOK(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 6, 4, 14, 0, 0, 0, time.UTC) }
	ring := NewKeyRing([]string{"a", "b"}, "http://stub", now)
	future := now().Add(24 * time.Hour)
	ring.MarkDailyLimit(0, future)
	ring.MarkDailyLimit(1, future)
	if _, _, ok := ring.Acquire(); ok {
		t.Error("Acquire returned ok=true with all keys in cooldown")
	}
	if got := ring.SoonestRecovery(); !got.Equal(future) {
		t.Errorf("SoonestRecovery = %v, want %v", got, future)
	}
	if got := ring.AvailableSlots(); got != 0 {
		t.Errorf("AvailableSlots = %d, want 0", got)
	}
}

func TestKeyRing_TrimsEmptyTokens(t *testing.T) {
	ring := NewKeyRing([]string{"", "a", "", "b", ""}, "http://stub", nil)
	if got := ring.Size(); got != 2 {
		t.Errorf("Size = %d, want 2 (empty strings should be dropped)", got)
	}
}

// ---------------------------------------------------------------------------
// Silent-fetch + LRO state machine integration tests (plan §4 Phase C / G4)
// ---------------------------------------------------------------------------

func newFakeArxivServer(t *testing.T, payload []byte) (string, *atomicInt64) {
	t.Helper()
	hits := newAtomicInt64()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.add(1)
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(payload)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/", hits
}

type atomicInt64 struct {
	mu  sync.Mutex
	val int64
}

func newAtomicInt64() *atomicInt64                  { return &atomicInt64{} }
func (a *atomicInt64) add(n int64)                  { a.mu.Lock(); a.val += n; a.mu.Unlock() }
func (a *atomicInt64) load() int64                  { a.mu.Lock(); defer a.mu.Unlock(); return a.val }

func makeFetcher(t *testing.T, baseURL string) *arxiv.Fetcher {
	t.Helper()
	f, err := arxiv.New(arxiv.Config{
		BaseURL:   baseURL,
		UserAgent: "qatlasd-test/0.0.0 (mailto:test@example.com)",
		RPS:       100, // way above test load — limiter shouldn't slow tests
		Burst:     100,
		MaxBytes:  10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("arxiv.New: %v", err)
	}
	return f
}

func makeConverterWithFetcher(t *testing.T, store objstore.Store, stubURL, arxivURL string, tokens ...string) *Converter {
	t.Helper()
	if len(tokens) == 0 {
		tokens = []string{"test-token"}
	}
	cfg := ConverterConfig{
		PaperAccessEnabled:      true,
		MinerUAPITokens:         tokens,
		MinerUAPIBaseURL:        stubURL,
		MinerUModelVersion:      "vlm",
		MinerULanguage:          "en",
		MinerUIsOCR:             false,
		MinerUEnableFormula:     true,
		MinerUEnableTable:       true,
		MinerUPollInterval:      5 * time.Millisecond,
		MinerUTimeout:           5 * time.Second,
		MinerUMaxConcurrentJobs: 2,
		S3PublicEndpoint:        "http://public.invalid",
		Fetcher:                 makeFetcher(t, arxivURL),
		ArxivFetchConcurrent:    2,
	}
	return NewConverter(cfg, store, nil, nil)
}

// %PDF- magic-byte + token padding so the fetcher's content check
// passes (it rejects bodies not starting with %PDF-).
var fakePDFBytes = []byte("%PDF-1.4\n%fake pdf for unit tests\n%%EOF\n")

// TestConverter_SilentFetchThenConvert verifies that Ensure on a paper
// with no PDF in store triggers an arxiv fetch (writes PDF to store)
// then proceeds to MinerU convert (writes md), landing the job in
// JobStateDone+PhaseReady. The Job snapshot exposes Fetch+Convert
// sub-states throughout.
func TestConverter_SilentFetchThenConvert(t *testing.T) {
	store := newFakeStore()
	// Note: NO PDF pre-seeded → forces silent fetch.

	arxivURL, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), arxivURL)

	job := c.Ensure(context.Background(), "2401.12345v1")
	if job.State != JobStateQueued {
		t.Fatalf("initial Ensure state = %v, want queued", job.State)
	}

	if !waitForJobState(c, "2401.12345v1", JobStateDone, 2*time.Second) {
		final, _ := c.Lookup("2401.12345v1")
		t.Fatalf("job did not reach Done; final = %+v", final)
	}

	// PDF must now exist in the store (written via silent fetch).
	if pdf, ok := store.get("pdf/2401/2401.12345v1.pdf"); !ok {
		t.Errorf("PDF not written to store after silent fetch")
	} else if !strings.HasPrefix(string(pdf), "%PDF-") {
		t.Errorf("written PDF is missing %%PDF- magic; got %q", string(pdf)[:8])
	}
	// Markdown must also exist (convert ran after fetch).
	if _, ok := store.get("markdown/2401/2401.12345v1.md"); !ok {
		t.Errorf("markdown not written to store")
	}
	if hits.load() != 1 {
		t.Errorf("arxiv hits = %d, want exactly 1 (single fetch even though dedupe was N/A)", hits.load())
	}

	final, _ := c.Lookup("2401.12345v1")
	if final.Phase != PhaseReady {
		t.Errorf("final Phase = %q, want %q", final.Phase, PhaseReady)
	}
	if final.Fetch == nil {
		t.Fatal("Fetch progress should be populated")
	}
	if final.Fetch.BytesReceived != int64(len(fakePDFBytes)) {
		t.Errorf("Fetch.BytesReceived = %d, want %d", final.Fetch.BytesReceived, len(fakePDFBytes))
	}
	if final.Fetch.Sha256 == "" {
		t.Error("Fetch.Sha256 should be populated after successful fetch")
	}
	if final.Convert == nil {
		t.Fatal("Convert progress should be populated")
	}

	snap := c.Snapshot()
	if snap.ArxivFetches != 1 || snap.ArxivFetchSucceeded != 1 || snap.ArxivFetchFailed != 0 {
		t.Errorf("fetch counters = %+v, want exactly 1 success", snap)
	}
	if snap.Submitted != 1 || snap.Succeeded != 1 {
		t.Errorf("convert counters = submitted=%d succeeded=%d, want 1/1", snap.Submitted, snap.Succeeded)
	}
}

// TestConverter_EnsurePDFFetchOnly verifies the /pdf endpoint's
// fetch-only entry point: it triggers a fetch when missing but
// never runs MinerU.
func TestConverter_EnsurePDFFetchOnly(t *testing.T) {
	store := newFakeStore()
	arxivURL, hits := newFakeArxivServer(t, fakePDFBytes)
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), arxivURL)

	job := c.EnsurePDF(context.Background(), "quant-ph/9508027v2")
	if job.State != JobStateQueued {
		t.Fatalf("initial EnsurePDF state = %v, want queued", job.State)
	}

	if !waitForJobState(c, "quant-ph/9508027v2", JobStateDone, 2*time.Second) {
		final, _ := c.Lookup("quant-ph/9508027v2")
		t.Fatalf("job did not reach Done; final = %+v", final)
	}

	// PDF should be in old-style layout (with category prefix).
	if _, ok := store.get("pdf/9508/quant-ph/9508027v2.pdf"); !ok {
		t.Errorf("PDF not written to store under old-style layout; objects = %v", listKeys(store))
	}
	// MinerU should NOT have been touched (fetch-only path).
	if _, ok := store.get("markdown/9508/quant-ph/9508027v2.md"); ok {
		t.Error("markdown unexpectedly written; EnsurePDF should not trigger MinerU")
	}
	if stub.submissions.load() != 0 {
		t.Errorf("stub.submissions = %d, want 0 (EnsurePDF must not call MinerU)", stub.submissions.load())
	}
	if hits.load() != 1 {
		t.Errorf("arxiv hits = %d, want 1", hits.load())
	}

	final, _ := c.Lookup("quant-ph/9508027v2")
	if final.Phase != PhaseReady {
		t.Errorf("Phase = %q, want %q", final.Phase, PhaseReady)
	}
	if final.Convert != nil {
		t.Errorf("Convert progress should be nil for PDF-only path, got %+v", final.Convert)
	}
}

// TestConverter_SilentFetchFailureLeavesNoPDF verifies that an arxiv
// 404 leaves the store untouched and surfaces as a fatal failure with
// PhaseErrorFetching.
func TestConverter_SilentFetchFailureLeavesNoPDF(t *testing.T) {
	store := newFakeStore()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.NotFound(w, nil)
	}))
	defer srv.Close()

	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverterWithFetcher(t, store, stub.url(), srv.URL+"/")

	c.Ensure(context.Background(), "2401.99999v1")
	if !waitForJobState(c, "2401.99999v1", JobStateFailed, 2*time.Second) {
		final, _ := c.Lookup("2401.99999v1")
		t.Fatalf("job did not reach Failed; final = %+v", final)
	}

	final, _ := c.Lookup("2401.99999v1")
	if final.Phase != PhaseErrorFetching {
		t.Errorf("Phase = %q, want %q", final.Phase, PhaseErrorFetching)
	}
	if !errors.Is(final.ErrKind, ErrFatal) {
		t.Errorf("ErrKind = %v, want ErrFatal (arxiv 404 should be fatal)", final.ErrKind)
	}
	if _, ok := store.get("pdf/2401/2401.99999v1.pdf"); ok {
		t.Error("PDF should not be in store after fetch failure")
	}
	if stub.submissions.load() != 0 {
		t.Error("MinerU should not have been called after fetch failure")
	}
}

// TestConverter_SilentFetchDisabledWithoutFetcher verifies that the
// pre-A2 behaviour is preserved when no fetcher is configured: missing
// PDF surfaces as a fatal "no PDF in store" without any silent fetch.
func TestConverter_SilentFetchDisabledWithoutFetcher(t *testing.T) {
	store := newFakeStore()
	stub := newMinerUStub(t)
	defer stub.close()
	c := makeConverter(t, store, stub.url()) // no Fetcher in ConverterConfig

	c.Ensure(context.Background(), "2401.12345v1")
	if !waitForJobState(c, "2401.12345v1", JobStateFailed, 2*time.Second) {
		t.Fatal("expected failed state without fetcher")
	}
	final, _ := c.Lookup("2401.12345v1")
	if !strings.Contains(errString(final.Err), "no PDF in store") {
		t.Errorf("error message should say 'no PDF in store'; got %q", errString(final.Err))
	}
}

// listKeys is a small helper for failure messages.
func listKeys(s *fakeStore) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.objects))
	for k := range s.objects {
		out = append(out, k)
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// ---------------------------------------------------------------------------
// Queue / ETA snapshot tests (plan §8 MinerU queue position + eta)
// ---------------------------------------------------------------------------

// TestConverter_AvgConvertDuration_EmptyAndSliding documents both
// the empty-history and full-window-rotation behaviour.
func TestConverter_AvgConvertDuration_EmptyAndSliding(t *testing.T) {
	c := &Converter{}

	// Empty ring → (0, 0)
	if avg, n := c.avgConvertDuration(); avg != 0 || n != 0 {
		t.Errorf("empty: avg=%v n=%d, want 0/0", avg, n)
	}

	// 5 samples, all 60s → avg=60s, n=5
	for i := 0; i < 5; i++ {
		c.recordConvertDuration(60 * time.Second)
	}
	if avg, n := c.avgConvertDuration(); avg != 60*time.Second || n != 5 {
		t.Errorf("5x60s: avg=%v n=%d, want 60s/5", avg, n)
	}

	// Push 20 more → ring overwrites; recentCount > cap, n capped at 20
	for i := 0; i < 20; i++ {
		c.recordConvertDuration(120 * time.Second)
	}
	avg, n := c.avgConvertDuration()
	if n != convertHistoryCap {
		t.Errorf("after overflow: n=%d, want %d", n, convertHistoryCap)
	}
	// 20 most-recent samples are all 120s now (we wrote 20 of them
	// after the initial 5, total 25 writes → ring contains the last
	// 20 = all 120s).
	if avg != 120*time.Second {
		t.Errorf("after overflow: avg=%v, want 120s", avg)
	}
}

// TestConverter_QueueSnapshotFor_EmptyDefaults verifies the
// "no history" path: avg falls back to MinerUTimeout/2 and basis
// reports default_no_history.
func TestConverter_QueueSnapshotFor_EmptyDefaults(t *testing.T) {
	c := makeConverter(t, newFakeStore(), "http://stub.invalid")
	c.cfg.MinerUTimeout = 60 * time.Second

	job := Job{
		Canonical:   "2401.99999v1",
		State:       JobStateQueued,
		SubmittedAt: time.Now(),
	}
	// Manually register it so queueSnapshotFor counts it.
	c.mu.Lock()
	c.jobs["2401.99999v1"] = &job
	c.mu.Unlock()

	qs := c.queueSnapshotFor("2401.99999v1", job)
	if qs.EtaBasis != "default_no_history" {
		t.Errorf("EtaBasis = %q, want default_no_history", qs.EtaBasis)
	}
	if qs.AvgDuration != 30*time.Second {
		t.Errorf("AvgDuration = %v, want half-timeout 30s", qs.AvgDuration)
	}
	if qs.Position != 1 {
		t.Errorf("Position = %d, want 1 (only job in queue)", qs.Position)
	}
	if qs.AheadOfMe != 0 {
		t.Errorf("AheadOfMe = %d, want 0", qs.AheadOfMe)
	}
	if qs.RunningCount != 0 {
		t.Errorf("RunningCount = %d, want 0", qs.RunningCount)
	}
	if qs.MaxConcurrent < 1 {
		t.Errorf("MaxConcurrent = %d, want >= 1", qs.MaxConcurrent)
	}
}

// TestConverter_QueueSnapshotFor_WithHistoryAndAhead exercises the
// realistic case: history of 60s/job, MaxConcurrent=2, 3 running +
// 4 queued ahead of "me" → ETA = ceil((4+3)/2) * 60s = 4 * 60s = 240s.
func TestConverter_QueueSnapshotFor_WithHistoryAndAhead(t *testing.T) {
	c := makeConverter(t, newFakeStore(), "http://stub.invalid")
	// Force MaxConcurrent = 2 by replacing the semaphore.
	c.sem = make(chan struct{}, 2)

	// Seed 10 finishes at 60s each → avg=60s
	for i := 0; i < 10; i++ {
		c.recordConvertDuration(60 * time.Second)
	}

	now := time.Now()
	// 3 running, 4 queued ahead, then "me" queued last.
	mk := func(id string, st JobState, t time.Time) {
		c.jobs[id] = &Job{Canonical: id, State: st, SubmittedAt: t}
	}
	c.mu.Lock()
	mk("r1", JobStateRunning, now.Add(-300*time.Second))
	mk("r2", JobStateRunning, now.Add(-280*time.Second))
	mk("r3", JobStateRunning, now.Add(-260*time.Second))
	mk("q1", JobStateQueued, now.Add(-50*time.Second))
	mk("q2", JobStateQueued, now.Add(-40*time.Second))
	mk("q3", JobStateQueued, now.Add(-30*time.Second))
	mk("q4", JobStateQueued, now.Add(-20*time.Second))
	mk("me", JobStateQueued, now)
	c.mu.Unlock()

	me := *c.jobs["me"]
	qs := c.queueSnapshotFor("me", me)

	if qs.RunningCount != 3 {
		t.Errorf("RunningCount = %d, want 3", qs.RunningCount)
	}
	if qs.AheadOfMe != 4 {
		t.Errorf("AheadOfMe = %d, want 4", qs.AheadOfMe)
	}
	if qs.Position != 5 {
		t.Errorf("Position = %d, want 5", qs.Position)
	}
	if qs.MaxConcurrent != 2 {
		t.Errorf("MaxConcurrent = %d, want 2", qs.MaxConcurrent)
	}
	// (4 ahead + 3 running) / 2 = ceil(3.5) = 4 batches * 60s = 240s
	if qs.EtaSeconds != 240 {
		t.Errorf("EtaSeconds = %d, want 240", qs.EtaSeconds)
	}
	if !strings.HasPrefix(qs.EtaBasis, "observed_avg_of_") {
		t.Errorf("EtaBasis = %q, want observed_avg_of_*", qs.EtaBasis)
	}
}

// TestConverter_QueueSnapshotFor_RunningOmitsPosition verifies that
// a job whose State has already advanced to Running gets a snapshot
// with Position=0 (so the handler can decide to suppress the field).
func TestConverter_QueueSnapshotFor_RunningOmitsPosition(t *testing.T) {
	c := makeConverter(t, newFakeStore(), "http://stub.invalid")
	c.sem = make(chan struct{}, 4)
	c.recordConvertDuration(45 * time.Second)

	now := time.Now()
	c.mu.Lock()
	c.jobs["me"] = &Job{Canonical: "me", State: JobStateRunning, SubmittedAt: now}
	c.mu.Unlock()

	me := *c.jobs["me"]
	qs := c.queueSnapshotFor("me", me)
	if qs.Position != 0 {
		t.Errorf("Position = %d, want 0 for running job", qs.Position)
	}
	if qs.EtaSeconds != 0 {
		t.Errorf("EtaSeconds = %d, want 0 for running job", qs.EtaSeconds)
	}
}
