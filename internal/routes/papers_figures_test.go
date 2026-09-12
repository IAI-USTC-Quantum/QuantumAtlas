package routes

// Tests for the figure-index endpoint (GET /api/papers/{id}/figures) and
// the single-image download (GET /api/papers/{id}/images/{name}), driven
// directly through the paperCatalog seam + a map-backed fake store, plus
// dispatch-layer unit tests for the new action peels.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// figuresFakeStore backs Get/Stat/ListPrefix from an in-memory map so
// both the zip and the per-paper-dir image layouts can be simulated.
type figuresFakeStore struct {
	*zipFakeStore
}

func newFiguresFakeStore(objects map[string][]byte) *figuresFakeStore {
	return &figuresFakeStore{zipFakeStore: &zipFakeStore{objects: objects}}
}

func (s *figuresFakeStore) ListPrefix(_ context.Context, prefix string, limit int) ([]objstore.ObjectInfo, error) {
	var out []objstore.ObjectInfo
	for key, b := range s.objects {
		if strings.HasPrefix(key, prefix) {
			out = append(out, objstore.ObjectInfo{Key: key, Size: int64(len(b))})
			if limit > 0 && len(out) >= limit {
				break
			}
		}
	}
	return out, nil
}

func buildImagesZip(members map[string][]byte) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, b := range members {
		w, err := zw.Create(name)
		if err != nil {
			panic(err)
		}
		if _, err := w.Write(b); err != nil {
			panic(err)
		}
	}
	if err := zw.Close(); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func callFigures(t *testing.T, catalog paperCatalog, store objstore.Store, id string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/"+id+"/figures", nil)
	rec := httptest.NewRecorder()
	if err := paperFiguresHandler(newTestReqEvent(req, rec), catalog, store, id); err != nil {
		t.Fatalf("paperFiguresHandler: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

var (
	figSha1 = "3afe9563" + strings.Repeat("0", 56)
	figSha2 = "bb01" + strings.Repeat("1", 60)
	figSha3 = "cc02" + strings.Repeat("2", 60)
)

func figuresTestMarkdown() string {
	return strings.Join([]string{
		"We compare the schemes below.",
		"",
		"![](images/" + figSha1 + ".jpg)",
		"![](images/" + figSha2 + ".jpg)",
		"",
		"FIG. 1. Surface code performance. Error rate per round.",
		"",
		"![](images/" + figSha3 + ".png)",
	}, "\n")
}

// figuresArxivFixture seeds a qa_ paper with one arXiv v1 asset, its
// markdown object and a three-member images zip (two referenced, one
// unmatched), returning the catalog + store pair.
func figuresArxivFixture() (*fakePaperCatalog, *figuresFakeStore) {
	catalog := newFakePaperCatalog()
	catalog.papers["qa_fig"] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: "qa_fig", ArxivID: "2501.00010"},
		Assets: []registry.Asset{{AssetID: 1, Source: "arxiv", ArxivVersion: 1, ImageCount: 3}},
	}
	catalog.identity["arxiv:2501.00010"] = "qa_fig"

	canonical := "2501.00010v1"
	store := newFiguresFakeStore(map[string][]byte{
		paperassets.AssetKey("markdown", canonical): []byte(figuresTestMarkdown()),
		paperassets.AssetKey("images", canonical): buildImagesZip(map[string][]byte{
			figSha1 + ".jpg": []byte("jpeg-bytes-1"),
			figSha2 + ".jpg": []byte("jpeg-bytes-2"),
			figSha3 + ".png": []byte("png-bytes-3"),
		}),
	})
	return catalog, store
}

func TestPaperFiguresHandler_ZipLayout(t *testing.T) {
	catalog, store := figuresArxivFixture()
	rec, body := callFigures(t, catalog, store, "qa_fig")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body["paper_id"] != "qa_fig" || body["resolved_id"] != "2501.00010v1" || body["markdown_ready"] != true {
		t.Errorf("header fields = %v/%v/%v", body["paper_id"], body["resolved_id"], body["markdown_ready"])
	}
	if body["image_count"] != float64(3) {
		t.Errorf("image_count = %v, want 3", body["image_count"])
	}
	figs, _ := body["figures"].([]any)
	if len(figs) != 2 {
		t.Fatalf("figures = %d, want 2 (panel group + isolated image); body=%s", len(figs), rec.Body.String())
	}
	first, _ := figs[0].(map[string]any)
	if first["fig_no"] != float64(1) {
		t.Errorf("fig[0].fig_no = %v, want 1", first["fig_no"])
	}
	if !strings.HasPrefix(asString(first["caption"]), "FIG. 1.") {
		t.Errorf("fig[0].caption = %q", first["caption"])
	}
	imgs, _ := first["images"].([]any)
	if len(imgs) != 2 {
		t.Fatalf("fig[0].images = %d, want 2", len(imgs))
	}
	m0, _ := imgs[0].(map[string]any)
	if m0["name"] != figSha1+".jpg" || m0["size"] != float64(len("jpeg-bytes-1")) {
		t.Errorf("fig[0].images[0] = %v", m0)
	}
	if m0["url"] != "/api/papers/2501.00010v1/images/"+figSha1+".jpg" {
		t.Errorf("fig[0].images[0].url = %v", m0["url"])
	}
	// The third image is referenced by its own (caption-less) group, so
	// nothing is unmatched here.
	unmatched, _ := body["unmatched_images"].([]any)
	if len(unmatched) != 0 {
		t.Errorf("unmatched_images = %v, want none", unmatched)
	}
}

func TestPaperFiguresHandler_ByArxivID(t *testing.T) {
	catalog, store := figuresArxivFixture()
	rec, body := callFigures(t, catalog, store, "2501.00010v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if body["paper_id"] != "qa_fig" {
		t.Errorf("paper_id = %v, want qa_fig (identity-resolved)", body["paper_id"])
	}
	if body["markdown_ready"] != true {
		t.Errorf("markdown_ready = %v, want true", body["markdown_ready"])
	}
}

func TestPaperFiguresHandler_UnmatchedImages(t *testing.T) {
	catalog, store := figuresArxivFixture()
	// Rewrite the markdown so it references nothing.
	canonical := "2501.00010v1"
	store.objects[paperassets.AssetKey("markdown", canonical)] = []byte("# No figures here\n\nProse only.\n")
	rec, body := callFigures(t, catalog, store, "qa_fig")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if figs, _ := body["figures"].([]any); len(figs) != 0 {
		t.Errorf("figures = %v, want none", figs)
	}
	unmatched, _ := body["unmatched_images"].([]any)
	if len(unmatched) != 3 {
		t.Fatalf("unmatched_images = %d, want all 3 listed images", len(unmatched))
	}
}

func TestPaperFiguresHandler_MarkdownMissing(t *testing.T) {
	catalog, store := figuresArxivFixture()
	delete(store.objects, paperassets.AssetKey("markdown", "2501.00010v1"))
	rec, body := callFigures(t, catalog, store, "qa_fig")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (missing markdown is not a 404)", rec.Code)
	}
	if body["markdown_ready"] != false {
		t.Errorf("markdown_ready = %v, want false", body["markdown_ready"])
	}
	if figs, _ := body["figures"].([]any); len(figs) != 0 {
		t.Errorf("figures = %v, want none", figs)
	}
	if body["image_count"] != float64(3) {
		t.Errorf("image_count = %v, want 3 (images zip still listed)", body["image_count"])
	}
}

