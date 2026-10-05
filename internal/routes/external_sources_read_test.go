package routes

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

func TestExternalSourcesHavePinnedReadTarget(t *testing.T) {
	for _, ref := range []string{"eprint:2026/1591", "source_url:https://university.example/thesis.pdf"} {
		catalog := newFakePaperCatalog()
		catalog.papers["qa_external_original"] = &registry.PaperDetail{Paper: &registry.Paper{PaperID: "qa_external_original", PaperRef: ref, Status: "ready"}}
		target, err := resolvePaperAssetTarget(context.Background(), catalog, "qa_external_original")
		if err != nil || target.NotFound || !target.SourcesOnly || target.DOI != "" || target.ArxivBare != "" || target.ArxivVersioned != "" {
			t.Fatalf("external target=%+v err=%v", target, err)
		}
	}
}

func TestSourcesListPreservesExternalProvenance(t *testing.T) {
	catalog, store := newFakeBlockCatalog(t)
	const original = "https://eprint.iacr.org/2026/1591"
	at := time.Date(2026, 10, 5, 3, 0, 0, 0, time.UTC)
	for id, src := range catalog.sources[fixturePaperID] {
		src.Origin, src.SourceURL, src.RetrievedURL, src.RetrievedAt = "eprint:2026/1591", original, original+".pdf", at
		catalog.sources[fixturePaperID][id] = src
	}
	rec, body := callBlockOriginals(t, catalog, store, "/api/papers/"+fixturePaperID+"/sources", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d %v", rec.Code, body)
	}
	for _, row := range body["sources"].([]any) {
		item := row.(map[string]any)
		if item["source_url"] != original || item["retrieved_url"] != original+".pdf" || item["retrieved_at"] != at.Format(time.RFC3339) {
			t.Fatalf("provenance lost: %v", item)
		}
	}
}
