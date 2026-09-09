package downloadworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

type closingBody struct {
	io.Reader
	closed atomic.Bool
}

func (b *closingBody) Close() error { b.closed.Store(true); return nil }

type bodyFetcher struct{ body io.Reader }

func (f bodyFetcher) FetchPDF(context.Context, registry.PaperRef) (*downloader.FetchOutcome, error) {
	return &downloader.FetchOutcome{Result: &downloader.FetchResult{Body: f.body}}, nil
}
func TestFetchedBodyClosedAfterDurableSpoolCopy(t *testing.T) {
	s := testSpool(t, MaxPDFBytes, time.Hour)
	a := assignment("closable")
	if err := s.Start(a, 2, time.Now()); err != nil {
		t.Fatal(err)
	}
	body := &closingBody{Reader: bytes.NewReader([]byte("%PDF-closable"))}
	r := Runner{Config: DefaultConfig(), Spool: s, Fetcher: bodyFetcher{body}, active: map[string]activeTask{}, fatal: make(chan error, 1)}
	r.startTask(context.Background(), a)
	r.wg.Wait()
	if !body.closed.Load() {
		t.Fatal("fetched reader was not closed")
	}
	if records := s.Records(); len(records) != 1 || records[0].State != "ready" {
		t.Fatal("body was not durably spooled")
	}
}

type immediateFetcher struct{}

func (immediateFetcher) FetchPDF(context.Context, registry.PaperRef) (*downloader.FetchOutcome, error) {
	return &downloader.FetchOutcome{DOI: "10.1234/test", Strategy: "test", Result: &downloader.FetchResult{Body: bytes.NewReader([]byte("%PDF-test body"))}}, nil
}

func TestUploadDoesNotBlockHeartbeatAndCancelsCleanly(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Name = "test"
	cfg.AllowHTTP = true
	cfg.PollInterval = 20 * time.Millisecond
	s := testSpool(t, 3*MaxPDFBytes, time.Hour)
	cfg.DataDir = s.dir
	id := Identity{ID: "persistent-worker-id", Secret: strings.Repeat("s", 64), Registered: true}
	var claims atomic.Int32
	var uploading atomic.Bool
	started := make(chan struct{}, 1)
	heartbeatDuringUpload := make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == workerprotocol.StatusPath:
			_ = json.NewEncoder(w).Encode(workerprotocol.Node{ID: id.ID, Status: "approved"})
		case r.URL.Path == workerprotocol.HeartbeatPath:
			if uploading.Load() {
				select {
				case heartbeatDuringUpload <- struct{}{}:
				default:
				}
			}
			_ = json.NewEncoder(w).Encode(workerprotocol.HeartbeatResponse{Status: "approved", LeaseExpires: map[string]time.Time{"upload": time.Now().Add(time.Minute)}})
		case r.URL.Path == workerprotocol.ClaimPath:
			attempts := []workerprotocol.Assignment{}
			if claims.Add(1) == 1 {
				attempts = append(attempts, assignment("upload"))
			}
			_ = json.NewEncoder(w).Encode(workerprotocol.ClaimResponse{Attempts: attempts})
		case strings.HasPrefix(r.URL.Path, workerprotocol.ReceiptPath):
			_ = json.NewEncoder(w).Encode(workerprotocol.Receipt{TaskID: "task-upload", AttemptID: "upload", State: "running"})
		case strings.HasPrefix(r.URL.Path, workerprotocol.UploadPath):
			_, _ = io.Copy(io.Discard, r.Body)
			uploading.Store(true)
			started <- struct{}{}
			<-r.Context().Done()
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg.MasterURL = server.URL
	runner := Runner{Config: cfg, Spool: s, Identity: id, Client: testClient(t, server.URL), Fetcher: immediateFetcher{}, BrowserHealthy: func(context.Context) bool { return true }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("upload did not start")
	}
	select {
	case <-heartbeatDuringUpload:
	case <-time.After(3 * time.Second):
		t.Fatal("upload blocked heartbeat")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("upload shutdown blocked")
	}
	records := s.Records()
	if len(records) != 1 || records[0].State != "ready" {
		t.Fatal("cancelled upload lost ready result")
	}
}

type blockingFetcher struct {
	started chan struct{}
	active  atomic.Int32
	peak    atomic.Int32
}

