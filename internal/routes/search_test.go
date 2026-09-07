package routes

// HTTP-layer tests for POST /api/search: the empty-entry guard and the
// hosting-summary degradation. The harness mirrors newAgenticHarness
// (same OnServe → BuildMux pattern); the populated-summary path needs a
// live Postgres (covered by attachResultSummaries below plus the
// registry integration suite).

import (
	"net/http"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// newSearchHarness mounts POST /api/search over a real test PB app with
// a provider-less engine (nil registry: minting disabled).
func newSearchHarness(t testing.TB, catalog *registry.Store) *patHarness {
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
		RegisterSearch(e, search.NewEngine(nil, nil), catalog, enforcer)
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

func TestAPI_Search_RejectsAnonymous(t *testing.T) {
	h := newSearchHarness(t, nil)
	status, _, _ := h.do(http.MethodPost, "/api/search", `{"text":"x"}`, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

// TestAPI_Search_EmptyEntry: an entry with none of text / title /
// arxiv_id / doi must 400 instead of fanning out an empty query.
func TestAPI_Search_EmptyEntry(t *testing.T) {
	h := newSearchHarness(t, nil)
	for _, body := range []string{`{}`, `{"text":"  ","title":""}`, `{"max_results":5}`} {
		status, _, decoded := h.do(http.MethodPost, "/api/search", body, rawHeader(h.sessionToken()))
		if status != http.StatusBadRequest {
			t.Errorf("body %s: status = %d, want 400", body, status)
			continue
		}
		if detail, _ := decoded["detail"].(string); !containsSubstr(detail, "empty search entry") {
			t.Errorf("body %s: detail = %q, want the empty-search-entry message", body, detail)
		}
	}
}

// TestAPI_Search_IdentityOnlyEntry: identity fields count as a query —
// the entry passes the guard and answers 200 (empty result set from the
// provider-less engine, hosting fields omitted via the nil catalog).
func TestAPI_Search_IdentityOnlyEntry(t *testing.T) {
	h := newSearchHarness(t, nil)
	status, raw, body := h.do(http.MethodPost, "/api/search", `{"arxiv_id":"2501.03424"}`, rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", status, raw)
	}
	results, ok := body["results"].([]any)
	if !ok || len(results) != 0 {
		t.Errorf("results = %v, want []", body["results"])
	}
	if string(raw) != "" && containsSubstr(string(raw), `"has_md"`) {
		t.Errorf("has_md must be omitted when the catalog is nil; body=%s", raw)
	}
}

// TestAttachResultSummaries covers the wire decoration: summary entries
// fill has_md / has_pdf / status; unknown or empty paper_ids stay
// omitted (degradation contract).
func TestAttachResultSummaries(t *testing.T) {
	out := []searchResultJSON{
		{PaperID: "qa_known", Hit: search.Hit{Title: "known"}},
		{PaperID: "qa_unknown", Hit: search.Hit{Title: "unknown"}},
		{PaperID: "", Hit: search.Hit{Title: "un-minted"}},
	}
	attachResultSummaries(out, map[string]registry.PaperSummary{
		"qa_known": {Status: "ready", HasMD: true, HasPDF: true},
	})
	if out[0].HasMD == nil || !*out[0].HasMD || out[0].HasPDF == nil || !*out[0].HasPDF || out[0].Status != "ready" {
		t.Errorf("out[0] = %+v, want filled summary", out[0])
	}
	if out[1].HasMD != nil || out[1].HasPDF != nil || out[1].Status != "" {
		t.Errorf("out[1] = %+v, want omitted fields for unknown paper", out[1])
	}
	if out[2].HasMD != nil || out[2].Status != "" {
		t.Errorf("out[2] = %+v, want omitted fields for empty paper_id", out[2])
	}
}
