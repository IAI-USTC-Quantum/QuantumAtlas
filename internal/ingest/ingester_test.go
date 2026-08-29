package ingest

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// fakeReg implements registryWriter, recording every call.
type fakeReg struct {
	mu       sync.Mutex
	upserts  []upsertCall
	statuses []string
}

type upsertCall struct {
	ref     registry.PaperRef
	version int
	sha256  string
	size    int64
	pdfPath string
}

func (f *fakeReg) UpsertPDF(_ context.Context, ref registry.PaperRef, version int, sha string, size int64, pdfPath string) (string, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserts = append(f.upserts, upsertCall{ref: ref, version: version, sha256: sha, size: size, pdfPath: pdfPath})
	return ref.ArxivID, 1, nil
}

func (f *fakeReg) UpdateStatus(_ context.Context, paperID, status string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, paperID+":"+status)
	return true, nil
}

func (f *fakeReg) snapshot() ([]upsertCall, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]upsertCall(nil), f.upserts...), append([]string(nil), f.statuses...)
}

// testPDF is a minimal byte string that passes the fetcher's %PDF-
// magic check.
var testPDF = []byte("%PDF-1.4\n% fake ingest test fixture\n")

func testPDFSha() string {
	sum := sha256.Sum256(testPDF)
	return hex.EncodeToString(sum[:])
}

// arxivStub serves /abs/<id> (og:url version tag) and /pdf/<id>
// (the fixture bytes). Paths not in the maps get a 404.
type arxivStub struct {
	absPages  map[string]string // id -> version to advertise, e.g. "2401.12345" -> "v3"
	pdfBytes  map[string][]byte // full versioned id -> body
	pdfHits   atomic.Int64
	absHits   atomic.Int64
	gate      chan struct{} // when non-nil, pdf handlers signal then block until closed
	pdfServed chan struct{} // signals a pdf handler was entered
	once      sync.Once
}

func (s *arxivStub) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/abs/", func(w http.ResponseWriter, r *http.Request) {
		s.absHits.Add(1)
		id := r.URL.Path[len("/abs/"):]
		v, ok := s.absPages[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprintf(w, `<html><head><meta property="og:url" content="https://arxiv.org/abs/%s%s" /></head></html>`, id, v)
	})
	mux.HandleFunc("/pdf/", func(w http.ResponseWriter, r *http.Request) {
		s.pdfHits.Add(1)
		if s.gate != nil {
			s.once.Do(func() { close(s.pdfServed) })
			<-s.gate
		}
		id := r.URL.Path[len("/pdf/"):]
		body, ok := s.pdfBytes[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write(body)
	})
	return mux
}

// newTestIngester wires a stub arxiv server + real fetcher + real
// LocalStore + fake registry into an Ingester.
func newTestIngester(t *testing.T, stub *arxivStub, reg registryWriter, opts ...Option) (*Ingester, *objstore.LocalStore) {
	t.Helper()
	srv := httptest.NewServer(stub.handler())
	t.Cleanup(srv.Close)
	fetcher, err := arxiv.New(arxiv.Config{
		BaseURL:    srv.URL + "/pdf/",
		AbsBaseURL: srv.URL + "/abs/",
		RPS:        1000,
		Burst:      1000,
	})
	if err != nil {
		t.Fatalf("arxiv.New: %v", err)
	}
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	base := []Option{WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	ing := newIngester(reg, fetcher, store, append(base, opts...)...)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := ing.Shutdown(ctx); err != nil {
			t.Errorf("Shutdown: %v", err)
		}
	})
	return ing, store
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func TestOnMintCoalesces(t *testing.T) {
	stub := &arxivStub{
		pdfBytes:  map[string][]byte{"2401.12345v1": testPDF},
		gate:      make(chan struct{}),
		pdfServed: make(chan struct{}),
	}
	reg := &fakeReg{}
	ing, _ := newTestIngester(t, stub, reg)

	ref := registry.PaperRef{ArxivID: "2401.12345v1"}
	ing.OnMint(context.Background(), "qa_paper1", ref)

	// Wait until the worker is inside the (gated) fetch, then fire a
	// duplicate notification — it must coalesce onto the in-flight run.
	select {
	case <-stub.pdfServed:
	case <-time.After(5 * time.Second):
		t.Fatal("pdf handler never entered")
	}
	ing.OnMint(context.Background(), "qa_paper1", ref)
	ing.OnMint(context.Background(), "qa_paper1", ref)
	close(stub.gate)

	waitFor(t, "fetched==1", func() bool { return ing.Snapshot()["fetched"] == 1 })
	if got := stub.pdfHits.Load(); got != 1 {
		t.Fatalf("expected exactly 1 pdf fetch, got %d", got)
	}
	if got := ing.Snapshot()["queued"]; got != 1 {
		t.Fatalf("expected exactly 1 enqueue, got %d", got)
	}
}

