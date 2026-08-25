// HTTP-layer tests for the /api/admin/plugins surface
// (admin_plugins.go).
//
//   - adminGuard contract: anonymous 401, non-admin session 403
//   - GET /api/admin/plugins returns {plugins: [...]} in the same
//     Summary JSON shape as /api/v1/plugins
//   - manifest/config proxy: only id=search-remote has an admin page
//     (others 404); nil provider 503s; with an httptest.Server standing
//     in for the qatlas-search microservice we verify the Bearer token,
//     the verbatim body/config passthrough, and the non-2xx → 502
//     mapping with upstream detail forwarding
package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
)

// fakeSearchRemote records what the proxy forwarded so the test can
// assert method/path/auth/body, and replies with canned payloads.
type fakeSearchRemote struct {
	mu         sync.Mutex
	auth       string
	method     string
	path       string
	body       string
	statusCode int
	payload    string
}

func (f *fakeSearchRemote) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.auth = r.Header.Get("Authorization")
		f.method = r.Method
		f.path = r.URL.Path
		f.body = string(raw)
		status, payload := f.statusCode, f.payload
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(payload))
	})
}

func (f *fakeSearchRemote) snapshot() (auth, method, path, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.auth, f.method, f.path, f.body
}

func TestAPI_Admin_PluginsGate(t *testing.T) {
	h := newAdminHarness(t)

	status, _, _ := h.do(http.MethodGet, "/api/admin/plugins", "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", status)
	}

	status, _, body := h.do(http.MethodGet, "/api/admin/plugins", "", rawHeader(h.sessionToken()))
	if status != http.StatusForbidden {
		t.Fatalf("non-admin: status = %d, want 403; body=%v", status, body)
	}
	if asString(body["detail"]) != "admin only" {
		t.Errorf("detail = %q, want %q", body["detail"], "admin only")
	}
}

func TestAPI_Admin_PluginsList(t *testing.T) {
	h := newAdminHarnessWith(t, qplugin.NewBuiltinRegistry(qplugin.Options{}), nil)
	status, _, body := h.do(http.MethodGet, "/api/admin/plugins", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	plugins, ok := body["plugins"].([]any)
	if !ok {
		t.Fatalf("plugins key missing or wrong type: %v", body)
	}
	// Every entry must carry the Summary contract keys the SPA reads
	// (same JSON shape as /api/v1/plugins).
	for _, p := range plugins {
		entry, ok := p.(map[string]any)
		if !ok {
			t.Fatalf("plugin entry wrong type: %v", p)
		}
		for _, key := range []string{"id", "status", "enabled"} {
			if _, ok := entry[key]; !ok {
				t.Errorf("plugin entry missing key %q: %v", key, entry)
			}
		}
	}
}

func TestAPI_Admin_PluginsListNilRegistry(t *testing.T) {
	h := newAdminHarnessWith(t, nil, nil)
	status, _, body := h.do(http.MethodGet, "/api/admin/plugins", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	plugins, ok := body["plugins"].([]any)
	if !ok || len(plugins) != 0 {
		t.Errorf("nil registry should render an empty list; got %v", body["plugins"])
	}
}

func TestAPI_Admin_PluginUnknownID(t *testing.T) {
	h := newAdminHarnessWith(t, nil, nil)
	tok := rawHeader(h.adminSessionToken())
	for _, ep := range []struct{ method, url string }{
		{http.MethodGet, "/api/admin/plugins/lean-content/manifest"},
		{http.MethodPut, "/api/admin/plugins/lean-content/config"},
	} {
		status, _, body := h.do(ep.method, ep.url, `{}`, tok)
		if status != http.StatusNotFound {
			t.Errorf("%s %s: status = %d, want 404; body=%v", ep.method, ep.url, status, body)
		}
		if asString(body["detail"]) != "plugin has no admin page" {
			t.Errorf("%s %s: detail = %q, want %q", ep.method, ep.url, body["detail"], "plugin has no admin page")
		}
	}
}

func TestAPI_Admin_PluginProxyNilRemote(t *testing.T) {
	h := newAdminHarnessWith(t, nil, nil)
	status, _, body := h.do(http.MethodGet, "/api/admin/plugins/search-remote/manifest", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nil remote); body=%v", status, body)
	}
	if asString(body["detail"]) != "search remote unavailable" {
		t.Errorf("detail = %q, want %q", body["detail"], "search remote unavailable")
	}
}

func TestAPI_Admin_PluginManifestPassthrough(t *testing.T) {
	fake := &fakeSearchRemote{statusCode: http.StatusOK, payload: `{"name":"qatlas-search","version":"1.2.3"}`}
	upstream := httptest.NewServer(fake.handler())
	defer upstream.Close()

	remote := search.NewRemoteProvider(upstream.URL, "test-token", 5*time.Second)
	h := newAdminHarnessWith(t, nil, remote)

	status, raw, _ := h.do(http.MethodGet, "/api/admin/plugins/search-remote/manifest", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, raw)
	}
	if string(raw) != fake.payload {
		t.Errorf("response body not passed through verbatim: got %s, want %s", raw, fake.payload)
	}
	auth, method, path, _ := fake.snapshot()
	if auth != "Bearer test-token" {
		t.Errorf("upstream Authorization = %q, want %q", auth, "Bearer test-token")
	}
	if method != http.MethodGet || path != "/v1/admin/manifest" {
		t.Errorf("upstream request = %s %s, want GET /v1/admin/manifest", method, path)
	}
}

