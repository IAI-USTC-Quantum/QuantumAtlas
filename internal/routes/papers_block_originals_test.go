package routes

// papers_block_originals_test.go: Q1 block-originals endpoints driven by
// the synthetic Q0 fixtures (tests/fixtures/blockcomments). Every case
// in golden-anchors.json has a corresponding assertion here:
//
//   - parse-A page 1 block 2 / page 2 block 2 resolve to the pinned
//     equation content (exact-index lookups)
//   - parse-A page 1 blocks 3/4 (non-contiguous gap) 404, no fallback
//   - parse-B page 2 (page absent from that parse) 404
//   - parse-B page 1 block 7 image → 404 no_bbox (never a fake crop)
//   - same visual text in parse-A vs parse-B = two distinct anchors
//
// plus sources (v2/v3 pin semantics), parse JSON bytes, keyset
// pagination, alias/merge resolution and honest-missing behaviour.

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image/png"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pdfraster"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

const (
	fixturePaperID = "qa_01J5SYNTHETICFIXTURE0001"
	revA           = "pr_fixture_parse_a"
	revB           = "pr_fixture_parse_b"
	srcArxivV2     = "src_arxiv_v2"
	srcArxivV3     = "src_arxiv_v3"
	pdfKey         = "blockcomments/fixture/minimal-2page.pdf"
	jsonKeyA       = "blockcomments/fixture/parse-a.middle.json"
	jsonKeyB       = "blockcomments/fixture/parse-b.middle.json"
	fixtureArxiv   = "2501.09999"
	fixtureDOI     = "10.9999/qatlas.synthetic"
)

// fakeBlockCatalog fakes the registry slice the block-originals
// endpoints need, seeded from the Q0 fixtures.
type fakeBlockCatalog struct {
	papers  map[string]*registry.Paper
	byDOI   map[string]string
	byArxiv map[string]string
	sources map[string]map[string]registry.PaperSource
	parses  map[string]map[string]registry.ParseRevision
}

func newFakeBlockCatalog(t *testing.T) (*fakeBlockCatalog, objstore.Store) {
	t.Helper()
	fx := os.DirFS("../../tests/fixtures/blockcomments")
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	put := func(key, file string) []byte {
		data, err := fs.ReadFile(fx, file)
		if err != nil {
			t.Fatalf("read fixture %s: %v", file, err)
		}
		if _, err := store.Put(t.Context(), key, bytes.NewReader(data), int64(len(data)), "application/octet-stream"); err != nil {
			t.Fatalf("seed %s: %v", key, err)
		}
		return data
	}
	pdfBytes := put(pdfKey, "minimal-2page.pdf")
	jsonA := put(jsonKeyA, "parse-a.middle.json")
	jsonB := put(jsonKeyB, "parse-b.middle.json")

	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	now := time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC)

	c := &fakeBlockCatalog{
		papers: map[string]*registry.Paper{
			fixturePaperID: {PaperID: fixturePaperID, Status: "ready", ArxivID: fixtureArxiv, DOI: fixtureDOI},
		},
		byDOI:   map[string]string{fixtureDOI: fixturePaperID},
		byArxiv: map[string]string{fixtureArxiv: fixturePaperID, fixtureArxiv + "v2": fixturePaperID},
		sources: map[string]map[string]registry.PaperSource{
			fixturePaperID: {
				srcArxivV2: {SourceID: srcArxivV2, PaperID: fixturePaperID, Origin: "arxiv:v2",
					Sha256: sum(pdfBytes), ObjstoreKey: pdfKey, SizeBytes: int64(len(pdfBytes)), CreatedAt: now},
				srcArxivV3: {SourceID: srcArxivV3, PaperID: fixturePaperID, Origin: "arxiv:v3",
					Sha256: sum(pdfBytes), ObjstoreKey: pdfKey, SizeBytes: int64(len(pdfBytes)), CreatedAt: now.Add(time.Second)},
			},
		},
		parses: map[string]map[string]registry.ParseRevision{
			fixturePaperID: {
				revA: {RevisionID: revA, PaperID: fixturePaperID, SourceID: srcArxivV2,
					Schema: "docvortex.middle", SchemaVersion: "2.0",
					ArtifactSha256: sum(jsonA), ObjstoreKey: jsonKeyA, CreatedAt: now, IsCurrent: true},
				revB: {RevisionID: revB, PaperID: fixturePaperID, SourceID: srcArxivV3,
					Schema: "docvortex.middle", SchemaVersion: "2.0",
					ArtifactSha256: sum(jsonB), ObjstoreKey: jsonKeyB, CreatedAt: now.Add(2 * time.Second), IsCurrent: false},
			},
		},
	}
	return c, store
}