func TestOnMintDOIOnlySkips(t *testing.T) {
	stub := &arxivStub{}
	reg := &fakeReg{}
	ing, _ := newTestIngester(t, stub, reg)

	ing.OnMint(context.Background(), "qa_doi1", registry.PaperRef{DOI: "10.1000/xyz.123"})

	waitFor(t, "skipped==1", func() bool { return ing.Snapshot()["skipped"] == 1 })
	upserts, statuses := reg.snapshot()
	if len(upserts) != 0 {
		t.Fatalf("expected no UpsertPDF calls, got %+v", upserts)
	}
	if len(statuses) != 0 {
		t.Fatalf("expected no UpdateStatus calls (paper stays pending), got %v", statuses)
	}
	if got := stub.pdfHits.Load() + stub.absHits.Load(); got != 0 {
		t.Fatalf("expected no arxiv traffic, got %d hits", got)
	}
}

func TestIngestSuccessResolvesVersion(t *testing.T) {
	stub := &arxivStub{
		absPages: map[string]string{"2401.12345": "v3"},
		pdfBytes: map[string][]byte{"2401.12345v3": testPDF},
	}
	reg := &fakeReg{}
	ing, _ := newTestIngester(t, stub, reg)

	// Versionless ref (the OpenAlex landing_page_url form): the ingester
	// must resolve the latest version before fetching.
	ing.OnMint(context.Background(), "qa_paper2", registry.PaperRef{ArxivID: "2401.12345"})

	waitFor(t, "fetched==1", func() bool { return ing.Snapshot()["fetched"] == 1 })

	upserts, statuses := reg.snapshot()
	if len(statuses) != 0 {
		t.Fatalf("expected no UpdateStatus calls on success, got %v", statuses)
	}
	if len(upserts) != 1 {
		t.Fatalf("expected 1 UpsertPDF, got %+v", upserts)
	}
	u := upserts[0]
	if u.version != 3 {
		t.Errorf("version = %d, want 3", u.version)
	}
	if u.ref.ArxivID != "2401.12345v3" {
		t.Errorf("ref.ArxivID = %q, want 2401.12345v3", u.ref.ArxivID)
	}
	if u.sha256 != testPDFSha() {
		t.Errorf("sha256 = %q, want %q", u.sha256, testPDFSha())
	}
	if u.size != int64(len(testPDF)) {
		t.Errorf("size = %d, want %d", u.size, len(testPDF))
	}
	// Legacy bucketRelKey convention: kind segment stripped.
	if u.pdfPath != "2401/2401.12345v3.pdf" {
		t.Errorf("pdfPath = %q, want %q", u.pdfPath, "2401/2401.12345v3.pdf")
	}
	if got := stub.absHits.Load(); got != 1 {
		t.Errorf("abs page hits = %d, want 1 (version resolution)", got)
	}
}

func TestIngestSuccessStoresBytes(t *testing.T) {
	stub := &arxivStub{
		pdfBytes: map[string][]byte{"quant-ph/9508027v2": testPDF},
	}
	reg := &fakeReg{}
	ing, store := newTestIngester(t, stub, reg)

	// Old-style canonical id exercises the per-category key layout.
	ing.OnMint(context.Background(), "qa_paper3", registry.PaperRef{ArxivID: "quant-ph/9508027v2"})

	waitFor(t, "fetched==1", func() bool { return ing.Snapshot()["fetched"] == 1 })

	upserts, _ := reg.snapshot()
	if len(upserts) != 1 {
		t.Fatalf("expected 1 UpsertPDF, got %+v", upserts)
	}
	if want := "9508/quant-ph/9508027v2.pdf"; upserts[0].pdfPath != want {
		t.Errorf("pdfPath = %q, want %q", upserts[0].pdfPath, want)
	}

	// Verify the stored bytes round-trip through the objstore at the
	// canonical AssetKey.
	rc, _, err := store.Get(context.Background(), "pdf/9508/quant-ph/9508027v2.pdf")
	if err != nil {
		t.Fatalf("Get stored pdf: %v", err)
	}
	defer rc.Close()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read stored pdf: %v", err)
	}
	if string(got) != string(testPDF) {
		t.Errorf("stored bytes mismatch: got %q", got)
	}
}

