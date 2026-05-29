package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/pocketbase/pocketbase/core"
)

func TestSplitMarkdownStatus(t *testing.T) {
	cases := []struct {
		raw     string
		wantID  string
		wantOK  bool
	}{
		{"2501.00010v1/markdown/status", "2501.00010v1", true},
		{"/2501.00010v1/markdown/status/", "2501.00010v1", true},
		{"old/style/id/markdown/status", "old/style/id", true},
		{"2501.00010v1/markdown", "", false},
		{"2501.00010v1/resources", "", false},
		{"2501.00010v1/markdown/other", "", false},
		{"", "", false},
	}
	for _, c := range cases {
		gotID, gotOK := splitMarkdownStatus(c.raw)
		if gotID != c.wantID || gotOK != c.wantOK {
			t.Errorf("splitMarkdownStatus(%q) = (%q,%v), want (%q,%v)",
				c.raw, gotID, gotOK, c.wantID, c.wantOK)
		}
	}
}

// newStatusRequest invokes markdownStatusHandler and returns the recorder.
func invokeStatus(t *testing.T, store objstore.Store, conv *mineru.Converter, arxivID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/"+arxivID+"/markdown/status", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := markdownStatusHandler(re, store, conv, arxivID); err != nil {
		t.Fatalf("markdownStatusHandler: %v", err)
	}
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode body: %v (raw=%s)", err, rec.Body.String())
		}
	}
	return body
}

func TestMarkdownStatusHandler_CacheHitDone(t *testing.T) {
	const arxivID = "2501.00010v1"
	store := newTempLocalStore(t)
	seedObject(t, store, paperassets.AssetKey("markdown", arxivID), []byte("# hi\n"), "text/markdown")

	// converter nil: cache hit must still report done.
	rec := invokeStatus(t, store, nil, arxivID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	body := decodeBody(t, rec)
	if body["status"] != "done" {
		t.Errorf("status = %v, want done", body["status"])
	}
	if body["markdown_url"] != "/api/papers/2501.00010v1/markdown" {
		t.Errorf("markdown_url = %v", body["markdown_url"])
	}
}

func TestMarkdownStatusHandler_UnavailableWhenNotConfigured(t *testing.T) {
	store := newTempLocalStore(t)
	rec := invokeStatus(t, store, nil, "2501.00010v1") // no converter, no cache
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	if got := decodeBody(t, rec)["status"]; got != "unavailable" {
		t.Errorf("status = %v, want unavailable", got)
	}
}

func TestMarkdownStatusHandler_NotStartedWhenEnabledNoJob(t *testing.T) {
	store := newTempLocalStore(t)
	cfg := &config.Config{MinerUAPIToken: "msk_test", MinerUAPIBaseURL: "http://127.0.0.1:1"}
	conv := mineru.NewConverter(cfg, store, nil)
	rec := invokeStatus(t, store, conv, "2501.00010v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status code = %d, want 200", rec.Code)
	}
	if got := decodeBody(t, rec)["status"]; got != "not_started" {
		t.Errorf("status = %v, want not_started", got)
	}
}

func TestMarkdownStatusHandler_BadID(t *testing.T) {
	store := newTempLocalStore(t)
	rec := invokeStatus(t, store, nil, "not-an-arxiv-id")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status code = %d, want 400", rec.Code)
	}
}

// TestMarkdownHandler_202Headers checks that a miss on an enabled converter
// returns 202 with Operation-Location + Retry-After and starts a (doomed,
// connection-refused) background job that doesn't block the response.
func TestMarkdownHandler_202Headers(t *testing.T) {
	const arxivID = "2501.00011v1"
	store := newTempLocalStore(t)
	seedObject(t, store, paperassets.AssetKey("pdf", arxivID), []byte("%PDF-1.4\n"), "application/pdf")

	// Block the MinerU submit so the background job stays in
	// queued/running long enough for the handler to observe it and
	// return 202 (a refused dial could fail before we re-check state).
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)

	cfg := &config.Config{
		MinerUAPIToken:     "msk_test",
		MinerUAPIBaseURL:   srv.URL,
		MinerUTimeout:      1,
		MinerUPollInterval: 1,
		// Static share token so buildPDFURL doesn't need a shareStore.
		ShareAccessToken: "share_tok",
		PublicBaseURL:    "https://server",
	}
	conv := mineru.NewConverter(cfg, store, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/papers/"+arxivID+"/markdown", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := markdownHandler(re, store, conv, arxivID); err != nil {
		t.Fatalf("markdownHandler: %v", err)
	}

	if rec.Code != http.StatusAccepted {
		t.Fatalf("status code = %d, want 202; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Operation-Location"); got != "/api/papers/2501.00011v1/markdown/status" {
		t.Errorf("Operation-Location = %q", got)
	}
	if got := rec.Header().Get("Retry-After"); got == "" {
		t.Error("Retry-After header missing")
	}
	body := decodeBody(t, rec)
	if body["status"] != "processing" {
		t.Errorf("status = %v, want processing", body["status"])
	}
	if body["status_url"] != "/api/papers/2501.00011v1/markdown/status" {
		t.Errorf("status_url = %v", body["status_url"])
	}
}

// --- test helpers ---

func newTempLocalStore(t *testing.T) objstore.Store {
	t.Helper()
	store, err := objstore.NewLocalStore(filepath.Join(t.TempDir(), "raw"))
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	return store
}

func seedObject(t *testing.T, store objstore.Store, key string, data []byte, contentType string) {
	t.Helper()
	if _, err := store.Put(context.Background(), key, bytes.NewReader(data), int64(len(data)), contentType); err != nil {
		t.Fatalf("seed %q: %v", key, err)
	}
}
