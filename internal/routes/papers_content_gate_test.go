package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/pocketbase/pocketbase/core"
)

func contentGatePaths() []string {
	return []string{
		"/api/papers/1605.01488v2/pdf", "/api/papers/qa_fixture/pdf/status",
		"/api/papers/qa_fixture/read", "/api/papers/qa_fixture/read/status",
		"/api/papers/10.1000/gate/markdown", "/api/papers/qa_fixture/markdown/status",
		"/api/papers/qa_fixture/images", "/api/papers/qa_fixture/images/nested/a.png", "/api/papers/qa_fixture/images/zip", "/api/papers/qa_fixture/figures",
		"/api/papers/qa_fixture/sources", "/api/papers/qa_fixture/sources/src_fixture/pdf",
		"/api/papers/qa_fixture/parses", "/api/papers/qa_fixture/parses/pr_fixture/json",
		"/api/papers/qa_fixture/parses/pr_fixture/files/middle_json.json", "/api/papers/qa_fixture/parses/pr_fixture/manifest",
		"/api/papers/qa_fixture/parses/pr_fixture/blocks", "/api/papers/qa_fixture/parses/pr_fixture/blocks/1/1", "/api/papers/qa_fixture/parses/pr_fixture/blocks/1/1/image",
	}
}

func TestPaperAccessOffGatesEveryAuthenticatedContentPath(t *testing.T) {
	h := newPapersHarness(t, &config.Config{PaperAccessEnabled: false})
	auth := rawHeader(h.sessionToken())
	for _, p := range contentGatePaths() {
		status, _, body := h.do(http.MethodGet, p, "", auth)
		if status != http.StatusNotFound {
			t.Errorf("%s status=%d body=%v", p, status, body)
		}
	}
	// Metadata remains observable without touching content or inference.
	for _, p := range []string{"/api/papers", "/api/papers/qa_fixture", "/api/papers/1605.01488"} {
		status, _, _ := h.do(http.MethodGet, p, "", auth)
		if status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound {
			t.Errorf("metadata unexpectedly gated %s=%d", p, status)
		}
	}
}

func TestPaperAccessOnStillRequiresAuthenticationIncludingRanges(t *testing.T) {
	h := newPapersHarness(t, &config.Config{PaperAccessEnabled: true})
	for _, p := range contentGatePaths() {
		status, _, _ := h.do(http.MethodGet, p, "", map[string]string{"Range": "bytes=0-4"})
		if status != http.StatusUnauthorized {
			t.Errorf("anonymousRange %s=%d", p, status)
		}
	}
}

func TestAdminContentCannotBypassPaperAccess(t *testing.T) {
	for _, kind := range []string{"pdf", "markdown"} {
		for _, link := range []bool{false, true} {
			req := httptest.NewRequest(http.MethodGet, "/api/admin/assets/qa_fixture/"+kind, nil)
			req.SetPathValue("paper_id", "qa_fixture")
			req.SetPathValue("kind", kind)
			rec := httptest.NewRecorder()
			re := &core.RequestEvent{}
			re.Request = req
			re.Response = rec
			if err := adminFrozenAsset(re, &config.Config{}, nil, nil, nil, link); err != nil {
				t.Fatal(err)
			}
			if rec.Code != 404 {
				t.Fatalf("admin %s link=%v bypassed gate: %d", kind, link, rec.Code)
			}
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/admin/assets/batch/download?paper_ids=qa_fixture", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := adminFrozenBatch(re, &config.Config{}, nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 404 {
		t.Fatal("admin batch bypassed gate")
	}
}
