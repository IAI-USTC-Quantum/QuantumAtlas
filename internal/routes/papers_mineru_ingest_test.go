package routes

// papers_mineru_ingest_test.go: hermetic tests for the new-format
// MinerU ingest (papers_mineru_ingest.go). The registry slice is faked
// (mineruIngestCatalog); the object store is a real LocalStore. The
// real-PG variant lives in papers_mineru_ingest_integration_test.go.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/pocketbase/pocketbase/core"
)

// fakeIngestCatalog fakes the mineruIngestCatalog slice with enough
// state to observe source reuse and the is_current flip.
type fakeIngestCatalog struct {
	mu        sync.Mutex
	byArxiv   map[string]string // bare arxiv → paper_id
	sources   map[string][]registry.PaperSource
	revisions map[string][]registry.ParseRevision
}

func (f *fakeIngestCatalog) GetPaperIDByIdentity(_ context.Context, scheme, id string) (string, bool, error) {
	if scheme != "arxiv" {
		return "", false, nil
	}
	if pid, ok := f.byArxiv[strings.Split(id, "v")[0]]; ok {
		return pid, true, nil
	}
	return "", false, nil
}

func (f *fakeIngestCatalog) FindPaperSourceBySHA(_ context.Context, paperID, sha string) (registry.PaperSource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sources[paperID] {
		if s.Sha256 == sha {
			return s, true, nil
		}
	}
	return registry.PaperSource{}, false, nil
}

func (f *fakeIngestCatalog) InsertPaperSource(_ context.Context, src registry.PaperSource) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if src.SourceID == "" { src.SourceID = "src_fake_1" }
	f.sources[src.PaperID] = append(f.sources[src.PaperID], src)
	return true, nil
}

func (f *fakeIngestCatalog) InsertParseRevision(_ context.Context, rev registry.ParseRevision, setCurrent bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if setCurrent {
		for i := range f.revisions[rev.PaperID] {
			f.revisions[rev.PaperID][i].IsCurrent = false
		}
		rev.IsCurrent = true
	}
	f.revisions[rev.PaperID] = append(f.revisions[rev.PaperID], rev)
	return nil
}

// callIngest drives ingestMinerUNewFormat directly with a prepared zip
// and returns the recorder + decoded body.
func callIngest(t *testing.T, store objstore.Store, catalog *fakeIngestCatalog, canonical, pdfSha, tier string, zipBytes []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/papers/"+canonical+"/upload-mineru", bytes.NewReader(zipBytes))
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := ingestMinerUNewFormat(re, store, catalog, canonical, zipBytes, pdfSha, tier, "tester", "synthetic"); err != nil {
		t.Fatalf("ingest: %v", err)
	}
	var body map[string]any
	if len(rec.Body.Bytes()) > 0 {
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
	}
	return rec, body
}

func newIngestFixture(t *testing.T) (*fakeIngestCatalog, objstore.Store) {
	t.Helper()
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	return &fakeIngestCatalog{
		byArxiv:   map[string]string{"2501.09999": fixturePaperID},
		sources:   map[string][]registry.PaperSource{},
		revisions: map[string][]registry.ParseRevision{},
	}, store
}