func TestPaperFiguresHandler_PaperNotFound(t *testing.T) {
	catalog, store := figuresArxivFixture()
	for _, id := range []string{"qa_missing", "2501.99999v1", "10.1103/nope.999"} {
		rec, _ := callFigures(t, catalog, store, id)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status = %d, want 404", id, rec.Code)
		}
	}
}

func TestPaperFiguresHandler_InvalidRef(t *testing.T) {
	catalog, store := figuresArxivFixture()
	rec, _ := callFigures(t, catalog, store, "not-a-paper-id")
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
}

func TestPaperFiguresHandler_CatalogUnavailable(t *testing.T) {
	catalog := newFakePaperCatalog()
	catalog.err = registry.ErrCatalogUnavailable
	rec, _ := callFigures(t, catalog, newFiguresFakeStore(map[string][]byte{}), "qa_fig")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestPaperFiguresHandler_DOIPublishedAsset(t *testing.T) {
	doi := "10.1103/physrevlett.123.070501"
	catalog := newFakePaperCatalog()
	catalog.papers["qa_pub"] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: "qa_pub", DOI: doi},
		Assets: []registry.Asset{{AssetID: 9, Source: "published", ImageCount: 1}},
	}
	catalog.identity["doi:"+doi] = "qa_pub"

	mdKey := paperassets.DOIAssetKey("markdown", doi)
	zipKey := paperassets.DOIAssetKey("images", doi)
	store := newFiguresFakeStore(map[string][]byte{
		mdKey:  []byte("![](images/" + figSha1 + ".jpg)\n\nFIG. 4. Published figure.\n"),
		zipKey: buildImagesZip(map[string][]byte{figSha1 + ".jpg": []byte("doi-img")}),
	})

	rec, body := callFigures(t, catalog, store, doi)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if body["resolved_id"] != doi {
		t.Errorf("resolved_id = %v, want %q", body["resolved_id"], doi)
	}
	figs, _ := body["figures"].([]any)
	if len(figs) != 1 {
		t.Fatalf("figures = %d, want 1", len(figs))
	}
	if figs[0].(map[string]any)["fig_no"] != float64(4) {
		t.Errorf("fig_no = %v, want 4", figs[0].(map[string]any)["fig_no"])
	}
}

