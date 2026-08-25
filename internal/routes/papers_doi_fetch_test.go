package routes

// Tests for the DOI fetch+convert LRO flow (plan §A):
// getMarkdownByDOIHandler on cache miss now drives converter.EnsureByDOI
// (202 + Operation-Location) instead of a bare 404, and
// markdownStatusByDOIHandler surfaces the in-flight job for polling.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/pocketbase/pocketbase/core"
)

// doiFlowStore is a full in-memory objstore.Store for the DOI flow
// tests (the upload-test fakeStore panics on Get, which the markdown
// handler needs).
type doiFlowStore struct {
	mu      sync.Mutex
	objects map[string][]byte
}

func newDOIFlowStore() *doiFlowStore { return &doiFlowStore{objects: map[string][]byte{}} }

func (s *doiFlowStore) Put(ctx context.Context, key string, r io.Reader, size int64, ct string) (int64, error) {
	return s.PutWithOptions(ctx, key, r, size, objstore.PutOptions{ContentType: ct})
}
func (s *doiFlowStore) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, ct string, md map[string]string) (int64, error) {
	return s.PutWithOptions(ctx, key, r, size, objstore.PutOptions{ContentType: ct, Metadata: md})
}
func (s *doiFlowStore) PutWithOptions(_ context.Context, key string, r io.Reader, _ int64, po objstore.PutOptions) (int64, error) {
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
func (s *doiFlowStore) Get(_ context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[key]
	if !ok {
		return nil, objstore.ObjectInfo{}, objstore.ErrNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), objstore.ObjectInfo{Key: key, Size: int64(len(b))}, nil
}
func (s *doiFlowStore) Stat(_ context.Context, key string) (objstore.ObjectInfo, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.objects[key]
	if !ok {
		return objstore.ObjectInfo{}, false, nil
	}
	return objstore.ObjectInfo{Key: key, Size: int64(len(b))}, true, nil
}
func (s *doiFlowStore) Delete(_ context.Context, key string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key)
	return nil
}
func (s *doiFlowStore) ListPrefix(_ context.Context, prefix string, _ int) ([]objstore.ObjectInfo, error) {
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
func (s *doiFlowStore) ListDirs(_ context.Context, _ string) ([]string, error) { return nil, nil }
func (s *doiFlowStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "", false, nil
}

// doiMinerUStub is a minimal MinerU upload-channel stub: batch create →
// PUT upload URL → poll reports done → result zip with a full.md.
type doiMinerUStub struct {
	server *httptest.Server
	zip    []byte
}

func newDOIMinerUStub(t *testing.T) *doiMinerUStub {
	t.Helper()
	stub := &doiMinerUStub{zip: buildDOIResultZip(t, "# Hello DOI\n\nconverted.\n")}
	stub.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v4/file-urls/batch" && r.Method == http.MethodPost:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"batch_id":  "batch-1",
				"file_urls": []string{stub.server.URL + "/upload/0"},
			}})
		case r.URL.Path == "/upload/0" && r.Method == http.MethodPut:
			_, _ = io.Copy(io.Discard, r.Body)
			w.WriteHeader(http.StatusOK)
		case strings.HasPrefix(r.URL.Path, "/api/v4/extract-results/batch/") && r.Method == http.MethodGet:
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "0", "data": map[string]any{
				"extract_result": []any{map[string]any{
					"file_name": "paper.pdf", "state": "done",
					"full_zip_url": stub.server.URL + "/result",
				}},
			}})
		case r.URL.Path == "/result":
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(stub.zip)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(stub.server.Close)
	return stub
}

