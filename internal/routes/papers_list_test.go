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
	} {
		rec, _ := callPapersList(t, catalog, q)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("query %q: status = %d, want 400", q, rec.Code)
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
