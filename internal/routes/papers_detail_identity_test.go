package routes

// Tests for the identifier-addressed GET dispatch added with the
// external-feedback fixes: detail by arXiv id / DOI (A1), the qa_
// paper_id asset-endpoint resolution (A2), and the action-vs-identifier
// boundary between them. The full GET catch-all needs a live PocketBase
// router (covered by the harness tests at the bottom); the helpers
// below are driven directly through the paperCatalog seam so hosted /
// un-hosted outcomes are testable without PostgreSQL.

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// fakePaperCatalog implements the paperCatalog seam with plain maps so
// the identifier-resolution helpers can be driven without PostgreSQL.
// Identity keys are the NORMALIZED forms the helpers pass down
// (registry.NormalizeArxivID / NormalizeDOI).
type fakePaperCatalog struct {
	papers   map[string]*registry.PaperDetail // by paper_id
	identity map[string]string                // "scheme:normalized-id" -> paper_id
	latest   map[string]int                   // bare arxiv id -> highest asset version
	err      error
	// DOI probes for decideLocalDOIServing (keys are normalized DOIs).
	doiRows         map[string]bool // papers row carries this DOI
	publishedAssets map[string]bool // a published-source asset exists
}

func newFakePaperCatalog() *fakePaperCatalog {
	return &fakePaperCatalog{
		papers:          map[string]*registry.PaperDetail{},
		identity:        map[string]string{},
		latest:          map[string]int{},
		doiRows:         map[string]bool{},
		publishedAssets: map[string]bool{},
	}
}

func (f *fakePaperCatalog) GetWithAssets(_ context.Context, paperID string) (*registry.PaperDetail, bool, error) {
	if f.err != nil {
		return nil, false, f.err
	}
	d, ok := f.papers[paperID]
	return d, ok, nil
}

func (f *fakePaperCatalog) GetPaperIDByIdentity(_ context.Context, scheme, id string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	pid, ok := f.identity[scheme+":"+id]
	return pid, ok, nil
}

func (f *fakePaperCatalog) LatestArxivAssetVersion(_ context.Context, bareArxiv string) (int, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	v, ok := f.latest[bareArxiv]
	return v, ok && v > 0, nil
}

func (f *fakePaperCatalog) LookupDOI(_ context.Context, doi string) (string, bool, error) {
	if f.err != nil {
		return "", false, f.err
	}
	hit := f.doiRows[doi]
	return doi, hit, nil
}

func (f *fakePaperCatalog) HasPublishedAsset(_ context.Context, doi string) (bool, error) {
	if f.err != nil {
		return false, f.err
	}
	return f.publishedAssets[doi], nil
}

// TestIsKnownGETAction locks the action-vs-identifier boundary: exactly
// the asset dispatcher's trailing segments count as actions, everything
// else falls through to identifier parsing.
func TestIsKnownGETAction(t *testing.T) {
	cases := []struct {
		raw  string
		want bool
	}{
		{"markdown", true},
		{"2501.00010/markdown", true},
		{"quant-ph/9508027/markdown", true},
		{"2501.00010/pdf", true},
		{"2501.00010/status", true},
		{"2501.00010/markdown/status", true},
		{"2501.00010/pdf/status", true},
		{"10.1038/xxx/markdown/status", true},
		{"2501.00010/images", true},
		{"2501.00010/images/zip", true},
		{"qa_abc/images/zip", true},
		// Identifiers, not actions.
		{"2501.00010", false},
		{"2501.00010v2", false},
		{"quant-ph/9508027", false},
		{"quant-ph/9508027v1", false},
		{"9508027", false},
		{"10.22331/q-2023-03-20-955", false},
		{"garbage", false},
		{"foo/bar/baz", false},
	}
	for _, c := range cases {
		if got := isKnownGETAction(c.raw); got != c.want {
			t.Errorf("isKnownGETAction(%q) = %v, want %v", c.raw, got, c.want)
		}
	}
}

// dispatchDetail runs dispatchDetailByIdentifier against a fake catalog
// and returns (handled, recorder, decoded-body).
func dispatchDetail(t *testing.T, catalog *fakePaperCatalog, raw string) (bool, *httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/"+raw, nil)
	rec := httptest.NewRecorder()
	handled, err := dispatchDetailByIdentifier(newTestReqEvent(req, rec), catalog, raw, nil, nil)
	if err != nil {
		t.Fatalf("dispatchDetailByIdentifier(%q): %v", raw, err)
	}
	var body map[string]any
	if handled {
		if jerr := json.Unmarshal(rec.Body.Bytes(), &body); jerr != nil {
			t.Fatalf("decode body %q: %v", rec.Body.String(), jerr)
		}
	}
	return handled, rec, body
}