func (f *fakeBlockCatalog) Get(_ context.Context, paperID string) (*registry.Paper, bool, error) {
	p, ok := f.papers[paperID]
	if !ok {
		return nil, false, nil
	}
	return p, true, nil
}

func (f *fakeBlockCatalog) GetPaperIDByIdentity(_ context.Context, scheme, id string) (string, bool, error) {
	switch scheme {
	case "doi":
		pid, ok := f.byDOI[id]
		return pid, ok, nil
	case "arxiv":
		// The real store strips a vN suffix before matching.
		for _, candidate := range []string{id, strings.Split(id, "v")[0]} {
			if pid, ok := f.byArxiv[candidate]; ok {
				return pid, true, nil
			}
		}
		return "", false, nil
	}
	return "", false, nil
}

func (f *fakeBlockCatalog) ListPaperSources(_ context.Context, paperID string) ([]registry.PaperSource, error) {
	var out []registry.PaperSource
	for _, s := range f.sources[paperID] {
		out = append(out, s)
	}
	// oldest first
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (f *fakeBlockCatalog) GetPaperSource(_ context.Context, paperID, sourceID string) (registry.PaperSource, bool, error) {
	s, ok := f.sources[paperID][sourceID]
	return s, ok, nil
}

func (f *fakeBlockCatalog) ListParseRevisions(_ context.Context, paperID string) ([]registry.ParseRevision, error) {
	var out []registry.ParseRevision
	for _, r := range f.parses[paperID] {
		out = append(out, r)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j].CreatedAt.Before(out[j-1].CreatedAt); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out, nil
}

func (f *fakeBlockCatalog) GetParseRevision(_ context.Context, paperID, revisionID string) (registry.ParseRevision, bool, error) {
	r, ok := f.parses[paperID][revisionID]
	return r, ok, nil
}

// callBlockOriginals drives the dispatcher for one GET path.
func callBlockOriginals(t *testing.T, c blockCatalog, store objstore.Store, path string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	// The real router hands the dispatcher PathValue("path"), which
	// excludes the query string — mirror that here.
	raw := strings.TrimPrefix(path, "/api/papers/")
	if i := strings.IndexByte(raw, '?'); i >= 0 {
		raw = raw[:i]
	}
	handled, err := dispatchBlockOriginalsGET(re, &config.Config{}, store, c, raw)
	if !handled {
		t.Fatalf("dispatcher did not handle %q", path)
	}
	if err != nil {
		t.Fatalf("dispatch %s: %v", path, err)
	}
	var body map[string]any
	if len(rec.Body.Bytes()) > 0 && strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}

// --- sources -----------------------------------------------------------------

func TestSourcesListTwoVersions(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	rec, body := callBlockOriginals(t, c, store, "/api/papers/"+fixturePaperID+"/sources", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", rec.Code, body)
	}
	items := body["sources"].([]any)
	if len(items) != 2 {
		t.Fatalf("sources = %d, want 2 (v2 + v3 share bytes but stay distinct)", len(items))
	}
	// current parse (rev A) is bound to src_arxiv_v2.
	if got := body["current_source_id"].(string); got != srcArxivV2 {
		t.Errorf("current_source_id = %q, want %q", got, srcArxivV2)
	}
	first := items[0].(map[string]any)
	if first["source_id"] != srcArxivV2 || first["origin"] != "arxiv:v2" {
		t.Errorf("first source = %v", first)
	}
	if first["is_current"] != true {
		t.Errorf("src v2 is_current = %v, want true (carries the current parse)", first["is_current"])
	}
}

func TestSourcesAliasResolutionTraceable(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	// Request by arXiv alias — canonical paper_id must be returned and
	// the alias trail surfaced via headers (plan §4.1: 保留可追溯别名).
	rec, body := callBlockOriginals(t, c, store, "/api/papers/"+fixtureArxiv+"/sources", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if got := body["paper_id"].(string); got != fixturePaperID {
		t.Errorf("paper_id = %q, want canonical %q", got, fixturePaperID)
	}
	if got := rec.Header().Get("X-QAtlas-Requested-Id"); got != fixtureArxiv {
		t.Errorf("X-QAtlas-Requested-Id = %q", got)
	}
	if got := rec.Header().Get("X-QAtlas-Resolved-Id"); got != fixturePaperID {
		t.Errorf("X-QAtlas-Resolved-Id = %q", got)
	}
	// Same by DOI.
	rec2, body2 := callBlockOriginals(t, c, store, "/api/papers/"+fixtureDOI+"/sources", nil)
	if rec2.Code != http.StatusOK || body2["paper_id"].(string) != fixturePaperID {
		t.Errorf("DOI alias: status=%d paper_id=%v", rec2.Code, body2["paper_id"])
	}
}

func TestSourcePDFBytesAndRange(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	fxPDF, _ := os.ReadFile("../../tests/fixtures/blockcomments/minimal-2page.pdf")
	sum := sha256.Sum256(fxPDF)

	rec, _ := callBlockOriginalsRaw(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcArxivV2+"/pdf", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), fxPDF) {
		t.Error("pdf bytes differ from fixture")
	}
	got := sha256.Sum256(rec.Body.Bytes())
	if hex.EncodeToString(got[:]) != hex.EncodeToString(sum[:]) {
		t.Error("pdf sha mismatch")
	}
	if etag := rec.Header().Get("ETag"); !strings.Contains(etag, hex.EncodeToString(sum[:])) {
		t.Errorf("ETag = %q, want source sha", etag)
	}

	// Range: bytes=0-99 → 206 with the first 100 fixture bytes.
	rec2, _ := callBlockOriginalsRaw(t, c, store, "/api/papers/"+fixturePaperID+"/sources/"+srcArxivV2+"/pdf",
		map[string]string{"Range": "bytes=0-99"})
	if rec2.Code != http.StatusPartialContent {
		t.Fatalf("range status = %d, want 206", rec2.Code)
	}
	if !bytes.Equal(rec2.Body.Bytes(), fxPDF[:100]) {
		t.Error("range slice mismatch")
	}
	if cr := rec2.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes 0-99/") {
		t.Errorf("Content-Range = %q", cr)
	}
}

