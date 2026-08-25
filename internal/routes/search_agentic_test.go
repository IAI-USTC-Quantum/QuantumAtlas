// HTTP-layer tests for POST /api/search/agentic. The harness mirrors
// patHarness (see pat_test.go): one mux built from OnServe, the do()
// helper for repeated requests. What's exercised:
//
//   - registration smoke: mounting the route must not panic
//   - authGuard underneath: anonymous → 401
//   - remote==nil (search.remote disabled) → 503 "not configured"
//   - system PAT (re.Auth == nil) → 403 "user-bound credential"
//
// The happy path needs a live Postgres (usage.Store over a real pool);
// it is covered by the integration-gated usage tests instead.

package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// agenticHarness mounts POST /api/search/agentic with the given backend
// (nil = no agentic backend configured) over a real test PB app.
// usageStore is nil-pool on purpose: the 503/403 branches under test
// never reach it.
type agenticHarness struct {
	*patHarness
}

func newAgenticHarness(t testing.TB, backend AgenticBackend) *agenticHarness {
	t.Helper()
	h := &agenticHarness{patHarness: &patHarness{t: t}}

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
		RegisterSearchAgentic(e, &config.Config{}, backend, usage.NewStore(nil), search.NewEngine(nil, nil), enforcer)
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
	if built == nil {
		t.Fatal("mux not built by OnServe trigger")
	}
	h.mux = built
	return h
}

func TestAPI_SearchAgentic_RejectsAnonymous(t *testing.T) {
	h := newAgenticHarness(t, nil)
	status, _, _ := h.do(http.MethodPost, "/api/search/agentic", `{"text":"x"}`, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestAPI_SearchAgentic_RemoteNotConfigured(t *testing.T) {
	h := newAgenticHarness(t, nil)
	status, _, body := h.do(http.MethodPost, "/api/search/agentic", `{"text":"x"}`, rawHeader(h.sessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%v", status, body)
	}
	if asString(body["detail"]) != "agentic search service not configured" {
		t.Errorf("detail = %q", body["detail"])
	}
}

func TestAPI_SearchAgentic_RejectsSystemPAT(t *testing.T) {
	// A non-nil remote (pointing at a throwaway httptest server) so the
	// flow reaches the re.Auth==nil check.
	fakeRemote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fakeRemote.Close)
	remote := search.NewRemoteProvider(fakeRemote.URL, "", 5*time.Second)

	const secret = "agentic-system-pat-test-secret-long-enough"
	sysPAT, err := pat.LoadSystemPAT(secret, []string{"*"})
	if err != nil {
		t.Fatalf("LoadSystemPAT: %v", err)
	}
	UseSystemPAT(sysPAT)
	t.Cleanup(func() { UseSystemPAT(nil) })

	h := newAgenticHarness(t, remote)
	status, _, body := h.do(http.MethodPost, "/api/search/agentic", `{"text":"x"}`, bearerHeader(secret))
	if status != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "user-bound credential") {
		t.Errorf("detail = %q, want mention of user-bound credential", body["detail"])
	}
}

func TestAPI_SearchAgentic_CatalogUnavailable(t *testing.T) {
	// Session caller clears the guards; the nil-pool usage store reports
	// ErrCatalogUnavailable → the route's 503 convention.
	fakeRemote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fakeRemote.Close)
	remote := search.NewRemoteProvider(fakeRemote.URL, "", 5*time.Second)

	h := newAgenticHarness(t, remote)
	status, _, body := h.do(http.MethodPost, "/api/search/agentic", `{"text":"x"}`, rawHeader(h.sessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nil pool); body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "catalog unavailable") {
		t.Errorf("detail = %q, want catalog-unavailable convention", body["detail"])
	}
}

// fakeAgenticBackend stands in for the local claude-CLI runner
// (internal/agentic.Runner) to prove the route accepts any
// AgenticBackend, not just *search.RemoteProvider.
type fakeAgenticBackend struct {
	called bool
}

func (f *fakeAgenticBackend) SearchAgentic(_ context.Context, _ search.SearchEntry, _ bool) (search.RemoteResponse, error) {
	f.called = true
	return search.RemoteResponse{}, nil
}

func TestAPI_SearchAgentic_LocalBackendWiring(t *testing.T) {
	// A fake local backend mounts exactly like the remote provider; the
	// nil-pool usage store still short-circuits to the 503 convention
	// BEFORE the backend is invoked (metering precedes the search call).
	fake := &fakeAgenticBackend{}
	h := newAgenticHarness(t, fake)
	status, _, body := h.do(http.MethodPost, "/api/search/agentic", `{"text":"x"}`, rawHeader(h.sessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nil pool); body=%v", status, body)
	}
	if fake.called {
		t.Error("backend invoked before metering passed, want untouched")
	}
}
