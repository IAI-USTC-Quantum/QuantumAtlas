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
	"errors"
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
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
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
func (s *doiFlowStore) GetRange(_ context.Context, key string, start, end int64) (io.ReadCloser, error) {
	s.mu.Lock()
	b, ok := s.objects[key]
	s.mu.Unlock()
	if !ok {
		return nil, objstore.ErrNotFound
	}
	if start >= int64(len(b)) {
		return io.NopCloser(bytes.NewReader(nil)), nil
	}
	if end >= int64(len(b)) {
		end = int64(len(b)) - 1
	}
	return io.NopCloser(bytes.NewReader(b[start : end+1])), nil
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

// DOI fixtures share the real complete-bundle reader helper, with DOI identity
// resolution and source origin overridden (no network or inference backend).
type doiReadingCatalog struct {
	*readTestCatalog
	doi         string
	omitSources bool
}

func (c *doiReadingCatalog) Get(ctx context.Context, id string) (*registry.Paper, bool, error) {
	p, ok, err := c.readTestCatalog.Get(ctx, id)
	if p != nil {
		v := *p
		v.DOI = c.doi
		v.ArxivID = ""
		p = &v
	}
	return p, ok, err
}
func (c *doiReadingCatalog) GetWithAssets(ctx context.Context, id string) (*registry.PaperDetail, bool, error) {
	p, ok, err := c.Get(ctx, id)
	return &registry.PaperDetail{Paper: p}, ok, err
}
func (c *doiReadingCatalog) GetPaperIDByIdentity(ctx context.Context, scheme, id string) (string, bool, error) {
	if scheme == "doi" && registry.NormalizeDOI(id) == c.doi {
		return readTestPaper, true, nil
	}
	return c.readTestCatalog.GetPaperIDByIdentity(ctx, scheme, id)
}
func (c *doiReadingCatalog) ListPaperSources(ctx context.Context, id string) ([]registry.PaperSource, error) {
	if c.omitSources {
		return nil, nil
	}
	return c.readTestCatalog.ListPaperSources(ctx, id)
}
func (c *doiReadingCatalog) GetPaperSource(ctx context.Context, paper, id string) (registry.PaperSource, bool, error) {
	if c.omitSources {
		return registry.PaperSource{}, false, nil
	}
	return c.readTestCatalog.GetPaperSource(ctx, paper, id)
}
func newDOIReadingFixture(t *testing.T) (*doiReadingCatalog, objstore.Store, registry.ParseBundle) {
	t.Helper()
	c, s, b := newReadingFixture(t)
	doi := "10.1038/s41534-020-00001-0"
	c.source.Origin = "doi:" + doi
	return &doiReadingCatalog{readTestCatalog: c, doi: doi}, s, b
}
func callDOIDerivative(t *testing.T, c *doiReadingCatalog, s objstore.Store, conv contentReadConverter, kind string) (*httptest.ResponseRecorder, map[string]any) {
	return readRouteRequest(t, "/api/papers/"+c.doi+"/"+kind, func(re *core.RequestEvent) error {
		return contentDerivativeHandler(re, &config.Config{PaperAccessEnabled: true}, s, c, conv, c.doi, kind)
	})
}

func TestGetMarkdownByDOI_MissTriggersFetchConvertLRO(t *testing.T) {
	c, s, _ := newDOIReadingFixture(t)
	c.bundles = map[string]registry.ParseBundle{}
	legacy := []byte("FORBIDDEN legacy DOI markdown")
	_, _ = s.Put(t.Context(), paperassets.DOIAssetKey("markdown", c.doi), bytes.NewReader(legacy), int64(len(legacy)), "text/markdown")
	conv := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued, RevisionID: "pr_pending"}}
	rec, body := callDOIDerivative(t, c, s, conv, "markdown")
	if rec.Code != 202 || body["ready"] != false || conv.ensureCalls != 1 {
		t.Fatalf("cold DOI %d %+v", rec.Code, body)
	}
	if rec.Header().Get("Operation-Location") != "/api/papers/"+readTestPaper+"/read/status?source_id="+readTestSource {
		t.Fatal("status location not source pinned")
	}
	ready := writeReadingBundle(t, c.readTestCatalog, s, "pr_doiready", "HELLO DOI native Middle")
	c.current = ready.RevisionID
	rec, _ = callDOIDerivative(t, c, s, conv, "markdown")
	if rec.Code != 200 || rec.Body.String() != "original parser markdown" || conv.ensureCalls != 1 {
		t.Fatalf("ready DOI %d %s", rec.Code, rec.Body.String())
	}
	poll, b := readRouteRequest(t, "/api/papers/"+c.doi+"/read/status?source_id="+readTestSource, func(re *core.RequestEvent) error {
		return contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, s, c, conv, c.doi)
	})
	if poll.Code != 200 || b["ready"] != true || b["md_ready"] != true || b["revision"] != ready.RevisionID {
		t.Fatalf("verified DOI status %d %+v", poll.Code, b)
	}
}
func TestGetMarkdownByDOI_MissNoSource404(t *testing.T) {
	c, s, _ := newDOIReadingFixture(t)
	c.omitSources = true
	c.bundles = map[string]registry.ParseBundle{}
	conv := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}
	rec, _ := callDOIDerivative(t, c, s, conv, "markdown")
	if rec.Code != 404 || conv.ensureCalls != 0 {
		t.Fatalf("missing PDF inferred/fabricated source %d", rec.Code)
	}
}
func TestGetMarkdownByDOI_ConverterDisabledKeeps404(t *testing.T) {
	c, s, _ := newDOIReadingFixture(t)
	c.bundles = map[string]registry.ParseBundle{}
	rec, _ := callDOIDerivative(t, c, s, nil, "markdown")
	if rec.Code != 503 {
		t.Fatalf("known frozen PDF without parser should expose capability gap: %d", rec.Code)
	}
	// The master switch is separate from parser availability and gates bytes.
	rec, _ = readRouteRequest(t, "/api/papers/"+c.doi+"/markdown", func(re *core.RequestEvent) error {
		return contentDerivativeHandler(re, &config.Config{}, s, c, nil, c.doi, "markdown")
	})
	if rec.Code != 404 {
		t.Fatal("disabled master leaked bytes")
	}
}
func TestGetMarkdownByDOI_InvalidDOI400(t *testing.T) {
	c, s, _ := newDOIReadingFixture(t)
	conv := &readTestConverter{job: &mineru.Job{State: mineru.JobStateQueued}}
	rec, _ := readRouteRequest(t, "/api/papers/not-a-doi/read", func(re *core.RequestEvent) error {
		return contentReadHandler(re, &config.Config{PaperAccessEnabled: true}, s, c, conv, "not-a-doi")
	})
	if rec.Code != 400 || conv.ensureCalls != 0 {
		t.Fatalf("bad DOI reached acquisition: %d", rec.Code)
	}
}
func TestDOIContentFailureRemainsSourcePinned(t *testing.T) {
	c, s, _ := newDOIReadingFixture(t)
	c.bundles = map[string]registry.ParseBundle{}
	conv := &readTestConverter{job: &mineru.Job{State: mineru.JobStateFailed, Err: errors.New("unsupported source PDF"), ErrKind: mineru.ErrFatal}}
	rec, body := callDOIDerivative(t, c, s, conv, "markdown")
	if rec.Code != 503 || body["state"] != "failed" || body["ready"] != false || body["source_id"] != readTestSource {
		t.Fatalf("fatal source parse %d %+v", rec.Code, body)
	}
}