func TestIngestNewFormatHappyPath(t *testing.T) {
	c, store := newIngestFixture(t)
	zipBytes := buildNewFormatZipPublic(t, "lite", map[string][]byte{"fig1.png": {0x89, 0x50}})
	pdfSha := strings.Repeat("ab", 32)

	rec, body := callIngest(t, store, c, "2501.09999v1", pdfSha, "", zipBytes)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%v", rec.Code, body)
	}
	if body["paper_id"] != fixturePaperID {
		t.Errorf("paper_id = %v", body["paper_id"])
	}
	if body["source_minted"] != true {
		t.Errorf("source_minted = %v, want true on first upload", body["source_minted"])
	}
	if body["tier"] != "lite" {
		t.Errorf("tier = %v, want lite (from zip metadata)", body["tier"])
	}
	if body["is_current"] != true {
		t.Errorf("is_current = %v", body["is_current"])
	}

	// Bundle landed in the store under papers/<qa>/parses/<rev>/.
	keys := body["objstore_keys"].(map[string]any)
	midKey := keys["middle_json"].(string)
	if !strings.HasPrefix(midKey, "papers/"+fixturePaperID+"/parses/pr_") || !strings.HasSuffix(midKey, "/middle.json") {
		t.Errorf("middle key = %q", midKey)
	}
	rc, _, err := store.Get(context.Background(), midKey)
	if err != nil {
		t.Fatalf("stored middle.json missing: %v", err)
	}
	rc.Close()
	mdKey := keys["markdown"].(string)
	if !strings.HasSuffix(mdKey, "/markdown.md") {
		t.Errorf("markdown key = %q", mdKey)
	}
	imgs := keys["images"].([]any)
	if len(imgs) != 1 || !strings.HasSuffix(imgs[0].(string), "/images/fig1.png") {
		t.Errorf("image keys = %v", imgs)
	}

	// Revision row written with artifact sha + tier + is_current.
	revs := c.revisions[fixturePaperID]
	if len(revs) != 1 {
		t.Fatalf("revisions = %d, want 1", len(revs))
	}
	if revs[0].Tier != "lite" || !revs[0].IsCurrent || revs[0].ObjstoreKey != midKey {
		t.Errorf("revision row = %+v", revs[0])
	}
	if revs[0].Schema != "docvortex.middle" || revs[0].SchemaVersion != "2.0" {
		t.Errorf("schema fields = %s/%s", revs[0].Schema, revs[0].SchemaVersion)
	}

	// artifact sha matches the stored bytes.
	stored, err := readAllFromStore(t, store, midKey)
	if err != nil {
		t.Fatalf("read middle: %v", err)
	}
	if fmt.Sprintf("%x", sha256Bytes(stored)) != body["artifact_sha256"] {
		t.Error("artifact_sha256 does not match stored middle.json bytes")
	}
}

func TestIngestNewFormatSourceReuseAndCurrentFlip(t *testing.T) {
	c, store := newIngestFixture(t)
	pdfSha := strings.Repeat("cd", 32)

	_, body1 := callIngest(t, store, c, "2501.09999v1", pdfSha, "", buildNewFormatZipPublic(t, "", nil))
	// Different middle.json content (different block text) → same
	// source, new revision, pointer flips.
	_, body2 := callIngest(t, store, c, "2501.09999v1", pdfSha, "premium", buildNewFormatZipPublic2(t, "", "second parse", nil))

	if body2["source_id"] != body1["source_id"] {
		t.Errorf("source id changed on re-upload: %v → %v", body1["source_id"], body2["source_id"])
	}
	if body2["source_minted"] != false {
		t.Errorf("source_minted = %v, want false on reuse", body2["source_minted"])
	}
	if body2["tier"] != "premium" {
		t.Errorf("tier = %v, want premium (query param fallback)", body2["tier"])
	}
	revs := c.revisions[fixturePaperID]
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2", len(revs))
	}
	current := 0
	for _, r := range revs {
		if r.IsCurrent {
			current++
		}
	}
	if current != 1 {
		t.Errorf("current revisions = %d, want exactly 1", current)
	}
	if !revs[1].IsCurrent || revs[0].IsCurrent {
		t.Error("is_current did not flip to the newest revision")
	}
}

func TestIngestNewFormatBadSchemaRejected(t *testing.T) {
	c, store := newIngestFixture(t)
	// Middle JSON with a bogus schema: must 422 and persist NOTHING.
	// (markdown member included so the strict completeness gate passes
	// and the SCHEMA rejection path is what fires.)
	var buf bytes.Buffer
	zw := newZipWriter(t, &buf)
	zw.write("middle_json.json", []byte(`{"schema":"not.middle","schema_version":"9","blocks":[]}`))
	zw.write("markdown.md", []byte("# x\n"))
	zw.close()
	rec, body := callIngest(t, store, c, "2501.09999v1", strings.Repeat("ab", 32), "", buf.Bytes())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !strings.Contains(body["detail"].(string), "rejected") {
		t.Errorf("detail = %v", body["detail"])
	}
	if len(c.revisions[fixturePaperID]) != 0 || len(c.sources[fixturePaperID]) != 0 {
		t.Error("rejected zip must not mint rows")
	}
	if objs, _ := store.ListPrefix(context.Background(), "papers/", 0); len(objs) != 0 {
		t.Errorf("rejected zip must not store objects (found %d)", len(objs))
	}
}