func TestPaperFiguresHandler_DirLayout(t *testing.T) {
	catalog := newFakePaperCatalog()
	catalog.papers["qa_dir"] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: "qa_dir", ArxivID: "2501.00010"},
		Assets: []registry.Asset{{AssetID: 1, Source: "arxiv", ArxivVersion: 1}},
	}
	canonical := "2501.00010v1"
	prefix := strings.TrimSuffix(paperassets.AssetKey("images", canonical), ".zip") + "/"
	store := newFiguresFakeStore(map[string][]byte{
		paperassets.AssetKey("markdown", canonical): []byte("Prose.\n\n![](images/" + figSha1 + ".jpg)\n\nFIG. 2. Dir layout.\n"),
		prefix + figSha1 + ".jpg":                   []byte("dir-image"),
	})
	rec, body := callFigures(t, catalog, store, "qa_dir")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if body["image_count"] != float64(1) {
		t.Errorf("image_count = %v, want 1 (dir listing)", body["image_count"])
	}
	figs, _ := body["figures"].([]any)
	if len(figs) != 1 {
		t.Fatalf("figures = %d, want 1", len(figs))
	}
	imgs := figs[0].(map[string]any)["images"].([]any)
	if imgs[0].(map[string]any)["size"] != float64(len("dir-image")) {
		t.Errorf("size = %v, want %d", imgs[0].(map[string]any)["size"], len("dir-image"))
	}
}

// ---------------------------------------------------------------------------
// Single-image download
// ---------------------------------------------------------------------------

func callImageGet(t *testing.T, catalog paperCatalog, store objstore.Store, id, name string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/"+id+"/images/"+name, nil)
	rec := httptest.NewRecorder()
	if err := paperImageGetHandler(newTestReqEvent(req, rec), catalog, store, id, name); err != nil {
		t.Fatalf("paperImageGetHandler: %v", err)
	}
	return rec
}

func TestPaperImageGetHandler_ZipMember(t *testing.T) {
	catalog, store := figuresArxivFixture()
	rec := callImageGet(t, catalog, store, "qa_fig", figSha2+".jpg")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Body.String(); got != "jpeg-bytes-2" {
		t.Errorf("body = %q, want the member bytes", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type = %q, want image/jpeg", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q", cc)
	}
}