func TestAPI_Admin_PluginConfigPutPassthrough(t *testing.T) {
	fake := &fakeSearchRemote{statusCode: http.StatusOK, payload: `{"applied":true}`}
	upstream := httptest.NewServer(fake.handler())
	defer upstream.Close()

	remote := search.NewRemoteProvider(upstream.URL, "test-token", 5*time.Second)
	h := newAdminHarnessWith(t, nil, remote)

	reqBody := `{"backends":["arxiv","openalex"],"max_results":25}`
	status, raw, _ := h.do(http.MethodPut, "/api/admin/plugins/search-remote/config", reqBody, rawHeader(h.adminSessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, raw)
	}
	if string(raw) != fake.payload {
		t.Errorf("response body not passed through verbatim: got %s, want %s", raw, fake.payload)
	}
	auth, method, path, body := fake.snapshot()
	if auth != "Bearer test-token" {
		t.Errorf("upstream Authorization = %q, want %q", auth, "Bearer test-token")
	}
	if method != http.MethodPut || path != "/v1/admin/config" {
		t.Errorf("upstream request = %s %s, want PUT /v1/admin/config", method, path)
	}
	if body != reqBody {
		t.Errorf("upstream body = %q, want verbatim %q", body, reqBody)
	}
}

func TestAPI_Admin_PluginProxyUpstreamError(t *testing.T) {
	fake := &fakeSearchRemote{statusCode: http.StatusInternalServerError, payload: `{"detail":"boom in remote"}`}
	upstream := httptest.NewServer(fake.handler())
	defer upstream.Close()

	remote := search.NewRemoteProvider(upstream.URL, "test-token", 5*time.Second)
	h := newAdminHarnessWith(t, nil, remote)

	status, _, body := h.do(http.MethodGet, "/api/admin/plugins/search-remote/manifest", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%v", status, body)
	}
	if asString(body["detail"]) != "boom in remote" {
		t.Errorf("detail = %q, want upstream detail %q", body["detail"], "boom in remote")
	}
}

func TestAPI_Admin_PluginProxyUnreachable(t *testing.T) {
	// A provider pointed at a closed port: the network failure maps to
	// 503 (service unreachable), not 502.
	remote := search.NewRemoteProvider("http://127.0.0.1:1", "test-token", 2*time.Second)
	h := newAdminHarnessWith(t, nil, remote)

	status, _, body := h.do(http.MethodGet, "/api/admin/plugins/search-remote/manifest", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%v", status, body)
	}
	if asString(body["detail"]) == "" {
		t.Error("detail should explain the unreachable upstream")
	}
}

// TestUpstreamDetail: non-JSON upstream bodies fall back to a generic
// detail rather than leaking raw bytes.
func TestUpstreamDetail(t *testing.T) {
	var parsed map[string]string
	if err := json.Unmarshal([]byte(upstreamDetail([]byte(`{"detail":"x"}`))), &parsed); err == nil {
		t.Fatal("upstreamDetail should return a plain string, not JSON")
	}
	if got := upstreamDetail([]byte(`{"detail":"rate limited"}`)); got != "rate limited" {
		t.Errorf("upstreamDetail JSON = %q", got)
	}
	if got := upstreamDetail([]byte(`<html>oops</html>`)); got != "search remote upstream error" {
		t.Errorf("upstreamDetail fallback = %q", got)
	}
	if got := upstreamDetail([]byte(`{"detail":""}`)); got != "search remote upstream error" {
		t.Errorf("upstreamDetail empty detail = %q", got)
	}
}
