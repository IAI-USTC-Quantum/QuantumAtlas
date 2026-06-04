package mineru

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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

func makeConverter(t *testing.T, store objstore.Store, stubURL string) *Converter {
	t.Helper()
	cfg := ConverterConfig{
		AssetDownloadsEnabled:   true,
		MinerUAPIToken:          "test-token",
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
	return NewConverter(cfg, store, nil, NewClient("test-token", stubURL, nil), nil)
}

func TestConverter_DisabledWhenSwitchOff(t *testing.T) {
	store := newFakeStore()
	c := NewConverter(ConverterConfig{AssetDownloadsEnabled: false}, store, nil, nil, nil)
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
		AssetDownloadsEnabled: true,
		S3PublicEndpoint:      "http://public.invalid",
	}, store, nil, nil, nil)
	if c.Enabled() {
		t.Fatal("converter should be disabled when token missing")
	}
	if !strings.Contains(c.DisabledReason(), "MINERU_API_TOKEN") {
		t.Fatalf("DisabledReason = %q, want hint about MINERU_API_TOKEN", c.DisabledReason())
	}
}

func TestConverter_DisabledWhenPublicEndpointMissing(t *testing.T) {
	store := newFakeStore()
	c := NewConverter(ConverterConfig{
		AssetDownloadsEnabled: true,
		MinerUAPIToken:        "tok",
	}, store, nil, nil, nil)
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
	// Image too.
	if _, ok := store.get("images/2401/2401.12345v1/a.png"); !ok {
		t.Errorf("image not written to store")
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