func TestPaperImageGetHandler_DirLayout(t *testing.T) {
	catalog := newFakePaperCatalog()
	catalog.papers["qa_dir2"] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: "qa_dir2", ArxivID: "2501.00010"},
		Assets: []registry.Asset{{AssetID: 1, Source: "arxiv", ArxivVersion: 1}},
	}
	catalog.identity["arxiv:2501.00010"] = "qa_dir2"
	prefix := strings.TrimSuffix(paperassets.AssetKey("images", "2501.00010v1"), ".zip") + "/"
	store := newFiguresFakeStore(map[string][]byte{prefix + figSha3 + ".png": []byte("dir-png")})

	rec := callImageGet(t, catalog, store, "2501.00010v1", figSha3+".png")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if rec.Body.String() != "dir-png" {
		t.Errorf("body = %q", rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("Content-Type = %q, want image/png", ct)
	}
}

func TestPaperImageGetHandler_NotFound(t *testing.T) {
	catalog, store := figuresArxivFixture()
	rec := callImageGet(t, catalog, store, "qa_fig", strings.Repeat("9", 64)+".jpg")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestPaperImageGetHandler_BadName400(t *testing.T) {
	catalog, store := figuresArxivFixture()
	for _, name := range []string{"../../etc/passwd", "notasha.jpg", figSha1 + ".svg", strings.ToUpper(figSha1) + ".jpg"} {
		rec := callImageGet(t, catalog, store, "qa_fig", name)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("name %q: status = %d, want 400", name, rec.Code)
		}
	}
}

func TestPeelImagesMemberAction(t *testing.T) {
	name := figSha1 + ".jpg"
	cases := []struct {
		raw, wantID, wantAction string
	}{
		{"2501.00010v1/images/" + name, "2501.00010v1", "images/" + name},
		{"quant-ph/9508027v2/images/" + name, "quant-ph/9508027v2", "images/" + name},
		{"10.1103/x/y/images/" + name, "10.1103/x/y", "images/" + name},
		// Non-member paths pass through untouched.
		{"2501.00010v1/images/zip", "2501.00010v1/images", "zip"}, // handled by the zip peel first
		{"2501.00010v1/markdown", "2501.00010v1", "markdown"},
		{"2501.00010v1/images/not-a-sha.jpg", "2501.00010v1/images", "not-a-sha.jpg"},
	}
	for _, c := range cases {
		id, action := splitPapersPath(c.raw)
		id, action = peelImagesMemberAction(id, action)
		if id != c.wantID || action != c.wantAction {
			t.Errorf("peel(%q) = (%q,%q), want (%q,%q)", c.raw, id, action, c.wantID, c.wantAction)
		}
	}
}

func TestSplitQAImagesMember(t *testing.T) {
	if id, name, ok := splitQAImagesMember("qa_01h5/images/" + figSha1 + ".jpg"); !ok || id != "qa_01h5" || name != figSha1+".jpg" {
		t.Errorf("split = (%q,%q,%v), want the qa_ three-segment form", id, name, ok)
	}
	for _, raw := range []string{
		"qa_01h5/images", "qa_01h5", "qa_01h5/figures",
		"qa_01h5/images/zip", // the images-zip action, not a member
		"2501.00010v1/images/x.jpg",
		"qa_01h5/images/not-a-sha.jpg",
	} {
		if _, _, ok := splitQAImagesMember(raw); ok {
			t.Errorf("split(%q) = ok, want false", raw)
		}
	}
}

func TestIsKnownGETAction_NewActions(t *testing.T) {
	for _, raw := range []string{
		"2501.00010v1/figures",
		"qa_abc/figures",
		"10.1103/x/figures",
		"2501.00010v1/images/" + figSha1 + ".jpg",
	} {
		if !isKnownGETAction(raw) {
			t.Errorf("isKnownGETAction(%q) = false, want true", raw)
		}
	}
}