func TestSourcePDFVersionPinNeverSubstitutes(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	// Ask src_arxiv_v2 for v3 → 404, never the v3 source's bytes.
	rec, body := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/sources/"+srcArxivV2+"/pdf?version=v3", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(body["detail"].(string), "refusing to substitute") {
		t.Errorf("detail = %v", body["detail"])
	}
	// Matching pin passes.
	rec2, _ := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/sources/"+srcArxivV2+"/pdf?version=v2", nil)
	if rec2.Code != http.StatusOK {
		t.Fatalf("matching pin status = %d, want 200", rec2.Code)
	}
	// No version → serves the addressed source as-is.
	rec3, _ := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/sources/"+srcArxivV3+"/pdf", nil)
	if rec3.Code != http.StatusOK {
		t.Fatalf("v3 no-pin status = %d", rec3.Code)
	}
}

func TestSourcePDFMissingBytesHonest(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	// Row points at a key that was never seeded.
	c.sources[fixturePaperID][srcArxivV3] = registry.PaperSource{
		SourceID: srcArxivV3, PaperID: fixturePaperID, Origin: "arxiv:v3",
		Sha256: strings.Repeat("0", 64), ObjstoreKey: "blockcomments/fixture/ghost.pdf", SizeBytes: 1,
	}
	rec, body := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/sources/"+srcArxivV3+"/pdf", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (never a ready pointer at missing bytes)", rec.Code)
	}
	if !strings.Contains(body["detail"].(string), "missing from the store") {
		t.Errorf("detail = %v", body["detail"])
	}
}