func buildDOIResultZip(t *testing.T, md string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create("out/full.md")
	if err != nil {
		t.Fatalf("zip create: %v", err)
	}
	if _, err := w.Write([]byte(md)); err != nil {
		t.Fatalf("zip write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("zip close: %v", err)
	}
	return buf.Bytes()
}

// newDOIFlowConverter wires a converter with the MinerU stub and an
// arxiv.Fetcher (FetchURL targets the supplied OA PDF server).
func newDOIFlowConverter(t *testing.T, store objstore.Store, stub *doiMinerUStub) *mineru.Converter {
	t.Helper()
	fetcher, err := arxiv.New(arxiv.Config{
		BaseURL:   "http://arxiv-unused.invalid/",
		UserAgent: "qatlasd-test/0.0.0 (mailto:test@example.com)",
		RPS:       100,
		Burst:     100,
		MaxBytes:  10 * 1024 * 1024,
	})
	if err != nil {
		t.Fatalf("arxiv.New: %v", err)
	}
	return mineru.NewConverter(mineru.ConverterConfig{
		PaperAccessEnabled:      true,
		MinerUAPITokens:         []string{"test-token"},
		MinerUAPIBaseURL:        stub.server.URL,
		MinerUModelVersion:      "vlm",
		MinerUPollInterval:      5 * time.Millisecond,
		MinerUTimeout:           10 * time.Second,
		MinerUMaxConcurrentJobs: 2,
		Fetcher:                 fetcher,
		ArxivFetchConcurrent:    2,
	}, store, nil, nil)
}

func mustDOIMarkdownReq(t *testing.T, doi string) (*core.RequestEvent, *httptest.ResponseRecorder) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/"+doi+"/markdown", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	return re, rec
}

// fakeOAPDFServer serves one %PDF- payload like an OA publisher host.
func fakeOAPDFServer(t *testing.T) (url string, hits *int) {
	t.Helper()
	n := new(int)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		*n++
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.4\n%fake OA pdf\n%%EOF\n"))
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/paper.pdf", n
}

// TestGetMarkdownByDOI_MissTriggersFetchConvertLRO: miss → 202 with
// Operation-Location; the background job fetches the OA PDF, converts,
// and a later GET streams the markdown (200). The status endpoint
// renders the job for polling clients.
func TestGetMarkdownByDOI_MissTriggersFetchConvertLRO(t *testing.T) {
	doi := "10.1038/s41534-020-00001-0"
	store := newDOIFlowStore()
	stub := newDOIMinerUStub(t)
	converter := newDOIFlowConverter(t, store, stub)
	oaURL, hits := fakeOAPDFServer(t)

	re, rec := mustDOIMarkdownReq(t, doi)
	if err := getMarkdownByDOIHandler(re, &config.Config{}, store, converter, doi, oaURL); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202 (body: %s)", rec.Code, rec.Body.String())
	}
	if loc := rec.Header().Get("Operation-Location"); loc != "/api/papers/"+doi+"/markdown/status" {
		t.Errorf("Operation-Location = %q", loc)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 202 body: %v", err)
	}
	if body["doi"] != doi {
		t.Errorf("body.doi = %v", body["doi"])
	}
	if body["arxiv_id"] != nil {
		t.Errorf("body must not carry arxiv_id on the DOI surface: %v", body["arxiv_id"])
	}

	// Wait for the background job to finish, then re-GET → 200 bytes.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := converter.LookupDOI(doi); ok && job.State == mineru.JobStateDone {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if job, ok := converter.LookupDOI(doi); !ok || job.State != mineru.JobStateDone {
		final, _ := converter.LookupDOI(doi)
		t.Fatalf("job not done: %+v", final)
	}
	if *hits != 1 {
		t.Errorf("OA host hits = %d, want 1", *hits)
	}
	// The fetched PDF landed under the DOI layout with source metadata
	// recorded via the published path.
	if _, ok, _ := store.Stat(context.Background(), paperassets.DOIAssetKey("pdf", doi)); !ok {
		t.Errorf("DOI pdf not stored")
	}

	re2, rec2 := mustDOIMarkdownReq(t, doi)
	if err := getMarkdownByDOIHandler(re2, &config.Config{}, store, converter, doi, oaURL); err != nil {
		t.Fatalf("handler (cached): %v", err)
	}
	if rec2.Code != http.StatusOK {
		t.Fatalf("cached status = %d, want 200", rec2.Code)
	}
	if !strings.Contains(rec2.Body.String(), "# Hello DOI") {
		t.Errorf("markdown body = %q", rec2.Body.String())
	}
	if got := rec2.Header().Get("X-QAtlas-DOI"); got != doi {
		t.Errorf("X-QAtlas-DOI = %q", got)
	}

	// Status surface: cached now.
	re3, rec3 := mustDOIStatusReq(t, doi, "markdown")
	if err := markdownStatusByDOIHandler(re3, store, converter, doi); err != nil {
		t.Fatalf("status handler: %v", err)
	}
	var st map[string]any
	if err := json.Unmarshal(rec3.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	if st["state"] != "cached" || st["md_ready"] != true {
		t.Errorf("status = %+v, want cached/md_ready", st)
	}
}

