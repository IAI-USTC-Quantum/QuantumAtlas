package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// callPapersList invokes papersListHandler with a synthetic request and
// returns the recorder plus the decoded body.
func callPapersList(t *testing.T, catalog *registry.Store, rawQuery string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers?"+rawQuery, nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := papersListHandler(re, catalog); err != nil {
		t.Fatalf("papersListHandler: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

// TestPapersListHandlerBadParams covers query-param validation: unknown
// enum values are a 400 before the catalog is ever touched.
func TestPapersListHandlerBadParams(t *testing.T) {
	catalog := registry.NewStore(nil)
	for _, q := range []string{
		"has_md=maybe",
		"status=archived",
		"sort=title",
		"arxiv_id=not-an-arxiv-id",
		"doi=definitely-not-a-doi",
		"paper_id=nope",
		"paper_id=qa_",
	} {
		rec, _ := callPapersList(t, catalog, q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, rec.Code)
		}
	}
}

// TestPapersListHandlerIdentityFiltersAccepted proves the three
// exact-identity filters parse (and pass validation) for every accepted
// shape — the matching itself is covered by the registry integration
// suite against a live PostgreSQL.
func TestPapersListHandlerIdentityFiltersAccepted(t *testing.T) {
	catalog := registry.NewStore(nil)
	for _, q := range []string{
		"arxiv_id=2501.00010",
		"arxiv_id=2501.00010v3", // version suffix tolerated (normalized away)
		"arxiv_id=quant-ph/9508027",
		"doi=10.22331/q-2023-03-20-955",
		"doi=https://doi.org/10.22331/q-2023-03-20-955", // URL prefix tolerated
		"paper_id=qa_01h5gvbpyf25hjyb9wq3v7r9ta",
	} {
		rec, _ := callPapersList(t, catalog, q)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("query %q: status = %d, want 503 (validation passed, nil-pool catalog)", q, rec.Code)
		}
	}
}

// TestPapersListHandlerUnavailable verifies graceful degradation: a
// valid request against an unconfigured catalog returns 503. The
// populated path is covered by the registry integration suite (needs a
// live PostgreSQL).
func TestPapersListHandlerUnavailable(t *testing.T) {
	rec, _ := callPapersList(t, registry.NewStore(nil), "has_md=true&page=2&per_page=50")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
}