// --- parses ------------------------------------------------------------------

func TestParsesListTwoRevisionsOneCurrent(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	rec, body := callBlockOriginals(t, c, store, "/api/papers/"+fixturePaperID+"/parses", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	items := body["parses"].([]any)
	if len(items) != 2 {
		t.Fatalf("parses = %d, want 2", len(items))
	}
	if got := body["current_revision_id"].(string); got != revA {
		t.Errorf("current_revision_id = %q, want %q", got, revA)
	}
	// Same PDF, two parses → both revisions list the same source sha?
	// No: they may bind different sources; the immutable artifact sha
	// is what distinguishes them.
	seen := map[string]bool{}
	for _, it := range items {
		m := it.(map[string]any)
		seen[m["artifact_sha256"].(string)] = true
	}
	if len(seen) != 2 {
		t.Error("two parses must have two distinct artifact hashes")
	}
}

func TestParseJSONOriginalBytes(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	fx, _ := os.ReadFile("../../tests/fixtures/blockcomments/parse-a.middle.json")
	rec, _ := callBlockOriginalsRaw(t, c, store, "/api/papers/"+fixturePaperID+"/parses/"+revA+"/json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if !bytes.Equal(rec.Body.Bytes(), fx) {
		t.Error("parse json bytes differ from fixture (must be original bytes, not re-serialized)")
	}
	sum := sha256.Sum256(fx)
	if etag := rec.Header().Get("ETag"); !strings.Contains(etag, hex.EncodeToString(sum[:])) {
		t.Errorf("ETag = %q, want artifact sha", etag)
	}
}

func TestParseJSONBadSchemaRejected(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	// Corrupt a copy: wrong schema → 422, never interpreted as blocks.
	bad := []byte(`{"schema":"other","schema_version":"9","blocks":[]}`)
	if _, err := store.Put(t.Context(), "blockcomments/fixture/bad.json", bytes.NewReader(bad), int64(len(bad)), "application/json"); err != nil {
		t.Fatalf("seed bad json: %v", err)
	}
	s := sha256.Sum256(bad)
	c.parses[fixturePaperID]["pr_bad"] = registry.ParseRevision{
		RevisionID: "pr_bad", PaperID: fixturePaperID, SourceID: srcArxivV2,
		Schema: "docvortex.middle", SchemaVersion: "2.0",
		ArtifactSha256: hex.EncodeToString(s[:]), ObjstoreKey: "blockcomments/fixture/bad.json",
	}
	rec, _ := callBlockOriginals(t, c, store, "/api/papers/"+fixturePaperID+"/parses/pr_bad/json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("json bytes still serve: %d", rec.Code)
	}
	rec2, body2 := callBlockOriginals(t, c, store, "/api/papers/"+fixturePaperID+"/parses/pr_bad/blocks", nil)
	if rec2.Code != http.StatusUnprocessableEntity {
		t.Fatalf("blocks on bad schema: status = %d, want 422", rec2.Code)
	}
	if !strings.Contains(body2["detail"].(string), "rejected") {
		t.Errorf("detail = %v", body2["detail"])
	}
}

// --- blocks (golden anchors) ---------------------------------------------------

func TestGoldenAnchor_ParseAPage1Block2(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	rec, body := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks/0/2", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", rec.Code, body)
	}
	anchor := body["anchor"].(map[string]any)
	if anchor["page_idx"].(float64) != 0 || anchor["block_index"].(float64) != 2 {
		t.Errorf("anchor = %v", anchor)
	}
	if anchor["parse_revision"].(string) != revA {
		t.Errorf("anchor revision = %v", anchor["parse_revision"])
	}
	content := body["content"].(map[string]any)
	if content["type"].(string) != "equation" {
		t.Errorf("type = %v", content["type"])
	}
	if got := content["content"].(string); got != "E = m c^{2}" {
		t.Errorf("content = %q, want the golden equation", got)
	}
	src := body["source"].(map[string]any)
	if src["pdf_sha256"] == "" || src["artifact_sha256"] == "" || src["parse_revision"].(string) != revA {
		t.Errorf("source = %v", src)
	}
	if body["discussions_ready"].(bool) {
		t.Error("discussions_ready must be false until Q2")
	}
}

