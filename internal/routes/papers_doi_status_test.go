package routes

// Tests for the DOI /markdown/status and /pdf/status handlers added in
// the PR #19 follow-up review fix. The bug was that splitPapersPath's
// last-slash rule glued the trailing kind onto arxivPart (".../markdown"
// or ".../pdf"), which then failed isDOICandidate's regex check inside
// the DOI dispatch and dead-ended at OpenAlex 404. After the fix the
// dispatcher peels the suffix at the top so both handlers see a clean
// DOI.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/pocketbase/pocketbase/core"
)

// statMockStore is a minimal objstore.Store that only answers Stat with
// a fixed set of present keys. Every other method panics — the DOI
// status handlers must not reach for them. (Distinct name from the
// statMockStore in papers_pdf_test.go which has intentionally wrong
// stub signatures and doesn't satisfy objstore.Store.)
type statMockStore struct {
	present map[string]bool
}

func (s *statMockStore) Stat(_ context.Context, key string) (objstore.ObjectInfo, bool, error) {
	if s.present[key] {
		return objstore.ObjectInfo{Size: 1}, true, nil
	}
	return objstore.ObjectInfo{}, false, nil
}

func (s *statMockStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) (int64, error) {
	panic("Put unused")
}
func (s *statMockStore) PutWithMeta(_ context.Context, _ string, _ io.Reader, _ int64, _ string, _ map[string]string) (int64, error) {
	panic("PutWithMeta unused")
}
func (s *statMockStore) PutWithOptions(_ context.Context, _ string, _ io.Reader, _ int64, _ objstore.PutOptions) (int64, error) {
	panic("PutWithOptions unused")
}
func (s *statMockStore) Get(_ context.Context, _ string) (io.ReadCloser, objstore.ObjectInfo, error) {
	panic("Get unused")
}
func (s *statMockStore) Delete(_ context.Context, _ string) error { panic("Delete unused") }
func (s *statMockStore) ListPrefix(_ context.Context, _ string, _ int) ([]objstore.ObjectInfo, error) {
	panic("ListPrefix unused")
}
func (s *statMockStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	panic("PresignGet unused")
}

func mustDOIStatusReq(t *testing.T, doi, kind string) (*core.RequestEvent, *httptest.ResponseRecorder) {
	t.Helper()
	url := "/api/papers/" + doi + "/" + kind + "/status"
	req := httptest.NewRequest(http.MethodGet, url, nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	return re, rec
}

func TestProbeDOIAssetReadiness(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	pdfKey := paperassets.DOIAssetKey("pdf", doi)
	mdKey := paperassets.DOIAssetKey("markdown", doi)

	t.Run("both missing", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{}}
		pdf, md := probeDOIAssetReadiness(context.Background(), s, doi)
		if pdf || md {
			t.Errorf("got (%v,%v), want (false,false)", pdf, md)
		}
	})
	t.Run("pdf only", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{pdfKey: true}}
		pdf, md := probeDOIAssetReadiness(context.Background(), s, doi)
		if !pdf || md {
			t.Errorf("got (%v,%v), want (true,false)", pdf, md)
		}
	})
	t.Run("md only", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{mdKey: true}}
		pdf, md := probeDOIAssetReadiness(context.Background(), s, doi)
		if pdf || !md {
			t.Errorf("got (%v,%v), want (false,true)", pdf, md)
		}
	})
	t.Run("both present", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{pdfKey: true, mdKey: true}}
		pdf, md := probeDOIAssetReadiness(context.Background(), s, doi)
		if !pdf || !md {
			t.Errorf("got (%v,%v), want (true,true)", pdf, md)
		}
	})
	t.Run("invalid DOI returns (false,false) without panic", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{}}
		pdf, md := probeDOIAssetReadiness(context.Background(), s, "not-a-doi")
		if pdf || md {
			t.Errorf("got (%v,%v), want (false,false)", pdf, md)
		}
	})
	t.Run("nil store is safe", func(t *testing.T) {
		pdf, md := probeDOIAssetReadiness(context.Background(), nil, doi)
		if pdf || md {
			t.Errorf("got (%v,%v), want (false,false)", pdf, md)
		}
	})
}

func TestMarkdownStatusByDOIHandler_Cached(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	mdKey := paperassets.DOIAssetKey("markdown", doi)
	pdfKey := paperassets.DOIAssetKey("pdf", doi)
	store := &statMockStore{present: map[string]bool{mdKey: true, pdfKey: true}}

	re, rec := mustDOIStatusReq(t, doi, "markdown")
	if err := markdownStatusByDOIHandler(re, store, doi); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got := body["doi"]; got != doi {
		t.Errorf("body.doi = %v, want %q", got, doi)
	}
	if body["state"] != "cached" {
		t.Errorf("body.state = %v, want cached", body["state"])
	}
	if mdReady, _ := body["md_ready"].(bool); !mdReady {
		t.Errorf("body.md_ready = %v, want true", body["md_ready"])
	}
	if pdfReady, _ := body["pdf_ready"].(bool); !pdfReady {
		t.Errorf("body.pdf_ready = %v, want true", body["pdf_ready"])
	}
	if got := body["markdown_url"]; got != "/api/papers/"+doi+"/markdown" {
		t.Errorf("body.markdown_url = %v, want /api/papers/<doi>/markdown", got)
	}
	if got := rec.Header().Get("X-QAtlas-DOI"); got != doi {
		t.Errorf("X-QAtlas-DOI = %q, want %q", got, doi)
	}
}