// TestGetMarkdownByDOI_MissNoSource404: no stored PDF, no OA URL → the
// job fails with ErrNoDOISource and the handler renders 404 with the
// contrib-upload hint (not 502).
func TestGetMarkdownByDOI_MissNoSource404(t *testing.T) {
	doi := "10.1093/closed/access.12345"
	store := newDOIFlowStore()
	stub := newDOIMinerUStub(t)
	converter := newDOIFlowConverter(t, store, stub)

	// First call: job queued (202); the failure lands asynchronously.
	re, rec := mustDOIMarkdownReq(t, doi)
	if err := getMarkdownByDOIHandler(re, &config.Config{}, store, converter, doi, ""); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusAccepted {
		t.Fatalf("first call status = %d, want 202", rec.Code)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if job, ok := converter.LookupDOI(doi); ok && job.State == mineru.JobStateFailed {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Second call within the failure cooldown: 404 + contrib hint.
	re2, rec2 := mustDOIMarkdownReq(t, doi)
	if err := getMarkdownByDOIHandler(re2, &config.Config{}, store, converter, doi, ""); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec2.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec2.Code, rec2.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode 404 body: %v", err)
	}
	detail, _ := body["detail"].(string)
	if !strings.Contains(detail, "upload-pdf") {
		t.Errorf("detail = %q, want contrib-upload hint", detail)
	}

	// The status endpoint renders the failed job for pollers.
	re3, rec3 := mustDOIStatusReq(t, doi, "markdown")
	if err := markdownStatusByDOIHandler(re3, store, converter, doi); err != nil {
		t.Fatalf("status handler: %v", err)
	}
	var st map[string]any
	if err := json.Unmarshal(rec3.Body.Bytes(), &st); err != nil {
		t.Fatalf("decode status: %v", err)
	}
	// snapshotBody maps a cooling-down failure to state=cooldown.
	if st["state"] != "cooldown" && st["state"] != "failed" {
		t.Errorf("status state = %v, want cooldown|failed (%+v)", st["state"], st)
	}
}

// TestGetMarkdownByDOI_ConverterDisabledKeeps404: without MinerU tokens
// the miss branch can't self-serve — plain 404.
func TestGetMarkdownByDOI_ConverterDisabledKeeps404(t *testing.T) {
	doi := "10.1093/closed/access.12345"
	store := newDOIFlowStore()
	converter := mineru.NewConverter(mineru.ConverterConfig{
		PaperAccessEnabled: true, // no tokens → disabled
	}, store, nil, nil)

	re, rec := mustDOIMarkdownReq(t, doi)
	if err := getMarkdownByDOIHandler(re, &config.Config{}, store, converter, doi, "https://example.com/x.pdf"); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (body: %s)", rec.Code, rec.Body.String())
	}
}

// TestGetMarkdownByDOI_InvalidDOI400 keeps the pre-existing 400 on the
// new signature.
func TestGetMarkdownByDOI_InvalidDOI400(t *testing.T) {
	store := newDOIFlowStore()
	re, rec := mustDOIMarkdownReq(t, "not-a-doi")
	if err := getMarkdownByDOIHandler(re, &config.Config{}, store, nil, "not-a-doi", ""); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}
