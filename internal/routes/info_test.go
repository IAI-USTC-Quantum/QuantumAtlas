package routes

// HTTP-layer tests for GET /api/server/info: the capability block and
// the two-tier privacy (anonymous booleans only; authenticated callers
// additionally see the MinerU daily-cap numbers — same tiering as
// /api/health).

import (
	"net/http"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// newInfoHarness mounts GET /api/server/info over a real test PB app.
func newInfoHarness(t testing.TB, cfg *config.Config, converter *mineru.Converter, scheduler *mineru.Scheduler) *patHarness {
	t.Helper()
	h := &patHarness{t: t}

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	h.app = app

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterServerInfo(e, cfg, "9.9.9-test", converter, scheduler, true)
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

func capsOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	caps, ok := body["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("capabilities missing / wrong type in %v", body)
	}
	return caps
}

func TestAPI_ServerInfo_AnonymousTier(t *testing.T) {
	cfg := &config.Config{PaperAccessEnabled: true}
	converter := mineru.NewConverter(mineru.ConverterConfig{
		PaperAccessEnabled: true,
		MinerUAPITokens:    []string{"tok"},
	}, nil, nil, nil)
	scheduler := mineru.NewScheduler(converter, nil, nil)

	h := newInfoHarness(t, cfg, converter, scheduler)
	status, _, body := h.do(http.MethodGet, "/api/server/info", "", nil)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if body["version"] != "9.9.9-test" || body["mode"] != "server" || body["engine"] != "go+pocketbase" {
		t.Errorf("legacy fields wrong: %v", body)
	}
	caps := capsOf(t, body)
	if caps["paper_access"] != true || caps["markdown_delivery"] != true {
		t.Errorf("paper-access caps = %v/%v, want true/true", caps["paper_access"], caps["markdown_delivery"])
	}
	if caps["pdf_delivery"] != false {
		t.Errorf("pdf_delivery = %v, want constant false", caps["pdf_delivery"])
	}
	if caps["agentic_search"] != true {
		t.Errorf("agentic_search = %v, want true", caps["agentic_search"])
	}
	m, ok := caps["mineru"].(map[string]any)
	if !ok {
		t.Fatalf("mineru cap missing / wrong type: %v", caps["mineru"])
	}
	if m["enabled"] != true || m["on_demand"] != true {
		t.Errorf("mineru enabled/on_demand = %v/%v, want true/true", m["enabled"], m["on_demand"])
	}
	// Privacy tier: the numbers must NOT leak to anonymous callers.
	if _, has := m["daily_cap"]; has {
		t.Error("mineru.daily_cap leaked to anonymous caller")
	}
	if _, has := m["converted_today"]; has {
		t.Error("mineru.converted_today leaked to anonymous caller")
	}
}

func TestAPI_ServerInfo_AuthenticatedTier(t *testing.T) {
	converter := mineru.NewConverter(mineru.ConverterConfig{}, nil, nil, nil)
	scheduler := mineru.NewScheduler(converter, nil, nil)

	h := newInfoHarness(t, &config.Config{}, converter, scheduler)

	// Session caller sees the numbers (default cap 4000, zero today).
	status, _, body := h.do(http.MethodGet, "/api/server/info", "", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	m, ok := capsOf(t, body)["mineru"].(map[string]any)
	if !ok {
		t.Fatalf("mineru cap missing: %v", body)
	}
	if m["daily_cap"] != float64(mineru.DefaultDailyCap) {
		t.Errorf("daily_cap = %v, want %d", m["daily_cap"], mineru.DefaultDailyCap)
	}
	if m["converted_today"] != float64(0) {
		t.Errorf("converted_today = %v, want 0", m["converted_today"])
	}
	if m["enabled"] != false || m["on_demand"] != false {
		t.Errorf("mineru enabled/on_demand = %v/%v, want false/false (no tokens)", m["enabled"], m["on_demand"])
	}

	// System PAT is the other recognized credential.
	const secret = "server-info-system-pat-test-secret-long-enough"
	sysPAT, err := pat.LoadSystemPAT(secret, []string{"*"})
	if err != nil {
		t.Fatalf("LoadSystemPAT: %v", err)
	}
	UseSystemPAT(sysPAT)
	t.Cleanup(func() { UseSystemPAT(nil) })
	status, _, body = h.do(http.MethodGet, "/api/server/info", "", bearerHeader(secret))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if m, ok := capsOf(t, body)["mineru"].(map[string]any); !ok || m["daily_cap"] == nil {
		t.Errorf("system PAT caller must see mineru.daily_cap; body=%v", body)
	}
}

// TestAPI_ServerInfo_NilSchedulerOmitsNumbers: without a scheduler the
// authenticated tier simply omits the numbers instead of reporting
// fabricated zeros.
func TestAPI_ServerInfo_NilSchedulerOmitsNumbers(t *testing.T) {
	h := newInfoHarness(t, &config.Config{}, nil, nil)
	status, _, body := h.do(http.MethodGet, "/api/server/info", "", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	m, ok := capsOf(t, body)["mineru"].(map[string]any)
	if !ok {
		t.Fatalf("mineru cap missing: %v", body)
	}
	if _, has := m["daily_cap"]; has {
		t.Error("daily_cap must be omitted when no scheduler is wired")
	}
	if m["enabled"] != false {
		t.Errorf("mineru.enabled = %v, want false for nil converter", m["enabled"])
	}
}