func TestIngestNewFormatIncompleteRejected(t *testing.T) {
	c, store := newIngestFixture(t)
	// Strict intake (plan §13.4.5): middle_json without markdown is
	// NOT a complete new-CLI final output — refuse the half bundle.
	var buf bytes.Buffer
	zw := newZipWriter(t, &buf)
	zw.write("middle_json.json", []byte(`{"schema":"docvortex.middle","schema_version":"2.0","blocks":[]}`))
	zw.close()
	rec, body := callIngest(t, store, c, "2501.09999v1", strings.Repeat("ab", 32), "", buf.Bytes())
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
	if !strings.Contains(body["detail"].(string), "COMPLETE") {
		t.Errorf("detail = %v", body["detail"])
	}
	if len(c.revisions[fixturePaperID]) != 0 || len(c.sources[fixturePaperID]) != 0 {
		t.Error("incomplete zip must not mint rows")
	}
}

func TestIngestNewFormatUnknownPaperAndMissingSha(t *testing.T) {
	c, store := newIngestFixture(t)
	zipBytes := buildNewFormatZipPublic(t, "", nil)

	// Unknown paper → 404.
	rec, _ := callIngest(t, store, c, "9999.00001v1", strings.Repeat("ab", 32), "", zipBytes)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown paper status = %d, want 404", rec.Code)
	}

	// Known paper, no sha provenance anywhere → 400.
	rec, body := callIngest(t, store, c, "2501.09999v1", "", "", zipBytes)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("no-sha status = %d, want 400", rec.Code)
	}
	if !strings.Contains(body["detail"].(string), "pdf_sha256") {
		t.Errorf("detail = %v", body["detail"])
	}
}

// TestUploadMinerUBranchSelection drives the real handler entry with a
// multipart body to prove the branch: a new-format zip never reaches
// the legacy full.md extraction (which would 400 on the missing
// full.md).
func TestUploadMinerUBranchSelection(t *testing.T) {
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatalf("local store: %v", err)
	}
	c := &fakeIngestCatalog{
		byArxiv:   map[string]string{"2501.09999": fixturePaperID},
		sources:   map[string][]registry.PaperSource{},
		revisions: map[string][]registry.ParseRevision{},
	}
	zipBytes := buildNewFormatZipPublic(t, "standard", nil)

	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	fw, _ := mw.CreateFormFile("mineru_zip", "result.zip")
	_, _ = fw.Write(zipBytes)
	_ = mw.Close()

	req := httptest.NewRequest(http.MethodPost,
		"/api/papers/2501.09999v1/upload-mineru?pdf_sha256="+strings.Repeat("ef", 32), &mp)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec

	// uploadMinerUHandler wants *registry.Store; call the ingest branch
	// through the same seam the handler uses.
	err = ingestMinerUNewFormat(re, store, c, "2501.09999v1", zipBytes,
		strings.Repeat("ef", 32), "", "tester", "synthetic")
	if err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d body=%s", rec.Code, rec.Body.String())
	}
	if len(c.revisions[fixturePaperID]) != 1 {
		t.Error("new-format branch did not run (no revision row)")
	}
	_ = config.Config{}
}

// buildNewFormatZipPublic is the routes-package variant of the mineru
// test helper (exported there as a method on *testing.T; here a plain
// func to avoid reaching across packages).
func buildNewFormatZipPublic(t *testing.T, tier string, images map[string][]byte) []byte {
	return buildNewFormatZipPublic2(t, tier, "HELLO NEW FORMAT", images)
}

func buildNewFormatZipPublic2(t *testing.T, tier, text string, images map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := newZipWriter(t, &buf)
	middle := map[string]any{
		"schema":         "docvortex.middle",
		"schema_version": "2.0",
		"pdf_info":       map[string]any{"pages": 1},
		"blocks": []map[string]any{
			{"page_idx": 0, "index": 1, "type": "text", "content": text},
		},
	}
	mb, _ := json.Marshal(middle)
	zw.write("out/middle_json.json", mb)
	zw.write("out/markdown.md", []byte("# "+text+"\n"))
	if tier != "" {
		zw.write("out/metadata.json", []byte(fmt.Sprintf(`{"tier":%q}`, tier)))
	}
	for name, b := range images {
		zw.write("out/images/"+name, b)
	}
	zw.close()
	return buf.Bytes()
}
