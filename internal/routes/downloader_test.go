// HTTP-layer tests for the downloader surface: POST /api/downloader/fetch
// + GET /api/downloader/jobs, with a fake Downloader + minter.

package routes

import (
	"context"
	"net/http"
	"sync"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

type fakeDownloader struct {
	mu       sync.Mutex
	enqueued int
	snapshot []downloader.Progress
}

func (f *fakeDownloader) Enqueue(ctx context.Context, paperID, input string, kind downloader.IdentifierKind, ref registry.PaperRef) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.enqueued++
	return true
}

func (f *fakeDownloader) Snapshot() []downloader.Progress {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snapshot
}

func (f *fakeDownloader) SnapshotCounters() map[string]int64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return map[string]int64{"queued": int64(f.enqueued)}
}

type fakeMinter struct{}

func (fakeMinter) ResolveOrMint(ctx context.Context, ref registry.PaperRef) (string, bool, error) {
	if ref.DOI != "" {
		return "qa_01DOI", ref.DOI == "10.1000/new", nil
	}
	return "qa_01ARXIV", false, nil
}

func newDownloaderHarness(t testing.TB, dl Downloader) *patHarness {
	t.Helper()
	h := &patHarness{t: t}
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	h.app = app

	enforcer, err := pat.NewEnforcer()
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}
	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterDownloader(e, dl, fakeMinter{}, enforcer)
		m, mErr := e.Router.BuildMux()
		if mErr != nil {
			return mErr
		}
		built = m
		return nil
	})
	if err != nil {
		t.Fatalf("OnServe trigger: %v", err)
	}
	h.mux = built
	return h
}

func TestAPI_Downloader_RejectsAnonymous(t *testing.T) {
	h := newDownloaderHarness(t, &fakeDownloader{})
	status, _, _ := h.do(http.MethodPost, "/api/downloader/fetch", `{"items":["10.1000/x"]}`, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	status, _, _ = h.do(http.MethodGet, "/api/downloader/jobs", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("jobs status = %d, want 401", status)
	}
}

func TestAPI_Downloader_NilBackendIs503(t *testing.T) {
	h := newDownloaderHarness(t, nil)
	status, _, body := h.do(http.MethodPost, "/api/downloader/fetch", `{"items":["10.1000/x"]}`, rawHeader(h.sessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; body=%v", status, body)
	}
	status, _, body = h.do(http.MethodGet, "/api/downloader/jobs", "", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("jobs on nil backend: status = %d; body=%v", status, body)
	}
}

func TestAPI_Downloader_FetchParsesAndEnqueues(t *testing.T) {
	dl := &fakeDownloader{}
	h := newDownloaderHarness(t, dl)
	body := `{"items":["10.1000/new","arXiv:2401.12345","https://doi.org/10.1000/x","garbage input"]}`
	status, _, resp := h.do(http.MethodPost, "/api/downloader/fetch", body, rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d; resp=%v", status, resp)
	}
	items := resp["items"].([]any)
	if len(items) != 4 {
		t.Fatalf("items = %v", items)
	}
	first := items[0].(map[string]any)
	if first["kind"] != "doi" || first["paper_id"] != "qa_01DOI" || first["created"] != true {
		t.Errorf("first = %v", first)
	}
	second := items[1].(map[string]any)
	if second["kind"] != "arxiv" || second["paper_id"] != "qa_01ARXIV" {
		t.Errorf("second = %v", second)
	}
	third := items[2].(map[string]any)
	if third["kind"] != "url" && third["kind"] != "doi" {
		t.Errorf("third = %v", third)
	}
	fourth := items[3].(map[string]any)
	if fourth["kind"] != "invalid" || fourth["error"] == "" {
		t.Errorf("fourth = %v", fourth)
	}
	if resp["enqueued"] != float64(3) {
		t.Errorf("enqueued = %v, want 3", resp["enqueued"])
	}
}

func TestAPI_Downloader_FetchValidation(t *testing.T) {
	h := newDownloaderHarness(t, &fakeDownloader{})
	hdr := rawHeader(h.sessionToken())
	if status, _, _ := h.do(http.MethodPost, "/api/downloader/fetch", `{}`, hdr); status != http.StatusBadRequest {
		t.Errorf("empty items: status = %d", status)
	}
	tooMany := `{"items":[`
	for i := 0; i < 60; i++ {
		if i > 0 {
			tooMany += ","
		}
		tooMany += `"10.1000/x"`
	}
	tooMany += `}`
	if status, _, _ := h.do(http.MethodPost, "/api/downloader/fetch", tooMany, hdr); status != http.StatusBadRequest {
		t.Errorf("60 items: status = %d", status)
	}
}

func TestAPI_Downloader_JobsSnapshot(t *testing.T) {
	dl := &fakeDownloader{snapshot: []downloader.Progress{{
		PaperID: "qa_01DOI", State: "done", Strategy: "oa:unpaywall",
		Trace: []downloader.Attempt{{Strategy: "oa:unpaywall", URL: "https://x/p.pdf"}},
	}}}
	h := newDownloaderHarness(t, dl)
	status, _, resp := h.do(http.MethodGet, "/api/downloader/jobs", "", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	jobs := resp["jobs"].([]any)
	if len(jobs) != 1 {
		t.Fatalf("jobs = %v", jobs)
	}
	job := jobs[0].(map[string]any)
	if job["strategy"] != "oa:unpaywall" || job["state"] != "done" {
		t.Errorf("job = %v", job)
	}
}
