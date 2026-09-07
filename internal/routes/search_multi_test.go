// HTTP-layer tests for the multi-search surface: POST /api/search/multi,
// GET /api/search/backends and the /api/me/search-keys CRUD. The harness
// mirrors agenticHarness (one mux, do() helper); the qatlas-search
// microservice is faked so the whole flow runs offline.

package routes

import (
	"context"
	"net/http"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/userkeys"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
)

// fakeMultiBackend stands in for the qatlas-search microservice,
// capturing what the handlers forwarded.
type fakeMultiBackend struct {
	lastQuery   string
	lastMax     int
	lastSources []string
	lastKeys    map[string]string

	multiResp search.RemoteMultiResponse
	multiErr  error

	listResp []search.RemoteBackendMeta
	listErr  error
}

func (f *fakeMultiBackend) SearchMulti(_ context.Context, query string, maxResults int, sources []string, apiKeys map[string]string) (search.RemoteMultiResponse, error) {
	f.lastQuery, f.lastMax, f.lastSources, f.lastKeys = query, maxResults, sources, apiKeys
	return f.multiResp, f.multiErr
}

func (f *fakeMultiBackend) ListBackends(context.Context) ([]search.RemoteBackendMeta, error) {
	return f.listResp, f.listErr
}

// newMultiHarness mounts the multi search + backends + search-keys routes
// over a real test PB app, with a userkeys store keyed by a fixed secret.
// engine / catalog drive the resolve-or-mint + summary-enrichment path
// (nil registry inside the engine = minting disabled; nil catalog = the
// hosting summary is omitted) without needing PostgreSQL.
type multiHarness struct {
	*patHarness
	keys *userkeys.Store
}