func TestIngestFailureMarksFailed(t *testing.T) {
	stub := &arxivStub{} // nothing served -> 404 everywhere
	reg := &fakeReg{}
	ing, _ := newTestIngester(t, stub, reg)

	ing.OnMint(context.Background(), "qa_gone", registry.PaperRef{ArxivID: "2401.99999v1"})

	waitFor(t, "failed==1", func() bool { return ing.Snapshot()["failed"] == 1 })

	upserts, statuses := reg.snapshot()
	if len(upserts) != 0 {
		t.Fatalf("expected no UpsertPDF on failure, got %+v", upserts)
	}
	if len(statuses) != 1 || statuses[0] != "qa_gone:failed" {
		t.Fatalf("statuses = %v, want [qa_gone:failed]", statuses)
	}
}

func TestOnMintNilFetcherDisabled(t *testing.T) {
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	reg := &fakeReg{}
	ing := newIngester(reg, nil, store)
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = ing.Shutdown(ctx)
	}()

	ing.OnMint(context.Background(), "qa_x", registry.PaperRef{ArxivID: "2401.12345v1"})
	// Give any (buggy) enqueue a chance to run.
	time.Sleep(50 * time.Millisecond)
	snap := ing.Snapshot()
	for k, v := range snap {
		if v != 0 {
			t.Fatalf("nil fetcher must disable ingestion; snapshot[%s]=%d", k, v)
		}
	}
}

func TestLocalStoreKeyLayout(t *testing.T) {
	// Sanity anchor for the success-path assertions above: LocalStore
	// maps "pdf/<rel>" to "<base>/pdf/<rel>".
	dir := t.TempDir()
	store, err := objstore.NewLocalStore(dir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	key := "pdf/2401/2401.12345v3.pdf"
	if _, err := store.Put(context.Background(), key, bytes.NewReader(testPDF), int64(len(testPDF)), "application/pdf"); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "pdf", "2401", "2401.12345v3.pdf")); err != nil {
		t.Fatalf("expected object at %s: %v", key, err)
	}
}

// fakePusher records PushIndex calls (the qatlas-rag index push).
type fakePusher struct {
	mu     sync.Mutex
	pushed []string
	err    error
}

func (f *fakePusher) PushIndex(_ context.Context, paperID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pushed = append(f.pushed, paperID)
	return f.err
}

func (f *fakePusher) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pushed...)
}

func TestIngestPushesIndexOnReady(t *testing.T) {
	stub := &arxivStub{
		pdfBytes: map[string][]byte{"2401.12345v1": testPDF},
	}
	reg := &fakeReg{}
	pusher := &fakePusher{}
	ing, _ := newTestIngester(t, stub, reg, WithIndexPusher(pusher))

	ing.OnMint(context.Background(), "qa_push1", registry.PaperRef{ArxivID: "2401.12345v1"})

	waitFor(t, "fetched==1", func() bool { return ing.Snapshot()["fetched"] == 1 })
	pushed := pusher.snapshot()
	if len(pushed) != 1 || pushed[0] != "2401.12345v1" {
		t.Fatalf("pushed = %v, want [2401.12345v1]", pushed)
	}
}

func TestIngestPushFailureDoesNotFailIngest(t *testing.T) {
	stub := &arxivStub{
		pdfBytes: map[string][]byte{"2401.12345v1": testPDF},
	}
	reg := &fakeReg{}
	pusher := &fakePusher{err: errors.New("qatlas-rag down")}
	ing, _ := newTestIngester(t, stub, reg, WithIndexPusher(pusher))

	ing.OnMint(context.Background(), "qa_push2", registry.PaperRef{ArxivID: "2401.12345v1"})

	// Best-effort: the paper still flips to ready and nothing is marked
	// failed when the push errors.
	waitFor(t, "fetched==1", func() bool { return ing.Snapshot()["fetched"] == 1 })
	if got := ing.Snapshot()["failed"]; got != 0 {
		t.Fatalf("failed = %d, want 0 (push failure is best-effort)", got)
	}
	_, statuses := reg.snapshot()
	if len(statuses) != 0 {
		t.Fatalf("expected no UpdateStatus calls, got %v", statuses)
	}
}
