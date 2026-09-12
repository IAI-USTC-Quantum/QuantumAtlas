package routes

// HTTP-layer tests for the qatlas-rag proxy routes (POST /api/rag/
// retrieve + /api/rag/evidence). Same harness shape as the agentic
// search tests: a real PB test app, the proxy mounted over OnServe, a
// session token to clear the guards, and an httptest stand-in for the
// microservice.

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/rag"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// newRagProxyHarness mounts RegisterRagProxy with the given client (nil
// = rag.remote disabled) over a real test PB app.
func newRagProxyHarness(t testing.TB, client *rag.RemoteClient) *patHarness {
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
		RegisterRagProxy(e, client, enforcer)
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

func TestAPI_RagProxy_RejectsAnonymous(t *testing.T) {
	h := newRagProxyHarness(t, nil)
	for _, path := range []string{"/api/rag/retrieve", "/api/rag/evidence"} {
		status, _, _ := h.do(http.MethodPost, path, `{"q":"x"}`, nil)
		if status != http.StatusUnauthorized {
			t.Errorf("POST %s: status = %d, want 401", path, status)
		}
	}
}

func TestAPI_RagProxy_NotConfigured(t *testing.T) {
	h := newRagProxyHarness(t, nil)
	auth := rawHeader(h.sessionToken())
	for _, path := range []string{"/api/rag/retrieve", "/api/rag/evidence"} {
		status, _, body := h.do(http.MethodPost, path, `{"q":"x"}`, auth)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("POST %s: status = %d, want 503; body=%v", path, status, body)
		}
		if asString(body["detail"]) != "rag service not configured" {
			t.Errorf("POST %s: detail = %q, want the not-configured message", path, body["detail"])
		}
	}
}

func TestAPI_RagProxy_Passthrough(t *testing.T) {
	var gotPath, gotAuth, gotBody string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		raw, _ := io.ReadAll(r.Body)
		gotBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"hits":[{"paper_id":"2401.12345","score":0.9}]}`))
	}))
	t.Cleanup(fake.Close)

	h := newRagProxyHarness(t, rag.NewRemoteClient(fake.URL, "rag-svc-token", 5*time.Second))
	auth := rawHeader(h.sessionToken())
	status, raw, _ := h.do(http.MethodPost, "/api/rag/retrieve", `{"query":"surface code","top_k":5}`, auth)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if gotPath != "/v1/retrieve" {
		t.Errorf("upstream path = %q, want /v1/retrieve", gotPath)
	}
	if gotAuth != "Bearer rag-svc-token" {
		t.Errorf("upstream Authorization = %q, want the relayed bearer", gotAuth)
	}
	if gotBody != `{"query":"surface code","top_k":5}` {
		t.Errorf("upstream body = %q, want verbatim relay", gotBody)
	}
	if !strings.Contains(string(raw), "2401.12345") {
		t.Errorf("body = %q, want passthrough of the upstream reply", string(raw))
	}
}

func TestAPI_RagProxy_UpstreamErrorForwarded(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"detail":"index not built for paper"}`))
	}))
	t.Cleanup(fake.Close)

	h := newRagProxyHarness(t, rag.NewRemoteClient(fake.URL, "", 5*time.Second))
	auth := rawHeader(h.sessionToken())
	status, raw, _ := h.do(http.MethodPost, "/api/rag/evidence", `{"claim_id":"x"}`, auth)
	if status != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want forwarded 422", status)
	}
	if !strings.Contains(string(raw), "index not built") {
		t.Errorf("body = %q, want the upstream error body forwarded", string(raw))
	}
}

func TestAPI_RagProxy_Unreachable(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	dead.Close()

	h := newRagProxyHarness(t, rag.NewRemoteClient(dead.URL, "", 2*time.Second))
	auth := rawHeader(h.sessionToken())
	status, _, body := h.do(http.MethodPost, "/api/rag/retrieve", `{"q":"x"}`, auth)
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", status)
	}
	if detail := asString(body["detail"]); !strings.Contains(detail, "rag unreachable") {
		t.Errorf("detail = %q, want the unreachable message", detail)
	}
}

func TestAPI_RagProxy_BodyTooLarge(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(fake.Close)

	h := newRagProxyHarness(t, rag.NewRemoteClient(fake.URL, "", 5*time.Second))
	auth := rawHeader(h.sessionToken())
	big := `{"q":"` + strings.Repeat("x", ragProxyMaxBody) + `"}`
	status, _, body := h.do(http.MethodPost, "/api/rag/retrieve", big, auth)
	if status != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", status)
	}
	if detail := asString(body["detail"]); !strings.Contains(detail, "64 KiB") {
		t.Errorf("detail = %q, want the size-cap message", detail)
	}
}