func TestGoldenAnchor_ParseAPage2Block2(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	rec, body := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks/1/2", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	content := body["content"].(map[string]any)
	want := "\\sum_{i=1}^{n} i = \\frac{n(n+1)}{2}"
	if content["content"].(string) != want {
		t.Errorf("content = %q, want %q", content["content"], want)
	}
}

func TestGoldenAnchor_NonContiguousGapIs404(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	for _, missing := range []string{"0/3", "0/4"} {
		rec, body := callBlockOriginals(t, c, store,
			"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks/"+missing, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("blocks/%s status = %d, want 404 (indexes 1,2,5 — no 3/4)", missing, rec.Code)
		}
		if !strings.Contains(body["detail"].(string), "no top-level block") {
			t.Errorf("detail = %v", body["detail"])
		}
	}
	// The image endpoint must equally 404 on a gap block.
	rec, _ := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks/0/3/image", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("gap image status = %d, want 404", rec.Code)
	}
}

func TestGoldenAnchor_ParseBPage2Is404(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	for _, idx := range []string{"1", "2", "7"} {
		rec, _ := callBlockOriginals(t, c, store,
			"/api/papers/"+fixturePaperID+"/parses/"+revB+"/blocks/1/"+idx, nil)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("parse-B page2 block %s status = %d, want 404 (parse B lacks page_idx=1)", idx, rec.Code)
		}
	}
	// And the page filter on the listing shows the page simply absent.
	_, body := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revB+"/blocks?page_idx=1", nil)
	if n := len(body["blocks"].([]any)); n != 0 {
		t.Errorf("parse-B page_idx=1 blocks = %d, want 0", n)
	}
}

func TestGoldenAnchor_ParseBBlock7ImageNoBBox(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	rec, body := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revB+"/blocks/0/7/image", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 4xx (no bbox → unavailable)", rec.Code)
	}
	if body["reason"].(string) != "no_bbox" {
		t.Errorf("reason = %v, want no_bbox", body["reason"])
	}
	if !strings.Contains(body["detail"].(string), "no bbox") {
		t.Errorf("detail = %v", body["detail"])
	}
}

func TestGoldenAnchor_SameTextTwoParsesAreTwoAnchors(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	// parse-A (0,1) and parse-B (0,2) both carry "PAGE ONE TEST".
	_, a := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks/0/1", nil)
	_, b := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revB+"/blocks/0/2", nil)
	ca := a["content"].(map[string]any)
	cb := b["content"].(map[string]any)
	if ca["content"].(string) != cb["content"].(string) {
		t.Fatalf("fixture premise broken: contents differ (%q vs %q)", ca["content"], cb["content"])
	}
	aa := a["anchor"].(map[string]any)
	ab := b["anchor"].(map[string]any)
	if aa["parse_revision"] == ab["parse_revision"] {
		t.Fatal("same visual text must remain TWO distinct anchors — revisions collapsed")
	}
	// Distinct artifact identity is what keeps comments from migrating.
	if a["source"].(map[string]any)["artifact_sha256"] == b["source"].(map[string]any)["artifact_sha256"] {
		t.Fatal("two parses must pin two distinct artifact hashes")
	}
}