func TestMarkdownStatusByDOIHandler_Missing(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	pdfKey := paperassets.DOIAssetKey("pdf", doi)
	// PDF is on disk but markdown is not — md/status should still 200
	// with state=missing rather than 404 (lets the client poll for
	// readiness without falling back to a separate "not found" branch).
	store := &statMockStore{present: map[string]bool{pdfKey: true}}

	re, rec := mustDOIStatusReq(t, doi, "markdown")
	if err := markdownStatusByDOIHandler(re, store, doi); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["state"] != "missing" {
		t.Errorf("body.state = %v, want missing", body["state"])
	}
	if mdReady, _ := body["md_ready"].(bool); mdReady {
		t.Errorf("body.md_ready = %v, want false", body["md_ready"])
	}
	if pdfReady, _ := body["pdf_ready"].(bool); !pdfReady {
		t.Errorf("body.pdf_ready = %v, want true", body["pdf_ready"])
	}
}

func TestPDFStatusByDOIHandler_Cached(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	pdfKey := paperassets.DOIAssetKey("pdf", doi)
	store := &statMockStore{present: map[string]bool{pdfKey: true}}

	re, rec := mustDOIStatusReq(t, doi, "pdf")
	if err := pdfStatusByDOIHandler(re, store, doi); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["state"] != "cached" {
		t.Errorf("body.state = %v, want cached", body["state"])
	}
	if got := body["pdf_url"]; got != "/api/papers/"+doi+"/pdf" {
		t.Errorf("body.pdf_url = %v, want /api/papers/<doi>/pdf", got)
	}
}

func TestStatusByDOIHandlerRejectsBadDOI(t *testing.T) {
	store := &statMockStore{present: map[string]bool{}}
	for _, kind := range []string{"markdown", "pdf"} {
		req := httptest.NewRequest(http.MethodGet, "/api/papers/not-a-doi/"+kind+"/status", nil)
		rec := httptest.NewRecorder()
		re := &core.RequestEvent{}
		re.Request = req
		re.Response = rec
		var err error
		if kind == "markdown" {
			err = markdownStatusByDOIHandler(re, store, "not-a-doi")
		} else {
			err = pdfStatusByDOIHandler(re, store, "not-a-doi")
		}
		if err != nil {
			t.Fatalf("%s/status handler returned err: %v", kind, err)
		}
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s/status code = %d, want 400 for invalid DOI", kind, rec.Code)
		}
	}
}

// statusKindPeel exercises ONLY the dispatcher's status-suffix peeling
// logic so the regression doesn't regress at the dispatch layer if
// someone "simplifies" the splitPapersPath helper or the DOI fast path
// later. The full GET dispatcher needs a live PocketBase router to
// invoke — covered by integration tests — but the peel itself is pure
// string manipulation, lifted into a small inline helper here.
func TestStatusSuffixPeel(t *testing.T) {
	cases := []struct {
		raw, wantArxiv, wantStatusKind string
	}{
		{"10.1234/foo/markdown/status", "10.1234/foo", "markdown"},
		{"10.1234/foo/pdf/status", "10.1234/foo", "pdf"},
		{"10.1234/foo/bar/markdown/status", "10.1234/foo/bar", "markdown"}, // nested-slash DOI, the headline regression
		{"2501.00010v1/markdown/status", "2501.00010v1", "markdown"},
		{"2501.00010v1/pdf/status", "2501.00010v1", "pdf"},
		// no peel for non-status actions
		{"10.1234/foo/markdown", "10.1234/foo", ""},
		{"10.1234/foo/pdf", "10.1234/foo", ""},
		// stray "status" without the kind suffix is ambiguous and
		// passes through (the status handler then picks DOI vs arxiv).
		{"10.1234/foo/status", "10.1234/foo", ""},
	}
	for _, c := range cases {
		arxiv, action := splitPapersPath(c.raw)
		statusKind := ""
		if action == "status" {
			switch {
			case len(arxiv) > len("/markdown") && arxiv[len(arxiv)-len("/markdown"):] == "/markdown":
				arxiv = arxiv[:len(arxiv)-len("/markdown")]
				statusKind = "markdown"
			case len(arxiv) > len("/pdf") && arxiv[len(arxiv)-len("/pdf"):] == "/pdf":
				arxiv = arxiv[:len(arxiv)-len("/pdf")]
				statusKind = "pdf"
			}
		}
		if arxiv != c.wantArxiv || statusKind != c.wantStatusKind {
			t.Errorf("peel(%q) = (%q,%q), want (%q,%q)", c.raw, arxiv, statusKind, c.wantArxiv, c.wantStatusKind)
		}
	}
	// Sanity: errors.Is import is not required here, but the test
	// file imports it for future cases. Touch it so the import never
	// goes silently unused.
	_ = errors.Is
}