// ---------------------------------------------------------------------------
// Batch status
// ---------------------------------------------------------------------------

func callStatusBatch(t *testing.T, catalog paperCatalog, store objstore.Store, query string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/status/batch?ids="+url.QueryEscape(query), nil)
	rec := httptest.NewRecorder()
	if err := paperStatusBatchHandler(newTestReqEvent(req, rec), catalog, store, nil); err != nil {
		t.Fatalf("paperStatusBatchHandler: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return rec, body
}

func TestPaperStatusBatchHandler_HitAndMiss(t *testing.T) {
	catalog := newFakePaperCatalog()
	catalog.papers["qa_hit"] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: "qa_hit", ArxivID: "2501.00010"},
		Assets: []registry.Asset{{AssetID: 1, Source: "arxiv", ArxivVersion: 1, ImageCount: 2}},
	}
	catalog.identity["arxiv:2501.00010"] = "qa_hit"
	catalog.latest["2501.00010"] = 1
	canonical := "2501.00010v1"
	store := newFiguresFakeStore(map[string][]byte{
		paperassets.AssetKey("markdown", canonical): []byte("# md"),
		paperassets.AssetKey("pdf", canonical):      []byte("%PDF-"),
	})

	rec, body := callStatusBatch(t, catalog, store, "2501.00010, 2501.00010, qa_hit, 2401.99999v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	results, _ := body["results"].([]any)
	if len(results) != 3 { // de-duped
		t.Fatalf("results = %d, want 3 (dedupe); body=%s", len(results), rec.Body.String())
	}

	r0 := results[0].(map[string]any)
	if r0["resolved_id"] != canonical {
		t.Errorf("results[0].resolved_id = %v, want %q (bare id pinned to latest asset version)", r0["resolved_id"], canonical)
	}
	if r0["md_ready"] != true || r0["pdf_ready"] != true {
		t.Errorf("results[0] readiness = %v/%v, want true/true", r0["md_ready"], r0["pdf_ready"])
	}
	if r0["phase"] != "ready" || r0["state"] != "cached" {
		t.Errorf("results[0] phase/state = %v/%v, want ready/cached", r0["phase"], r0["state"])
	}
	if r0["image_count"] != float64(2) {
		t.Errorf("results[0].image_count = %v, want 2", r0["image_count"])
	}

	r1 := results[1].(map[string]any)
	// The qa_ surrogate resolves onto the paper's serving identity.
	if r1["resolved_id"] != "2501.00010v1" || r1["md_ready"] != true {
		t.Errorf("results[1] = %v, want the surrogate resolved with md_ready", r1)
	}

	r2 := results[2].(map[string]any)
	if r2["error"] != "not found" {
		t.Errorf("results[2].error = %v, want 'not found'", r2["error"])
	}
}

func TestPaperStatusBatchHandler_OverLimit(t *testing.T) {
	catalog := newFakePaperCatalog()
	ids := make([]string, maxStatusBatchIDs+1)
	for i := range ids {
		ids[i] = fmt.Sprintf("2501.%05dv1", i)
	}
	rec, _ := callStatusBatch(t, catalog, newFiguresFakeStore(nil), strings.Join(ids, ","))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
}

func TestPaperStatusBatchHandler_Empty(t *testing.T) {
	rec, _ := callStatusBatch(t, newFakePaperCatalog(), newFiguresFakeStore(nil), ",,")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 for empty ids", rec.Code)
	}
}