func TestBlocksKeysetPagination(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	// parse A order: (0,1)(0,2)(0,5)(1,1)(1,2)
	_, p1 := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks?per_page=2", nil)
	if n := len(p1["blocks"].([]any)); n != 2 {
		t.Fatalf("page1 blocks = %d, want 2", n)
	}
	cur1, _ := p1["next_cursor"].(string)
	if cur1 == "" {
		t.Fatal("next_cursor missing")
	}
	_, p2 := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks?per_page=2&cursor="+cur1, nil)
	got := blockIndexSeq(t, p2)
	if got != "0:5,1:1" {
		t.Errorf("page2 = %q, want 0:5,1:1 (keyset must skip emitted blocks)", got)
	}
	cur2, _ := p2["next_cursor"].(string)
	_, p3 := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks?per_page=2&cursor="+cur2, nil)
	got3 := blockIndexSeq(t, p3)
	if got3 != "1:2" {
		t.Errorf("page3 = %q, want 1:2", got3)
	}
	if p3["next_cursor"] != nil {
		t.Errorf("final next_cursor = %v, want null", p3["next_cursor"])
	}
	// page_idx filter + per_page within page 0.
	_, pf := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks?page_idx=0&per_page=2", nil)
	if got := blockIndexSeq(t, pf); got != "0:1,0:2" {
		t.Errorf("filtered page = %q, want 0:1,0:2", got)
	}
	// Bad cursor / bad per_page / bad page_idx → 400.
	for _, q := range []string{"cursor=zzz", "per_page=0", "per_page=abc", "page_idx=-1", "page_idx=x"} {
		rec, _ := callBlockOriginals(t, c, store,
			"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks?"+q, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("?%s status = %d, want 400", q, rec.Code)
		}
	}
}

func blockIndexSeq(t *testing.T, body map[string]any) string {
	t.Helper()
	var parts []string
	for _, it := range body["blocks"].([]any) {
		m := it.(map[string]any)
		parts = append(parts, strconv.Itoa(int(m["page_idx"].(float64)))+":"+strconv.Itoa(int(m["block_index"].(float64))))
	}
	return strings.Join(parts, ",")
}

