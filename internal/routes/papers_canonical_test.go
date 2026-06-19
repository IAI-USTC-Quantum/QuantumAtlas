package routes

// Tests for the DOI-canonical resolution helpers added in the PR #19
// review-4 follow-up. The full GET catch-all in RegisterPapers needs a
// live PocketBase router to invoke (covered by integration tests), but
// the helpers below are pure / dependency-light and locked in here.
//
// The contract under test is: a :PaperWork node with
// `identifier_scheme='doi'` ALWAYS wins over its arxiv twin when both
// exist, and the caller opts out per-request with `?force_arxiv=1`.

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/pocketbase/pocketbase/core"
)

func newTestReqEvent(req *http.Request, rec http.ResponseWriter) *core.RequestEvent {
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	return re
}

func TestParseForceArxivQuery(t *testing.T) {
	cases := []struct {
		query string
		want  bool
	}{
		{"", false},
		{"force_arxiv=1", true},
		{"force_arxiv=true", true},
		{"force_arxiv=TRUE", true},
		{"force_arxiv= true ", true}, // trimmed before compare
		{"force_arxiv=0", false},
		{"force_arxiv=false", false},
		{"force_arxiv=yes", false}, // strict: only 1/true count
		{"force_arxiv", false},     // missing value
		{"other=1", false},
	}
	for _, c := range cases {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.URL.RawQuery = c.query
		re := newTestReqEvent(req, httptest.NewRecorder())
		got := parseForceArxivQuery(re)
		if got != c.want {
			t.Errorf("parseForceArxivQuery(%q) = %v, want %v", c.query, got, c.want)
		}
	}
}

func TestApplyDOICanonicalHeadersDOIInput(t *testing.T) {
	// Shape (a): caller passed a DOI. RequestedID == DOI ⇒ no
	// arxiv-twin redirect note, just the canonical header.
	doi := "10.1103/physrevlett.123.070501"
	rec := httptest.NewRecorder()
	re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/", nil), rec)

	applyDOICanonicalHeaders(re, doi, doi, "")

	if got := rec.Header().Get("X-QAtlas-Requested-Id"); got != doi {
		t.Errorf("X-QAtlas-Requested-Id = %q, want %q", got, doi)
	}
	if got := rec.Header().Get("X-QAtlas-Resolved-Id"); got != doi {
		t.Errorf("X-QAtlas-Resolved-Id = %q, want %q", got, doi)
	}
	if got := rec.Header().Get("X-QAtlas-Canonical-DOI"); got != doi {
		t.Errorf("X-QAtlas-Canonical-DOI = %q, want %q", got, doi)
	}
	if got := rec.Header().Get("X-QAtlas-Defaults-Applied"); got != "" {
		t.Errorf("X-QAtlas-Defaults-Applied = %q, want empty (no redirect)", got)
	}
}

func TestApplyDOICanonicalHeadersArxivInput(t *testing.T) {
	// Shape (b): caller passed an arxiv id, we redirected to its DOI
	// twin. The X-QAtlas-Defaults-Applied entry MUST mention both ids
	// and the ?force_arxiv=1 opt-out so the client can detect the
	// redirect and know how to suppress it next time.
	arxiv := "2501.00010v1"
	doi := "10.1103/physrevlett.123.070501"
	rec := httptest.NewRecorder()
	re := newTestReqEvent(httptest.NewRequest(http.MethodGet, "/", nil), rec)

	applyDOICanonicalHeaders(re, arxiv, doi, arxiv)

	if got := rec.Header().Get("X-QAtlas-Requested-Id"); got != arxiv {
		t.Errorf("X-QAtlas-Requested-Id = %q, want %q", got, arxiv)
	}
	if got := rec.Header().Get("X-QAtlas-Resolved-Id"); got != doi {
		t.Errorf("X-QAtlas-Resolved-Id = %q, want %q", got, doi)
	}
	if got := rec.Header().Get("X-QAtlas-Canonical-DOI"); got != doi {
		t.Errorf("X-QAtlas-Canonical-DOI = %q, want %q", got, doi)
	}
	da := rec.Header().Get("X-QAtlas-Defaults-Applied")
	for _, mustContain := range []string{"served_as_doi_canonical", arxiv, doi, "force_arxiv=1"} {
		if !containsSubstr(da, mustContain) {
			t.Errorf("X-QAtlas-Defaults-Applied = %q, missing %q", da, mustContain)
		}
	}
}

