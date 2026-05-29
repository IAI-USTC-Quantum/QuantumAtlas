package mineru

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// memStore is a tiny in-memory objstore.Store for converter tests. It
// honours IfNoneMatch="*" create-only semantics so putBytes exercises its
// real precondition path.
type memStore struct {
	mu      sync.Mutex
	objects map[string][]byte
	metas   map[string]map[string]string
}

func newMemStore() *memStore {
	return &memStore{objects: map[string][]byte{}, metas: map[string]map[string]string{}}
}

func (m *memStore) Put(ctx context.Context, key string, r io.Reader, size int64, ct string) (int64, error) {
	return m.PutWithOptions(ctx, key, r, size, objstore.PutOptions{ContentType: ct})
}
func (m *memStore) PutWithMeta(ctx context.Context, key string, r io.Reader, size int64, ct string, meta map[string]string) (int64, error) {
	return m.PutWithOptions(ctx, key, r, size, objstore.PutOptions{ContentType: ct, Metadata: meta})
}
func (m *memStore) PutWithOptions(_ context.Context, key string, r io.Reader, _ int64, opts objstore.PutOptions) (int64, error) {
	body, _ := io.ReadAll(r)
	m.mu.Lock()
	defer m.mu.Unlock()
	if opts.IfNoneMatch == "*" {
		if _, exists := m.objects[key]; exists {
			return 0, fmt.Errorf("memStore put %s: %w", key, objstore.ErrPreconditionFailed)
		}
	}
	m.objects[key] = body
	if opts.Metadata != nil {
		m.metas[key] = opts.Metadata
	}
	return int64(len(body)), nil
}
func (m *memStore) Get(_ context.Context, key string) (io.ReadCloser, objstore.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return nil, objstore.ObjectInfo{}, objstore.ErrNotFound
	}
	return io.NopCloser(strings.NewReader(string(b))), objstore.ObjectInfo{Key: key, Size: int64(len(b))}, nil
}
func (m *memStore) Stat(_ context.Context, key string) (objstore.ObjectInfo, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objects[key]
	if !ok {
		return objstore.ObjectInfo{}, false, nil
	}
	return objstore.ObjectInfo{Key: key, Size: int64(len(b)), Metadata: m.metas[key]}, true, nil
}
func (m *memStore) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}
func (m *memStore) ListPrefix(_ context.Context, prefix string, _ int) ([]objstore.ObjectInfo, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []objstore.ObjectInfo
	for k, b := range m.objects {
		if strings.HasPrefix(k, prefix) {
			out = append(out, objstore.ObjectInfo{Key: k, Size: int64(len(b))})
		}
	}
	return out, nil
}
func (m *memStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "", false, nil
}

func (m *memStore) has(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.objects[key]
	return ok
}

func baseConfig(baseURL string) *config.Config {
	return &config.Config{
		MinerUAPIToken:     "test-token",
		MinerUAPIBaseURL:   baseURL,
		MinerUModelVersion: "vlm",
		MinerULanguage:     "ch",
		MinerUPollInterval: 0.01,
		MinerUTimeout:      30,
		// Static share token avoids needing a real shares.Store.
		ShareAccessToken: "share-secret",
		PublicBaseURL:    "https://atlas.example.com",
	}
}

func TestConverterEnabled(t *testing.T) {
	if (&Converter{cfg: &config.Config{}}).Enabled() {
		t.Fatal("Enabled() true with no token")
	}
	if !(&Converter{cfg: &config.Config{MinerUAPIToken: "x"}}).Enabled() {
		t.Fatal("Enabled() false with token set")
	}
}

// minerUStub serves the submit + poll + zip-download flow. pollState controls
// what state the task reports.
func minerUStub(t *testing.T, md string, images map[string]string) *httptest.Server {
	t.Helper()
	zipBytes := buildResultZip(t, "result", md, images)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/extract/task", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":0,"data":{"task_id":"job-1"}}`))
	})
	var server *httptest.Server
	mux.HandleFunc("/api/v4/extract/task/job-1", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"code":0,"data":{"state":"done","full_zip_url":%q}}`, server.URL+"/zip")
	})
	mux.HandleFunc("/zip", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/zip")
		_, _ = w.Write(zipBytes)
	})
	server = httptest.NewServer(mux)
	return server
}

func TestConverterEnsureFullPipeline(t *testing.T) {
	const md = "# Hello\n\n![f](images/a.png)\n"
	srv := minerUStub(t, md, map[string]string{"images/a.png": "IMG"})
	defer srv.Close()

	store := newMemStore()
	canonical := "2501.00010v1"
	pdfKey := paperassets.AssetKey("pdf", canonical)
	_, _ = store.PutWithOptions(context.Background(), pdfKey, strings.NewReader("%PDF-1.4 fake"), -1, objstore.PutOptions{})

	conv := NewConverter(baseConfig(srv.URL), store, nil)

	job := conv.Ensure(canonical)
	if job.State != StateQueued && job.State != StateRunning {
		t.Fatalf("initial state = %q", job.State)
	}

	mdKey := paperassets.AssetKey("markdown", canonical)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if store.has(mdKey) {
			break
		}
		if time.Now().After(deadline) {
			snap := conv.Lookup(canonical)
			t.Fatalf("markdown not written before deadline; job=%+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}

	if got := string(store.objects[mdKey]); got != md {
		t.Fatalf("stored markdown = %q, want %q", got, md)
	}
	imgKey := paperassets.AssetKey("images", canonical) + "/a.png"
	if !store.has(imgKey) {
		t.Fatalf("image not stored at %q; keys=%v", imgKey, keysOf2(store))
	}

	// Job should converge to done.
	deadline = time.Now().Add(2 * time.Second)
	for {
		snap := conv.Lookup(canonical)
		if snap != nil && snap.State == StateDone {
			if snap.ImageCount != 1 {
				t.Fatalf("ImageCount = %d, want 1", snap.ImageCount)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not reach done: %+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConverterEnsureNoPDF(t *testing.T) {
	srv := minerUStub(t, "x", nil)
	defer srv.Close()
	store := newMemStore() // empty: no PDF
	conv := NewConverter(baseConfig(srv.URL), store, nil)

	conv.Ensure("2501.99999v1")
	// Wait for the background job to fail (no PDF).
	deadline := time.Now().Add(2 * time.Second)
	for {
		snap := conv.Lookup("2501.99999v1")
		if snap != nil && snap.State == StateFailed {
			if !strings.Contains(snap.Err, "no PDF") {
				t.Fatalf("fail reason = %q", snap.Err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("job did not fail; snap=%+v", snap)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestConverterEnsureDedup(t *testing.T) {
	// A converter with no MinerU server reachable; jobs will eventually fail,
	// but while queued/running two Ensure calls must share one job.
	store := newMemStore()
	canonical := "2501.00010v1"
	pdfKey := paperassets.AssetKey("pdf", canonical)
	_, _ = store.PutWithOptions(context.Background(), pdfKey, strings.NewReader("pdf"), -1, objstore.PutOptions{})

	conv := NewConverter(baseConfig("http://127.0.0.1:0"), store, nil)
	j1 := conv.Ensure(canonical)
	j2 := conv.Ensure(canonical)
	if !j1.StartedAt.Equal(j2.StartedAt) {
		t.Fatalf("dedup failed: started_at differ %v vs %v", j1.StartedAt, j2.StartedAt)
	}
}

func keysOf2(m *memStore) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]string, 0, len(m.objects))
	for k := range m.objects {
		out = append(out, k)
	}
	return out
}
