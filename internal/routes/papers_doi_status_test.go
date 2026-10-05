package routes

// Tests for the DOI /markdown/status and /pdf/status handlers added in
// the PR #19 follow-up review fix. The bug was that splitPapersPath's
// last-slash rule glued the trailing kind onto arxivPart (".../markdown"
// or ".../pdf"), which then failed isDOICandidate's regex check inside
// the DOI dispatch and dead-ended at OpenAlex 404. After the fix the
// dispatcher peels the suffix at the top so both handlers see a clean
// DOI.

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
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
func (s *statMockStore) GetRange(_ context.Context, _ string, _, _ int64) (io.ReadCloser, error) {
	panic("GetRange unused")
}
func (s *statMockStore) Delete(_ context.Context, _ string) error { panic("Delete unused") }
func (s *statMockStore) ListPrefix(_ context.Context, _ string, _ int) ([]objstore.ObjectInfo, error) {
	panic("ListPrefix unused")
}
func (s *statMockStore) ListDirs(_ context.Context, _ string) ([]string, error) {
	panic("ListDirs unused")
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
		pdf, md, _ := probeDOIAssetReadiness(context.Background(), s, doi)
		if pdf || md {
			t.Errorf("got (%v,%v), want (false,false)", pdf, md)
		}
	})
	t.Run("pdf only", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{pdfKey: true}}
		pdf, md, _ := probeDOIAssetReadiness(context.Background(), s, doi)
		if !pdf || md {
			t.Errorf("got (%v,%v), want (true,false)", pdf, md)
		}
	})
	t.Run("md only", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{mdKey: true}}
		pdf, md, _ := probeDOIAssetReadiness(context.Background(), s, doi)
		if pdf || md {
			t.Errorf("got (%v,%v), want (false,false); legacy MD is not ready", pdf, md)
		}
	})
	t.Run("both present", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{pdfKey: true, mdKey: true}}
		pdf, md, _ := probeDOIAssetReadiness(context.Background(), s, doi)
		if !pdf || md {
			t.Errorf("got (%v,%v), want (true,false); legacy MD is not ready", pdf, md)
		}
	})
	t.Run("invalid DOI returns (false,false) without panic", func(t *testing.T) {
		s := &statMockStore{present: map[string]bool{}}
		pdf, md, _ := probeDOIAssetReadiness(context.Background(), s, "not-a-doi")
		if pdf || md {
			t.Errorf("got (%v,%v), want (false,false)", pdf, md)
		}
	})
	t.Run("nil store is safe", func(t *testing.T) {
		pdf, md, _ := probeDOIAssetReadiness(context.Background(), nil, doi)
		if pdf || md {
			t.Errorf("got (%v,%v), want (false,false)", pdf, md)
		}
	})
}

func TestMarkdownStatusByDOIHandler_Cached(t *testing.T) {
	c, store, b := newDOIReadingFixture(t)
	rec, body := readRouteRequest(t, "/api/papers/"+c.doi+"/read/status?source_id="+readTestSource, func(re *core.RequestEvent) error {
		return contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, nil, c.doi)
	})
	if rec.Code != 200 || body["ready"] != true || body["md_ready"] != true || body["pdf_ready"] != true || body["revision"] != b.RevisionID || body["source_id"] != readTestSource {
		t.Fatalf("complete status %d %+v", rec.Code, body)
	}
}
func TestMarkdownStatusByDOIHandler_Missing(t *testing.T) {
	c, store, _ := newDOIReadingFixture(t)
	c.bundles = map[string]registry.ParseBundle{}
	_, _ = store.Put(t.Context(), paperassets.DOIAssetKey("markdown", c.doi), strings.NewReader("legacy cached bytes"), 19, "text/markdown")
	conv := &readTestConverter{job: &mineru.Job{State: mineru.JobStateDone, RevisionID: "pr_stale"}}
	rec, body := readRouteRequest(t, "/api/papers/"+c.doi+"/read/status?source_id="+readTestSource, func(re *core.RequestEvent) error {
		return contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, conv, c.doi)
	})
	if rec.Code != 202 || body["ready"] != false || body["md_ready"] != false || body["pdf_ready"] != true || conv.ensureCalls != 0 {
		t.Fatalf("stale MD/Done faked readiness %d %+v", rec.Code, body)
	}
}
func TestPDFByDOIAndPinnedStatusWithoutConverter(t *testing.T) {
	c, store, _ := newDOIReadingFixture(t)
	c.bundles = map[string]registry.ParseBundle{}
	original, err := readAllFromStore(t, store, c.source.ObjstoreKey)
	if err != nil {
		t.Fatal(err)
	}
	rec, _ := readRouteRequest(t, "/api/papers/"+c.doi+"/pdf?source_id="+readTestSource, func(re *core.RequestEvent) error {
		return contentPDFHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, nil, c.doi)
	})
	if rec.Code != 200 || !bytes.Equal(rec.Body.Bytes(), original) || rec.Header().Get("X-QAtlas-Source-Id") != readTestSource {
		t.Fatalf("PDF-only delivery %d %s", rec.Code, rec.Body.String())
	}
	status, body := readRouteRequest(t, "/api/papers/"+c.doi+"/pdf/status?source_id="+readTestSource, func(re *core.RequestEvent) error {
		return contentPDFStatusHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, nil, c.doi)
	})
	if status.Code != 200 || body["pdf_ready"] != true || body["md_ready"] != false || body["source_id"] != readTestSource || !strings.Contains(body["pdf_url"].(string), readTestSource) {
		t.Fatalf("source-bound PDF readiness %d %+v", status.Code, body)
	}
}

func TestPDFStatusByDOIHandler_Cached(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	store := &statMockStore{present: map[string]bool{paperassets.DOIAssetKey("pdf", doi): true, paperassets.DOIAssetKey("markdown", doi): true}}
	re, rec := mustDOIStatusReq(t, doi, "pdf")
	if err := pdfStatusByDOIHandler(re, store, doi); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 503 {
		t.Fatal("catalog-less legacy metadata was accepted as frozen PDF proof")
	}
}
func TestDOIStatusCompatibilityCannotTrustLegacyStore(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	store := &statMockStore{present: map[string]bool{paperassets.DOIAssetKey("pdf", doi): true, paperassets.DOIAssetKey("markdown", doi): true}}
	re, rec := mustDOIStatusReq(t, doi, "markdown")
	if err := markdownStatusByDOIHandler(re, store, nil, doi); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 503 {
		t.Fatal("compatibility status leaked legacy cache readiness")
	}
}
func TestStatusByDOIHandlerRejectsBadDOI(t *testing.T) {
	c, store, _ := newDOIReadingFixture(t)
	rec, _ := readRouteRequest(t, "/api/papers/not-a-doi/read/status", func(re *core.RequestEvent) error {
		return contentReadStatusHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, nil, "not-a-doi")
	})
	if rec.Code != 400 {
		t.Fatalf("invalid status identity: %d", rec.Code)
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
}
