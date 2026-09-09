package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/router"
)

func TestFleetUploadDoesNotBufferPocketBaseReplay(t *testing.T) {
	raw := http.NoBody
	re := &core.RequestEvent{}
	re.Request = httptest.NewRequest(http.MethodPut, wp.UploadPath+"1234567890123456", nil)
	re.Request.Body = &router.RereadableReadCloser{ReadCloser: raw}
	if err := streamWorkerUpload(re); err != nil {
		t.Fatal(err)
	}
	if re.Request.Body != raw {
		t.Fatal("PDF upload would still be cached in the PocketBase replay buffer")
	}
}

func newFleetRouteHarness(t *testing.T) *adminHarness {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Cleanup)
	h := &adminHarness{patHarness: &patHarness{t: t, app: app}, cfg: &config.Config{AdminGitHubLogins: []string{adminTestLogin}}}
	enforcer, err := pat.NewEnforcer()
	if err != nil {
		t.Fatal(err)
	}
	router, err := apis.NewRouter(app)
	if err != nil {
		t.Fatal(err)
	}
	se := &core.ServeEvent{}
	se.App = app
	se.Router = router
	if err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterDownloadFleet(e, h.cfg, nil, enforcer)
		var err error
		h.mux, err = e.Router.BuildMux()
		return err
	}); err != nil {
		t.Fatal(err)
	}
	return h
}
func TestFleetAdminGatesAndDisabledStatus(t *testing.T) {
	h := newFleetRouteHarness(t)
	regular := h.sessionToken()
	admin := h.adminSessionToken()
	for _, path := range []string{"/api/admin/downloader/workers", "/api/admin/downloader/enrollment", "/api/admin/downloader/workers/worker-a/approve"} {
		method := http.MethodPost
		if path == "/api/admin/downloader/workers" {
			method = http.MethodGet
		}
		if status, _, _ := h.do(method, path, "{}", nil); status != http.StatusUnauthorized {
			t.Fatalf("anonymous %s: %d", path, status)
		}
		if status, _, _ := h.do(method, path, "{}", rawHeader(regular)); status != http.StatusForbidden {
			t.Fatalf("nonadmin %s: %d", path, status)
		}
		if status, _, _ := h.do(method, path, "{}", rawHeader(admin)); status != http.StatusServiceUnavailable {
			t.Fatalf("disabled admin %s: %d", path, status)
		}
	}
	if status, _, _ := h.do(http.MethodGet, "/api/downloader/remote-jobs", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("anonymous jobs: %d", status)
	}
	if status, _, body := h.do(http.MethodGet, "/api/downloader/remote-jobs", "", rawHeader(regular)); status != http.StatusOK || body["enabled"] != false {
		t.Fatalf("disabled remote jobs: %d %+v", status, body)
	}
}
func TestFleetUploadOverridesPocketBase32MiBLimit(t *testing.T) {
	h := newFleetRouteHarness(t)
	for _, tc := range []struct {
		size int64
		want int
	}{{33 << 20, http.StatusServiceUnavailable}, {101 << 20, http.StatusRequestEntityTooLarge}} {
		req := httptest.NewRequest(http.MethodPut, wp.UploadPath+"1234567890123456", http.NoBody)
		req.ContentLength = tc.size
		rec := httptest.NewRecorder()
		h.mux.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Fatalf("size=%d code=%d want=%d body=%s", tc.size, rec.Code, tc.want, rec.Body.String())
		}
	}
}