func TestBlockImageCropFromOriginalPDF(t *testing.T) {
	if _, err := exec.LookPath(pdfraster.DefaultCommand); err != nil {
		t.Skipf("%s not installed — positive crop not exercised (recorded as 未跑项)", pdfraster.DefaultCommand)
	}
	c, store := newFakeBlockCatalog(t)
	rec, _ := callBlockOriginalsRaw(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/"+revA+"/blocks/0/1/image", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/png" {
		t.Errorf("content-type = %q", ct)
	}
	img, err := png.Decode(bytes.NewReader(rec.Body.Bytes()))
	if err != nil {
		t.Fatalf("decode crop: %v", err)
	}
	// bbox [0.1,0.08,0.55,0.12] on 1275x1650 → 574x66 (±small rasterizer variance).
	dx, dy := img.Bounds().Dx(), img.Bounds().Dy()
	if dx < 570 || dx > 580 || dy < 62 || dy > 70 {
		t.Errorf("crop size = %dx%d, want ≈574x66", dx, dy)
	}
	if rec.Header().Get("X-QAtlas-Source-Sha256") == "" {
		t.Error("crop must pin the source sha it was rendered from")
	}
}

func TestMergedAliasFollowsSurvivor(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	const oldID = "qa_0oldmergedsurVIV0Rtest1" // any 29-char-ish id
	const newer = "qa_0newersurvivingid00test1"
	c.papers[oldID] = &registry.Paper{PaperID: oldID, Status: "merged_into:" + newer}
	c.papers[newer] = &registry.Paper{PaperID: newer, Status: "ready", ArxivID: fixtureArxiv}
	c.byArxiv[fixtureArxiv] = newer
	c.sources[newer] = c.sources[fixturePaperID]
	c.parses[newer] = c.parses[fixturePaperID]

	rec, body := callBlockOriginals(t, c, store, "/api/papers/"+oldID+"/sources", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body=%v", rec.Code, body)
	}
	if got := body["paper_id"].(string); got != newer {
		t.Errorf("paper_id = %q, want merged survivor %q", got, newer)
	}
	if got := rec.Header().Get("X-QAtlas-Resolved-Id"); got != newer {
		t.Errorf("X-QAtlas-Resolved-Id = %q, want %q", got, newer)
	}
}

func TestBlockOriginalsCatalogUnavailable(t *testing.T) {
	// nil-pool registry: every entry degrades to 503, never 500/panic.
	rec, _ := callBlockOriginals(t, registry.NewStore(nil), nil,
		"/api/papers/"+fixturePaperID+"/sources", nil)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503", rec.Code)
	}
	rec2, _ := callBlockOriginals(t, registry.NewStore(nil), nil,
		"/api/papers/"+fixturePaperID+"/parses", nil)
	if rec2.Code != http.StatusServiceUnavailable {
		t.Fatalf("parses status = %d, want 503", rec2.Code)
	}
}

func TestBlockOriginalsUnknownPaperAndRevision(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	rec, _ := callBlockOriginals(t, c, store, "/api/papers/qa_0doesnotexist0000000000test/sources", nil)
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown paper status = %d, want 404", rec.Code)
	}
	rec2, _ := callBlockOriginals(t, c, store,
		"/api/papers/"+fixturePaperID+"/parses/pr_nope/json", nil)
	if rec2.Code != http.StatusNotFound {
		t.Errorf("unknown revision status = %d, want 404", rec2.Code)
	}
	// Cross-paper isolation: source under another paper.
	c2 := &fakeBlockCatalog{
		papers:  map[string]*registry.Paper{},
		byDOI:   map[string]string{},
		byArxiv: map[string]string{},
		sources: map[string]map[string]registry.PaperSource{"qa_other": c.sources[fixturePaperID]},
		parses:  map[string]map[string]registry.ParseRevision{},
	}
	c2.papers["qa_other"] = &registry.Paper{PaperID: "qa_other", Status: "ready"}
	rec3, _ := callBlockOriginals(t, c2, store, "/api/papers/qa_other/parses/"+revA+"/json", nil)
	if rec3.Code != http.StatusNotFound {
		t.Errorf("cross-paper revision status = %d, want 404", rec3.Code)
	}
}

func TestDispatcherLeavesNonBlockPathsAlone(t *testing.T) {
	c, store := newFakeBlockCatalog(t)
	for _, raw := range []string{
		fixturePaperID,
		fixturePaperID + "/images",
		fixturePaperID + "/figures",
		"needs-mineru",
		"stats",
	} {
		re := &core.RequestEvent{}
		re.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		re.Response = httptest.NewRecorder()
		handled, _ := dispatchBlockOriginalsGET(re, &config.Config{}, store, c, raw)
		if handled {
			t.Errorf("dispatcher wrongly claimed %q", raw)
		}
	}
}

// callBlockOriginalsRaw is callBlockOriginals without JSON decoding.
func callBlockOriginalsRaw(t *testing.T, c blockCatalog, store objstore.Store, path string, hdr map[string]string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	return callBlockOriginals(t, c, store, path, hdr)
}