func TestActionLabel(t *testing.T) {
	cases := []struct {
		action, statusKind, want string
	}{
		{"pdf", "", "pdf"},
		{"markdown", "", "markdown"},
		{"status", "pdf", "pdf/status"},
		{"status", "markdown", "markdown/status"},
		{"", "", "pdf"}, // fallback when caller passed neither
	}
	for _, c := range cases {
		got := actionLabel(c.action, c.statusKind)
		if got != c.want {
			t.Errorf("actionLabel(%q,%q) = %q, want %q", c.action, c.statusKind, got, c.want)
		}
	}
}

// canonicalNoopStore satisfies objstore.Store with nil/false answers
// everywhere; for dispatchGETDOIHandlers we only need to exercise the
// routing switch — the eventual handler will return its own 4xx for
// missing bytes and we don't dive into that here.
type canonicalNoopStore struct{}

func (canonicalNoopStore) Stat(_ context.Context, _ string) (objstore.ObjectInfo, bool, error) {
	return objstore.ObjectInfo{}, false, nil
}
func (canonicalNoopStore) Put(_ context.Context, _ string, _ io.Reader, _ int64, _ string) (int64, error) {
	return 0, nil
}
func (canonicalNoopStore) PutWithMeta(_ context.Context, _ string, _ io.Reader, _ int64, _ string, _ map[string]string) (int64, error) {
	return 0, nil
}
func (canonicalNoopStore) PutWithOptions(_ context.Context, _ string, _ io.Reader, _ int64, _ objstore.PutOptions) (int64, error) {
	return 0, nil
}
func (canonicalNoopStore) Get(_ context.Context, _ string) (io.ReadCloser, objstore.ObjectInfo, error) {
	return nil, objstore.ObjectInfo{}, objstore.ErrNotFound
}
func (canonicalNoopStore) Delete(_ context.Context, _ string) error { return nil }
func (canonicalNoopStore) ListPrefix(_ context.Context, _ string, _ int) ([]objstore.ObjectInfo, error) {
	return nil, nil
}
func (canonicalNoopStore) PresignGet(_ context.Context, _ string, _ time.Duration) (string, bool, error) {
	return "", false, nil
}

func TestDispatchGETDOIHandlersStatusAction(t *testing.T) {
	// action="status" with no statusKind is the bare /status endpoint
	// (e.g. /api/papers/<doi>/status). The dispatcher synthesises an
	// available-marker JSON without touching the handlers — pure.
	doi := "10.1103/physrevlett.123.070501"
	rec := httptest.NewRecorder()
	re := newTestReqEvent(
		httptest.NewRequest(http.MethodGet, "/api/papers/"+doi+"/status", nil),
		rec,
	)

	cfg := &config.Config{}
	if err := dispatchGETDOIHandlers(re, cfg, canonicalNoopStore{}, nil, doi, "status", "", doi+"/status"); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", rec.Code)
	}
}

func TestDispatchGETDOIHandlersUnknownAction(t *testing.T) {
	// An action the matrix doesn't recognise must surface a 404 with
	// the original raw path in the message, not silently fall through
	// to a different handler.
	doi := "10.1103/physrevlett.123.070501"
	rec := httptest.NewRecorder()
	re := newTestReqEvent(
		httptest.NewRequest(http.MethodGet, "/api/papers/"+doi+"/totally-unknown", nil),
		rec,
	)

	cfg := &config.Config{}
	if err := dispatchGETDOIHandlers(re, cfg, canonicalNoopStore{}, nil, doi, "totally-unknown", "", doi+"/totally-unknown"); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
	if !containsSubstr(rec.Body.String(), "totally-unknown") {
		t.Errorf("body should mention the unknown action; got %s", rec.Body.String())
	}
}

func containsSubstr(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