func (f *blockingFetcher) FetchPDF(ctx context.Context, _ registry.PaperRef) (*downloader.FetchOutcome, error) {
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		old := f.peak.Load()
		if old >= n || f.peak.CompareAndSwap(old, n) {
			break
		}
	}
	f.started <- struct{}{}
	<-ctx.Done()
	return nil, ctx.Err()
}
func TestRunnerEnforcesDefaultConcurrencyAndShutdown(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Concurrency != 2 {
		t.Fatal("default concurrency changed")
	}
	cfg.Name = "test"
	cfg.AllowHTTP = true
	cfg.PollInterval = 20 * time.Millisecond
	s := testSpool(t, 5*MaxPDFBytes, time.Hour)
	cfg.DataDir = s.dir
	id := Identity{ID: "persistent-worker-id", Secret: strings.Repeat("s", 64), Registered: true}
	var claims atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case workerprotocol.StatusPath:
			_ = json.NewEncoder(w).Encode(workerprotocol.Node{ID: id.ID, Status: "approved"})
		case workerprotocol.HeartbeatPath:
			_ = json.NewEncoder(w).Encode(workerprotocol.HeartbeatResponse{Status: "approved", LeaseExpires: map[string]time.Time{"one": time.Now().Add(time.Minute), "two": time.Now().Add(time.Minute)}})
		case workerprotocol.ClaimPath:
			var req workerprotocol.ClaimRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			if req.Limit != 2 {
				t.Errorf("claim limit=%d", req.Limit)
			}
			if claims.Add(1) == 1 {
				_ = json.NewEncoder(w).Encode(workerprotocol.ClaimResponse{Attempts: []workerprotocol.Assignment{assignment("one"), assignment("two")}})
			} else {
				_ = json.NewEncoder(w).Encode(workerprotocol.ClaimResponse{})
			}
		default:
			w.WriteHeader(404)
		}
	}))
	defer server.Close()
	cfg.MasterURL = server.URL
	f := &blockingFetcher{started: make(chan struct{}, 2)}
	runner := Runner{Config: cfg, Spool: s, Identity: id, Client: testClient(t, server.URL), Fetcher: f, BrowserHealthy: func(context.Context) bool { return true }}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	for range 2 {
		select {
		case <-f.started:
		case <-time.After(3 * time.Second):
			t.Fatal("downloads not started")
		}
	}
	if slots := s.Slots(cfg.Concurrency); slots != 0 {
		t.Fatal("runner left extra slots", slots)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel contexts")
	}
	if f.peak.Load() != 2 || f.active.Load() != 0 {
		t.Fatal("incorrect active limit/lifecycle")
	}
	for _, record := range s.Records() {
		if record.State != "failed" {
			t.Fatal("interrupted task not durable", record.State)
		}
	}
}
func TestEnrollmentRetriesPersistentIdentityAndPendingDoesNotClaim(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Name = "test"
	cfg.EnrollmentToken = "enrollment-sensitive"
	cfg.AllowHTTP = true
	s := testSpool(t, 2*MaxPDFBytes, time.Hour)
	cfg.DataDir = s.dir
	var registered atomic.Bool
	var registerCalls atomic.Int32
	var otherCalls atomic.Int32
	var identity Identity
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case workerprotocol.StatusPath:
			if !registered.Load() {
				w.WriteHeader(401)
				return
			}
			_ = json.NewEncoder(w).Encode(workerprotocol.Node{ID: identity.ID, Status: "pending"})
		case workerprotocol.RegisterPath:
			registerCalls.Add(1)
			var req workerprotocol.RegisterRequest
			_ = json.NewDecoder(r.Body).Decode(&req)
			persisted, err := s.LoadIdentity(cfg.MasterURL)
			if err != nil || persisted.ID != req.ID || persisted.Secret != req.Secret {
				t.Error("identity not durable before enrollment")
			}
			if req.EnrollmentToken != cfg.EnrollmentToken {
				t.Error("wrong enrollment token")
			}
			registered.Store(true)
			w.WriteHeader(503) // enrollment committed, response lost
		default:
			otherCalls.Add(1)
			w.WriteHeader(500)
		}
	}))
	defer server.Close()
	cfg.MasterURL = server.URL
	var err error
	identity, err = s.LoadIdentity(cfg.MasterURL)
	if err != nil {
		t.Fatal(err)
	}
	c, err := NewClient(cfg, identity)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := Runner{Config: cfg, Spool: s, Client: c, Identity: identity, active: map[string]activeTask{}, BrowserHealthy: func(context.Context) bool { return true }}
	if err = r.cycle(context.Background()); err == nil {
		t.Fatal("lost enrollment response accepted")
	}
	if err = r.cycle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !r.Identity.Registered || registerCalls.Load() != 1 || otherCalls.Load() != 0 {
		t.Fatal("enrollment retry/pending isolation incorrect")
	}
	persisted, err := s.LoadIdentity(cfg.MasterURL)
	if err != nil || !persisted.Registered {
		t.Fatal("enrollment state not committed")
	}
}
func TestBrowserFlagsAndHealth(t *testing.T) {
	args := strings.Join(browserArgs("/headless-shell/headless-shell", "/tmp/profile"), " ")
	if strings.Contains(args, "--headless") || !strings.Contains(args, "--remote-debugging-address=127.0.0.1") {
		t.Fatal(args)
	}
	if !strings.Contains(strings.Join(browserArgs("chromium", "/tmp/profile"), " "), "--headless=new") {
		t.Fatal("full Chrome not headless")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"webSocketDebuggerUrl": "ws://localhost/devtools/browser/test"})
	}))
	defer server.Close()
	b, err := StartBrowser(context.Background(), server.URL, "")
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if !b.Healthy(context.Background()) {
		t.Fatal("external CDP health failed")
	}
	dir := t.TempDir()
	now := time.Now()
	if err = atomicJSON(filepath.Join(dir, "health.json"), Health{Updated: now, Healthy: true}); err != nil {
		t.Fatal(err)
	}
	if err = CheckHealth(dir, now); err != nil {
		t.Fatal(err)
	}
	if err = CheckHealth(dir, now.Add(91*time.Second)); err == nil {
		t.Fatal("stale health accepted")
	}
	if err = atomicJSON(filepath.Join(dir, "health.json"), Health{Updated: now, Healthy: false}); err != nil {
		t.Fatal(err)
	}
	if err = CheckHealth(dir, now); err == nil {
		t.Fatal("unhealthy accepted")
	}
}
func TestMetadataNeverExportsArbitraryErrorsOrURLSecrets(t *testing.T) {
	out := &downloader.FetchOutcome{DOI: "10.1234/test", URL: "https://user:secret@publisher/pdf?token=secret#secret", Trace: []downloader.Attempt{{URL: "https://publisher/pdf?key=secret", Error: errors.New("secret").Error()}}}
	b, err := json.Marshal(resultMetadata(out))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "secret") {
		t.Fatal("provenance leaked secret")
	}
}