// TestDispatchDetailByIdentifier_Hosted covers every identifier shape a
// hosted paper can be addressed by: new-style bare / versioned,
// old-style bare / canonical / versioned, pasted arXiv: prefix, DOIs
// (bare and URL form). All reuse the paperDetailHandler response.
func TestDispatchDetailByIdentifier_Hosted(t *testing.T) {
	catalog := newFakePaperCatalog()
	catalog.papers["qa_hosted"] = &registry.PaperDetail{
		Paper: &registry.Paper{
			PaperID: "qa_hosted",
			ArxivID: "quant-ph/9508027",
			Status:  "ready",
		},
	}
	catalog.identity["arxiv:quant-ph/9508027"] = "qa_hosted"

	catalog.papers["qa_newstyle"] = &registry.PaperDetail{
		Paper: &registry.Paper{PaperID: "qa_newstyle", ArxivID: "2501.00010", Status: "pending"},
	}
	catalog.identity["arxiv:2501.00010"] = "qa_newstyle"

	catalog.papers["qa_doi"] = &registry.PaperDetail{
		Paper: &registry.Paper{PaperID: "qa_doi", DOI: "10.22331/q-2023-03-20-955", Status: "ready"},
	}
	catalog.identity["doi:10.22331/q-2023-03-20-955"] = "qa_doi"

	cases := []struct {
		raw      string
		wantBody string
	}{
		{"quant-ph/9508027", "qa_hosted"},       // old-style, split by last slash
		{"quant-ph/9508027v1", "qa_hosted"},     // old-style versioned
		{"2501.00010", "qa_newstyle"},           // new-style bare
		{"2501.00010v3", "qa_newstyle"},         // new-style versioned
		{"arXiv:2501.00010", "qa_newstyle"},     // pasted prefix form
		{"10.22331/q-2023-03-20-955", "qa_doi"}, // DOI
	}
	for _, c := range cases {
		handled, rec, body := dispatchDetail(t, catalog, c.raw)
		if !handled {
			t.Errorf("%q: not handled, want detail dispatch", c.raw)
			continue
		}
		if rec.Code != http.StatusOK {
			t.Errorf("%q: status = %d, want 200 (body=%s)", c.raw, rec.Code, rec.Body.String())
		}
		if body["paper_id"] != c.wantBody {
			t.Errorf("%q: paper_id = %v, want %q", c.raw, body["paper_id"], c.wantBody)
		}
	}
}

// TestDispatchDetailByIdentifier_NotHosted: a well-formed identifier the
// registry does not know answers 404 with a pointer at the batch lookup
// endpoint (metadata resolution), not a bare "no such paper".
func TestDispatchDetailByIdentifier_NotHosted(t *testing.T) {
	catalog := newFakePaperCatalog()
	for _, raw := range []string{"2501.00010", "quant-ph/9508027", "10.22331/q-2023-03-20-955"} {
		handled, rec, body := dispatchDetail(t, catalog, raw)
		if !handled {
			t.Fatalf("%q: not handled, want the 404-with-hint path", raw)
		}
		if rec.Code != http.StatusNotFound {
			t.Errorf("%q: status = %d, want 404", raw, rec.Code)
		}
		detail, _ := body["detail"].(string)
		if !strings.Contains(detail, "not hosted") || !strings.Contains(detail, "/api/papers/lookup?ids=") {
			t.Errorf("%q: detail = %q, want not-hosted + lookup hint", raw, detail)
		}
	}
}

// TestDispatchDetailByIdentifier_GarbageAndActions: paths that are not
// identifier-shaped (or that name an asset action) stay unhandled so the
// caller keeps its existing generic 404 / asset dispatch.
func TestDispatchDetailByIdentifier_GarbageAndActions(t *testing.T) {
	catalog := newFakePaperCatalog()
	for _, raw := range []string{
		"", "/", "garbage", "foo/bar/baz",
		"2501.00010/markdown", "quant-ph/9508027/pdf",
		"2501.00010/markdown/status", "2501.00010/images/zip",
	} {
		handled, _, _ := dispatchDetail(t, catalog, raw)
		if handled {
			t.Errorf("%q: handled, want passthrough", raw)
		}
	}
}

// TestDispatchDetailByIdentifier_CatalogUnavailable: a registry failure
// surfaces as 503 — the identifier may well be hosted, so 404 would lie.
func TestDispatchDetailByIdentifier_CatalogUnavailable(t *testing.T) {
	catalog := newFakePaperCatalog()
	catalog.err = registry.ErrCatalogUnavailable
	handled, rec, body := dispatchDetail(t, catalog, "2501.00010")
	if !handled {
		t.Fatal("not handled, want the 503 path")
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "catalog unavailable") {
		t.Errorf("detail = %q, want catalog-unavailable convention", detail)
	}
}