func newMultiHarness(t testing.TB, backend MultiBackend, engine *search.Engine, catalog *registry.Store) *multiHarness {
	t.Helper()
	h := &multiHarness{patHarness: &patHarness{t: t}}

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	h.app = app
	h.keys = userkeys.NewStore(app, "multi-test-system-secret")

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
		RegisterSearchMulti(e, h.keys, backend, engine, catalog, enforcer)
		RegisterSearchBackends(e, h.keys, backend)
		RegisterMeSearchKeys(e, h.keys)
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

func TestAPI_SearchMulti_RejectsAnonymous(t *testing.T) {
	h := newMultiHarness(t, &fakeMultiBackend{}, search.NewEngine(nil, nil), nil)
	status, _, _ := h.do(http.MethodPost, "/api/search/multi", `{"text":"x","sources":["arxiv"]}`, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestAPI_SearchMulti_NoBackend(t *testing.T) {
	h := newMultiHarness(t, nil, nil, nil)
	status, _, body := h.do(http.MethodPost, "/api/search/multi", `{"text":"x","sources":["arxiv"]}`, rawHeader(h.sessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body=%v", status, body)
	}
	if asString(body["detail"]) != "multi search requires the qatlas-search microservice (search.remote)" {
		t.Errorf("detail = %q", body["detail"])
	}
}

func TestAPI_SearchMulti_ValidatesBody(t *testing.T) {
	h := newMultiHarness(t, &fakeMultiBackend{}, search.NewEngine(nil, nil), nil)
	hdr := rawHeader(h.sessionToken())
	for _, tc := range []struct{ name, body string }{
		{"no text", `{"sources":["arxiv"]}`},
		{"no sources", `{"text":"x"}`},
		{"blank sources", `{"text":"x","sources":["  ",""]}`},
	} {
		status, _, _ := h.do(http.MethodPost, "/api/search/multi", tc.body, hdr)
		if status != http.StatusBadRequest {
			t.Fatalf("%s: status = %d, want 400", tc.name, status)
		}
	}
}

func TestAPI_SearchMulti_ForwardsSourcesAndUserKeys(t *testing.T) {
	fake := &fakeMultiBackend{
		multiResp: search.RemoteMultiResponse{
			Results: map[string][]search.RemoteHit{
				"ieee": {{Title: "An IEEE paper", Source: "ieee"}},
			},
			Errors: map[string]string{"tavily": "HTTPError: 401"},
		},
	}
	h := newMultiHarness(t, fake, search.NewEngine(nil, nil), nil)

	// Store a user key for a key-requiring backend first (dashboard flow).
	status, _, _ := h.do(http.MethodPut, "/api/me/search-keys/ieee", `{"key":"ieee-user-key-99"}`, rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("seed key: status = %d", status)
	}

	body := `{"text":"quantum error correction","max_results":7,"sources":["ieee","arxiv"]}`
	status, _, resp := h.do(http.MethodPost, "/api/search/multi", body, rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d; body=%v", status, resp)
	}
	if fake.lastQuery != "quantum error correction" || fake.lastMax != 7 {
		t.Fatalf("forwarded query/max = %q/%d", fake.lastQuery, fake.lastMax)
	}
	if len(fake.lastSources) != 2 || fake.lastSources[0] != "ieee" || fake.lastSources[1] != "arxiv" {
		t.Fatalf("forwarded sources = %v", fake.lastSources)
	}
	if fake.lastKeys == nil || fake.lastKeys["ieee"] != "ieee-user-key-99" || len(fake.lastKeys) != 1 {
		t.Fatalf("forwarded keys = %v (only requested backends' keys)", fake.lastKeys)
	}
	if resp["remote"] != true {
		t.Errorf("remote flag = %v", resp["remote"])
	}
	results := resp["results"].(map[string]any)
	if len(results) != 1 || results["ieee"].([]any)[0].(map[string]any)["title"] != "An IEEE paper" {
		t.Fatalf("results = %v", results)
	}
	errs := resp["errors"].(map[string]any)
	if errs["tavily"] != "HTTPError: 401" {
		t.Errorf("errors = %v", errs)
	}
}

func TestAPI_SearchMulti_UpstreamFailure(t *testing.T) {
	fake := &fakeMultiBackend{}
	fake.multiErr = context.DeadlineExceeded
	h := newMultiHarness(t, fake, search.NewEngine(nil, nil), nil)
	status, _, body := h.do(http.MethodPost, "/api/search/multi", `{"text":"x","sources":["arxiv"]}`, rawHeader(h.sessionToken()))
	if status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body=%v", status, body)
	}
}

// TestMultiAnchors_BackfillsAcrossBackends covers the server-side
// enrichment mapping: mint results anchor by DOI (arXiv as fallback), so
// every backend's hit carrying the same identity backfills the SAME
// paper_id; title-only hits and identities without a summary entry keep
// the fields omitted. The populated-paper path needs a live Postgres
// (see the registry integration suite); the mapping itself is pure.
func TestMultiAnchors_BackfillsAcrossBackends(t *testing.T) {
	// What the engine returns for the deduped hits: the DOI+arXiv hit
	// anchors by DOI (minted here), the arXiv-only twin resolves to the
	// same paper (created=false); qa_nomd has no summary entry.
	results := []search.Result{
		{PaperID: "qa_doi", Created: true, Hit: search.Hit{DOI: "10.1234/qa", ArxivID: "2501.03424", Title: "Twin identities"}},
		{PaperID: "qa_doi", Created: false, Hit: search.Hit{ArxivID: "2501.03424", Title: "arXiv-only twin"}},
		{PaperID: "qa_nomd", Created: false, Hit: search.Hit{DOI: "10.5555/no-summary"}},
	}
	resp := search.RemoteMultiResponse{Results: map[string][]search.RemoteHit{
		"arxiv": {
			{Title: "Twin identities", DOI: "10.1234/qa", ArxivID: "2501.03424", Source: "arxiv"},
			{Title: "No identity here", Source: "arxiv"},
		},
		"crossref": {
			{Title: "Twin identities (again)", DOI: "10.1234/qa", Source: "crossref"},
			{Title: "arXiv-only twin", ArxivID: "2501.03424", Source: "crossref"},
			{Title: "No summary for me", DOI: "10.5555/no-summary", Source: "crossref"},
		},
	}}
	attachMultiAnchors(resp.Results, multiAnchors(results), map[string]registry.PaperSummary{
		"qa_doi": {Status: "ready", HasMD: true},
	})

	// DOI-anchored hit: full enrichment, same paper on both backends.
	arxiv0 := resp.Results["arxiv"][0]
	if arxiv0.PaperID != "qa_doi" || !arxiv0.Created {
		t.Errorf("arxiv[0] = %+v, want qa_doi created", arxiv0)
	}
	if arxiv0.HasMD == nil || !*arxiv0.HasMD || arxiv0.Status != "ready" {
		t.Errorf("arxiv[0] summary = %+v, want ready+has_md", arxiv0)
	}
	crossref0 := resp.Results["crossref"][0]
	if crossref0.PaperID != "qa_doi" || !crossref0.Created || crossref0.Status != "ready" {
		t.Errorf("crossref[0] = %+v, want the same qa_doi anchor", crossref0)
	}
	// arXiv-only twin of the same paper: same paper_id, resolved not
	// minted (created omitted on the wire).
	crossref1 := resp.Results["crossref"][1]
	if crossref1.PaperID != "qa_doi" || crossref1.Created {
		t.Errorf("crossref[1] = %+v, want qa_doi resolved", crossref1)
	}
	// Title-only hit: no anchor at all.
	if hit := resp.Results["arxiv"][1]; hit.PaperID != "" || hit.Created || hit.HasMD != nil || hit.Status != "" {
		t.Errorf("title-only hit = %+v, want no enrichment", hit)
	}
	// Summary miss: anchored but has_md/status omitted.
	nomd := resp.Results["crossref"][2]
	if nomd.PaperID != "qa_nomd" || nomd.HasMD != nil || nomd.Status != "" {
		t.Errorf("summary-miss hit = %+v, want paper_id only", nomd)
	}
}

// TestAPI_SearchMulti_EnrichmentOmittedWithoutRegistry: with minting
// disabled (nil registry inside the engine) and no catalog, identity
// hits still answer 200 with the raw fields — no paper_id / created /
// has_md / status keys anywhere in the body.
func TestAPI_SearchMulti_EnrichmentOmittedWithoutRegistry(t *testing.T) {
	fake := &fakeMultiBackend{
		multiResp: search.RemoteMultiResponse{
			Results: map[string][]search.RemoteHit{
				"arxiv":  {{Title: "Anchored", DOI: "10.1234/qa", Source: "arxiv"}},
				"web":    {{Title: "Title only", Source: "wikipedia"}},
			},
		},
	}
	h := newMultiHarness(t, fake, search.NewEngine(nil, nil), nil)
	status, raw, resp := h.do(http.MethodPost, "/api/search/multi", `{"text":"x","sources":["arxiv","web"]}`, rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d; body=%s", status, raw)
	}
	results := resp["results"].(map[string]any)
	if results["arxiv"].([]any)[0].(map[string]any)["title"] != "Anchored" {
		t.Fatalf("raw hits lost: %s", raw)
	}
	for _, key := range []string{"paper_id", "created", "has_md", "status"} {
		if containsSubstr(string(raw), `"`+key+`"`) {
			t.Errorf("body must omit %q without a registry: %s", key, raw)
		}
	}
}

// TestAPI_SearchMulti_MintFailureKeepsResponse: a failing mint (nil-pool
// registry store → ErrCatalogUnavailable) is best-effort — the response
// stays 200 with the raw hits and no enrichment fields.
func TestAPI_SearchMulti_MintFailureKeepsResponse(t *testing.T) {
	fake := &fakeMultiBackend{
		multiResp: search.RemoteMultiResponse{
			Results: map[string][]search.RemoteHit{
				"arxiv": {{Title: "Anchored", ArxivID: "2501.03424", Source: "arxiv"}},
			},
		},
	}
	h := newMultiHarness(t, fake, search.NewEngine(registry.NewStore(nil), nil), registry.NewStore(nil))
	status, raw, resp := h.do(http.MethodPost, "/api/search/multi", `{"text":"x","sources":["arxiv"]}`, rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200 despite mint failure; body=%s", status, raw)
	}
	results := resp["results"].(map[string]any)
	if results["arxiv"].([]any)[0].(map[string]any)["title"] != "Anchored" {
		t.Fatalf("raw hits lost: %s", raw)
	}
	if containsSubstr(string(raw), `"paper_id"`) || containsSubstr(string(raw), `"status"`) {
		t.Errorf("body must omit enrichment after a mint failure: %s", raw)
	}
}

func TestAPI_SearchBackends_MergesLiveAndUserKeys(t *testing.T) {
	fake := &fakeMultiBackend{
		listResp: []search.RemoteBackendMeta{
			{Name: "arxiv", Label: "arXiv", Category: "academic", Available: true},
			{Name: "ieee", Label: "IEEE Xplore", Category: "academic", RequiresKey: true, UserKey: true, Available: false},
			{Name: "totally_new", Label: "Brand New", Category: "web", UserKey: true, Available: true},
		},
	}
	h := newMultiHarness(t, fake, search.NewEngine(nil, nil), nil)

	status, _, _ := h.do(http.MethodPut, "/api/me/search-keys/ieee", `{"key":"ieee-user-key-99"}`, rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("seed key: status = %d", status)
	}

	status, _, resp := h.do(http.MethodGet, "/api/search/backends", "", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if resp["remote"] != true || resp["keys_enabled"] != true {
		t.Fatalf("flags = %v/%v", resp["remote"], resp["keys_enabled"])
	}
	entries := resp["backends"].([]any)
	byName := map[string]map[string]any{}
	for _, e := range entries {
		m := e.(map[string]any)
		byName[m["name"].(string)] = m
	}
	// Keyless backend, server-ready -> selectable.
	if byName["arxiv"]["server_ready"] != true || byName["arxiv"]["selectable"] != true {
		t.Errorf("arxiv = %v", byName["arxiv"])
	}
	// Key-requiring, not server-ready, but the user stored a key -> selectable.
	if byName["ieee"]["key_configured"] != true || byName["ieee"]["selectable"] != true {
		t.Errorf("ieee = %v", byName["ieee"])
	}
	// Key-requiring, no key anywhere -> NOT selectable (checkbox disabled).
	if byName["tavily"]["selectable"] != false || byName["tavily"]["requires_key"] != true {
		t.Errorf("tavily = %v", byName["tavily"])
	}
	// A static entry the live list didn't mention degrades to server_ready=false.
	if byName["crossref"]["server_ready"] != false || byName["crossref"]["selectable"] != false {
		t.Errorf("crossref = %v", byName["crossref"])
	}
	// Live-only backends are appended, not dropped.
	if byName["totally_new"] == nil {
		t.Error("live-only backend missing from merge")
	}
}

func TestAPI_SearchBackends_NoBackendIsEmpty(t *testing.T) {
	h := newMultiHarness(t, nil, nil, nil)
	status, _, resp := h.do(http.MethodGet, "/api/search/backends", "", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if resp["remote"] != false {
		t.Errorf("remote = %v, want false", resp["remote"])
	}
	if len(resp["backends"].([]any)) != 0 {
		t.Errorf("backends = %v, want empty", resp["backends"])
	}
}

func TestAPI_SearchBackends_RejectsAnonymous(t *testing.T) {
	h := newMultiHarness(t, &fakeMultiBackend{}, search.NewEngine(nil, nil), nil)
	status, _, _ := h.do(http.MethodGet, "/api/search/backends", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
}

func TestAPI_MeSearchKeys_CRUD(t *testing.T) {
	h := newMultiHarness(t, &fakeMultiBackend{}, search.NewEngine(nil, nil), nil)
	hdr := rawHeader(h.sessionToken())

	// Unknown backend / no user-key slot -> 400.
	status, _, body := h.do(http.MethodPut, "/api/me/search-keys/nope", `{"key":"k"}`, hdr)
	if status != http.StatusBadRequest {
		t.Fatalf("unknown backend: status = %d; body=%v", status, body)
	}
	status, _, _ = h.do(http.MethodPut, "/api/me/search-keys/arxiv", `{"key":"k"}`, hdr)
	if status != http.StatusBadRequest {
		t.Fatalf("keyless backend: status = %d, want 400", status)
	}

	// Empty key -> 400.
	status, _, _ = h.do(http.MethodPut, "/api/me/search-keys/ieee", `{"key":"  "}`, hdr)
	if status != http.StatusBadRequest {
		t.Fatalf("empty key: status = %d, want 400", status)
	}

	// Anonymous -> 401 on every verb.
	status, _, _ = h.do(http.MethodGet, "/api/me/search-keys", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("anonymous list: status = %d", status)
	}

	// Put + list + delete round trip.
	status, _, _ = h.do(http.MethodPut, "/api/me/search-keys/ieee", `{"key":"ieee-user-key-99"}`, hdr)
	if status != http.StatusOK {
		t.Fatalf("put: status = %d", status)
	}
	status, _, resp := h.do(http.MethodGet, "/api/me/search-keys", "", hdr)
	if status != http.StatusOK {
		t.Fatalf("list: status = %d", status)
	}
	if resp["enabled"] != true {
		t.Fatalf("enabled = %v", resp["enabled"])
	}
	keys := resp["keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("keys = %v", keys)
	}
	entry := keys[0].(map[string]any)
	if entry["backend"] != "ieee" || entry["hint"] != "••••y-99" {
		t.Fatalf("entry = %v", entry)
	}

	status, _, _ = h.do(http.MethodDelete, "/api/me/search-keys/ieee", "", hdr)
	if status != http.StatusOK {
		t.Fatalf("delete: status = %d", status)
	}
	status, _, _ = h.do(http.MethodDelete, "/api/me/search-keys/ieee", "", hdr)
	if status != http.StatusNotFound {
		t.Fatalf("second delete: status = %d, want 404", status)
	}
}
