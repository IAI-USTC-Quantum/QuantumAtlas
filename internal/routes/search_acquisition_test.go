package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// Search cards poll this detail handler, including for PDF-only hits. Merely
// rendering or polling a result must not submit MinerU work or fetch a PDF.
func TestPaperDetailPollingDoesNotAcquire(t *testing.T) {
	for _, source := range []string{"arxiv", "published"} {
		t.Run(source, func(t *testing.T) {
			converter := mineru.NewConverter(mineru.ConverterConfig{
				PaperAccessEnabled: true,
				MinerUAPITokens:    []string{"test-only-token"},
			}, canonicalNoopStore{}, nil, nil)
			if !converter.Enabled() {
				t.Fatal("test requires an enabled converter to catch implicit Ensure calls")
			}
			paper := &registry.Paper{PaperID: "qa_poll", Status: "ready", DOI: "10.1000/poll", ArxivID: "2401.12345"}
			catalog := newFakePaperCatalog()
			catalog.papers[paper.PaperID] = &registry.PaperDetail{
				Paper:  paper,
				Assets: []registry.Asset{{Source: source, ArxivVersion: 2, PDFPath: "existing.pdf"}},
			}
			catalog.identity["doi:"+paper.DOI] = paper.PaperID
			catalog.identity["arxiv:"+paper.ArxivID] = paper.PaperID
			// Both surrogate and identifier detail paths must be observational.
			for _, identifier := range []string{paper.PaperID, paper.DOI, paper.ArxivID, paper.PaperID} {
				recorder := httptest.NewRecorder()
				re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/api/papers/"+identifier, nil), recorder)
				var err error
				if identifier == paper.PaperID {
					err = paperDetailHandler(re, catalog, paper.PaperID, nil, converter)
				} else {
					var handled bool
					handled, err = dispatchDetailByIdentifier(re, catalog, identifier, nil, converter)
					if !handled {
						t.Fatal("identifier was not handled")
					}
				}
				if err != nil || recorder.Code != http.StatusOK {
					t.Fatalf("detail status=%d err=%v", recorder.Code, err)
				}
				var body struct {
					Acquisition map[string]any `json:"acquisition"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
					t.Fatal(err)
				}
				if body.Acquisition["active"] != false || body.Acquisition["state"] != "idle" || body.Acquisition["phase"] != "waiting_mineru" {
					t.Fatalf("unselected PDF incorrectly reported as queued: %+v", body.Acquisition)
				}
			}
			if counters := converter.Snapshot(); counters.Submitted != 0 || counters.ArxivFetches != 0 {
				t.Fatalf("polling submitted work: %+v", counters)
			}
			if job, ok := converter.Lookup("2401.12345v2"); ok {
				t.Fatalf("polling created arXiv job: %+v", job)
			}
			if job, ok := converter.LookupDOI(paper.DOI); ok {
				t.Fatalf("polling created DOI job: %+v", job)
			}
		})
	}
}