func TestPaperStatusBatchHandler_VersionedInputStaysTyped(t *testing.T) {
	catalog := newFakePaperCatalog()
	catalog.papers["qa_v1"] = &registry.PaperDetail{
		Paper:  &registry.Paper{PaperID: "qa_v1", ArxivID: "2501.00010"},
		Assets: []registry.Asset{{AssetID: 1, Source: "arxiv", ArxivVersion: 2}},
	}
	catalog.identity["arxiv:2501.00010"] = "qa_v1"
	catalog.latest["2501.00010"] = 2

	// Explicit v1 input stays v1 even though v2 is the latest asset.
	rec, body := callStatusBatch(t, catalog, newFiguresFakeStore(nil), "2501.00010v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	r := body["results"].([]any)[0].(map[string]any)
	if r["resolved_id"] != "2501.00010v1" {
		t.Errorf("resolved_id = %v, want the typed 2501.00010v1", r["resolved_id"])
	}
}

func TestPaperStatusBatchHandler_CatalogUnavailablePerEntry(t *testing.T) {
	catalog := newFakePaperCatalog()
	catalog.err = registry.ErrCatalogUnavailable
	rec, body := callStatusBatch(t, catalog, newFiguresFakeStore(nil), "2501.00010v1")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (errors are per-entry)", rec.Code)
	}
	r := body["results"].([]any)[0].(map[string]any)
	if r["error"] != "catalog unavailable" {
		t.Errorf("error = %v, want 'catalog unavailable'", r["error"])
	}
}

// Compile-time interface checks for the fakes used above.
var (
	_ objstore.Store = (*figuresFakeStore)(nil)
	_ paperCatalog   = (*fakePaperCatalog)(nil)
	_                = time.Second
)

// ---------------------------------------------------------------------------
// Full-router smoke: the new GET branches are wired into the catch-all.
// The nil-pool catalog proves reachability via its degraded 503/400
// answers instead of the pre-wiring generic 404.
// ---------------------------------------------------------------------------

func TestAPI_PapersNewGetBranchesDispatch(t *testing.T) {
	h := newPapersHarness(t, &config.Config{})
	auth := rawHeader(h.sessionToken())

	// status/batch with an empty ids list reaches the batch handler (400),
	// not the generic no-handler 404.
	status, _, body := h.do(http.MethodGet, "/api/papers/status/batch", "", auth)
	if status != http.StatusBadRequest {
		t.Fatalf("GET status/batch: status = %d, want 400 (batch handler); body=%v", status, body)
	}

	// qa_ figures resolves through the registry (nil pool → 503).
	status, _, _ = h.do(http.MethodGet, "/api/papers/qa_01h5gvbpyf25hjyb9wq3v7r9ta/figures", "", auth)
	if status != http.StatusServiceUnavailable {
		t.Errorf("GET qa_/figures: status = %d, want 503 (registry reached)", status)
	}

	// qa_ images/<name> with a whitelisted name resolves through the
	// registry the same way.
	status, _, _ = h.do(http.MethodGet, "/api/papers/qa_01h5gvbpyf25hjyb9wq3v7r9ta/images/"+figSha1+".jpg", "", auth)
	if status != http.StatusServiceUnavailable {
		t.Errorf("GET qa_/images/<name>: status = %d, want 503 (registry reached)", status)
	}
}

func TestAPI_PapersFiguresDispatchBehindAccessSwitch(t *testing.T) {
	// With paper access OFF, the arxiv-form figures action is not
	// dispatched: the path falls to the generic 404 (identifier dispatch
	// skips it because "figures" is a known action).
	h := newPapersHarness(t, &config.Config{})
	auth := rawHeader(h.sessionToken())
	status, _, body := h.do(http.MethodGet, "/api/papers/2501.00010v1/figures", "", auth)
	if status != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 with access off", status)
	}
	if detail, _ := body["detail"].(string); !strings.Contains(detail, "no GET handler") {
		t.Errorf("detail = %q, want the generic no-handler message", detail)
	}

	// With paper access ON, the same path reaches the registry (nil pool
	// → 503 via identifier→asset dispatch).
	h2 := newPapersHarness(t, &config.Config{PaperAccessEnabled: true})
	status, _, _ = h2.do(http.MethodGet, "/api/papers/2501.00010v1/figures", "", auth)
	if status != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 (asset dispatch reached the registry)", status)
	}
}