// TestResolvePaperAssetTarget locks the qa_ → canonical identity mapping
// used by the asset endpoints.
func TestResolvePaperAssetTarget(t *testing.T) {
	ctx := context.Background()

	catalog := newFakePaperCatalog()
	catalog.papers["qa_versions"] = &registry.PaperDetail{
		Paper: &registry.Paper{PaperID: "qa_versions", ArxivID: "2501.00010"},
		Assets: []registry.Asset{
			{AssetID: 1, Source: "arxiv", ArxivVersion: 1},
			{AssetID: 2, Source: "arxiv", ArxivVersion: 3},
			{AssetID: 3, Source: "arxiv", ArxivVersion: 2},
		},
	}
	catalog.papers["qa_noversion"] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: "qa_noversion", ArxivID: "quant-ph/9508027"},
		Assets: []registry.Asset{{AssetID: 4, Source: "arxiv"}}, // no version recorded
	}
	catalog.papers["qa_doionly"] = &registry.PaperDetail{
		Paper: &registry.Paper{PaperID: "qa_doionly", DOI: "10.22331/Q-2023-03-20-955"},
	}

	cases := []struct {
		paperID       string
		wantVersioned string
		wantBare      string
		wantDOI       string
		wantNotFound  bool
	}{
		{"qa_versions", "2501.00010v3", "2501.00010", "", false},
		{"qa_noversion", "", "quant-ph/9508027", "", false},
		{"qa_doionly", "", "", "10.22331/q-2023-03-20-955", false}, // lower-cased
		{"qa_missing", "", "", "", true},
	}
	for _, c := range cases {
		target, err := resolvePaperAssetTarget(ctx, catalog, c.paperID)
		if err != nil {
			t.Fatalf("%s: %v", c.paperID, err)
		}
		if target.ArxivVersioned != c.wantVersioned || target.ArxivBare != c.wantBare ||
			target.DOI != c.wantDOI || target.NotFound != c.wantNotFound {
			t.Errorf("%s: target = %+v, want {versioned:%q bare:%q doi:%q notFound:%v}",
				c.paperID, target, c.wantVersioned, c.wantBare, c.wantDOI, c.wantNotFound)
		}
	}
}

// ---------------------------------------------------------------------------
// Full-router dispatch (nil-pool catalog: the new branches are proven by
// their degraded responses — 503 instead of the pre-fix 404/400).
// ---------------------------------------------------------------------------

// newPapersHarness mounts the real /api/papers routes over a test PB app
// (same shape as newAgenticHarness) with a nil-pool registry store.
func newPapersHarness(t testing.TB, cfg *config.Config) *patHarness {
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
		RegisterPapers(e, cfg, nil, registry.NewStore(nil), nil, enforcer, nil, nil, nil, nil)
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

// TestAPI_PapersDetailByIdentifierDispatch proves the fallback is wired
// into the GET catch-all: an identifier-shaped path now reaches the
// registry (503 against the nil-pool store) instead of the old generic
// 404, while garbage keeps 404ing.
func TestAPI_PapersDetailByIdentifierDispatch(t *testing.T) {
	h := newPapersHarness(t, &config.Config{})
	auth := rawHeader(h.sessionToken())

	for _, path := range []string{
		"/api/papers/quant-ph/9508027",
		"/api/papers/2501.00010",
		"/api/papers/2501.00010v2",
		"/api/papers/10.22331/q-2023-03-20-955",
	} {
		status, _, body := h.do(http.MethodGet, path, "", auth)
		if status != http.StatusServiceUnavailable {
			t.Errorf("GET %s: status = %d, want 503 (nil-pool registry reached); body=%v", path, status, body)
		}
	}

	status, _, body := h.do(http.MethodGet, "/api/papers/garbage-path", "", auth)
	if status != http.StatusNotFound {
		t.Fatalf("GET garbage: status = %d, want the generic 404; body=%v", status, body)
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "no GET handler") {
		t.Errorf("detail = %q, want the generic no-handler message", detail)
	}
}

// TestAPI_PapersQAAssetEndpointsResolveThroughCatalog: with the paper
// access switch ON, qa_<id>/markdown now resolves the surrogate through
// the registry first (503 against the nil-pool store) instead of the
// pre-fix 400 "invalid arxiv_id".
func TestAPI_PapersQAAssetEndpointsResolveThroughCatalog(t *testing.T) {
	h := newPapersHarness(t, &config.Config{PaperAccessEnabled: true})
	auth := rawHeader(h.sessionToken())

	for _, path := range []string{
		"/api/papers/qa_01h5gvbpyf25hjyb9wq3v7r9ta/markdown",
		"/api/papers/qa_01h5gvbpyf25hjyb9wq3v7r9ta/markdown/status",
		"/api/papers/qa_01h5gvbpyf25hjyb9wq3v7r9ta/pdf",
		"/api/papers/qa_01h5gvbpyf25hjyb9wq3v7r9ta/images/zip",
	} {
		status, _, body := h.do(http.MethodGet, path, "", auth)
		if status != http.StatusServiceUnavailable {
			t.Errorf("GET %s: status = %d, want 503 (surrogate resolution hit the nil-pool registry); body=%v",
				path, status, body)
		}
	}
}
