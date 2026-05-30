package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"

	"github.com/pocketbase/pocketbase/core"
)

// callStats invokes paperStatsHandler with a synthetic request and
// decodes the JSON body.
func callStats(t *testing.T, catalog *papers.Store) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/stats", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := paperStatsHandler(re, catalog); err != nil {
		t.Fatalf("paperStatsHandler: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

// TestPaperStatsHandlerUnavailable verifies the graceful-degradation
// path: with no Neo4j client configured (NewStore(nil)) the catalog
// reports ErrCatalogUnavailable and the handler returns 200 with
// available:false rather than a 500. The populated-counts path is
// covered by the §8.2 end-to-end suite (needs a live Neo4j).
func TestPaperStatsHandlerUnavailable(t *testing.T) {
	catalog := papers.NewStore(nil)
	body := callStats(t, catalog)
	if avail, _ := body["available"].(bool); avail {
		t.Fatalf("expected available=false when catalog is unconfigured, got %v", body)
	}
}
