package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

var ingestPDF = []byte("%PDF-1.4\nexact frozen intake source fixture\n%%EOF\n")

// Offline catalog models authoritative immutable sources and publications,
// while actual LocalStore objects/manifest hashes provide the readiness proof.
type fakeIngestCatalog struct {
	mu             sync.Mutex
	byArxiv, byDOI map[string]string
	sources        map[string][]registry.PaperSource
	revisions      map[string][]registry.ParseRevision
	imports        map[string]string
	bundles        map[string]registry.ParseBundle
	publishErr     error
}

func (f *fakeIngestCatalog) GetPaperIDByIdentity(_ context.Context, scheme, id string) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var p string
	if scheme == "arxiv" {
		p = f.byArxiv[registry.NormalizeArxivID(id)]
	} else if scheme == "doi" {
		p = f.byDOI[registry.NormalizeDOI(id)]
	}
	return p, p != "", nil
}
func (f *fakeIngestCatalog) GetPaperSource(_ context.Context, paper, id string) (registry.PaperSource, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sources[paper] {
		if s.SourceID == id {
			return s, true, nil
		}
	}
	return registry.PaperSource{}, false, nil
}
func (f *fakeIngestCatalog) ListPaperSources(_ context.Context, paper string) ([]registry.PaperSource, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]registry.PaperSource(nil), f.sources[paper]...), nil
}
func (f *fakeIngestCatalog) GetImportedPaperSource(ctx context.Context, paper, key string) (registry.PaperSource, bool, error) {
	f.mu.Lock()
	id := f.imports[paper+":"+key]
	f.mu.Unlock()
	if id == "" {
		return registry.PaperSource{}, false, nil
	}
	return f.GetPaperSource(ctx, paper, id)
}
func (f *fakeIngestCatalog) FreezePaperSource(ctx context.Context, store objstore.Store, src registry.PaperSource) (registry.PaperSource, error) {
	actual, found, err := f.GetPaperSource(ctx, src.PaperID, src.SourceID)
	if err != nil {
		return src, err
	}
	if !found {
		return src, objstore.ErrNotFound
	}
	src = actual
	if strings.HasPrefix(src.ObjstoreKey, "content/") {
		if src.ObjstoreKey != paperbundle.PDFKey(src.PaperID, src.SourceID) {
			return src, paperbundle.ErrIntegrity
		}
		_, err := paperbundle.New(store).ReadPDF(ctx, src.PaperID, src.SourceID, src.Sha256, src.SizeBytes)
		return src, err
	}
	old := src.ObjstoreKey
	r, _, err := store.Get(ctx, old)
	if err != nil {
		return src, err
	}
	b, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return src, err
	}
	if int64(len(b)) != src.SizeBytes || paperbundle.SHA256(b) != src.Sha256 {
		return src, paperbundle.ErrIntegrity
	}
	pdf, err := paperbundle.New(store).FreezePDF(ctx, src.PaperID, src.SourceID, b, src.Sha256)
	if err != nil {
		return src, err
	}
	src.ObjstoreKey = pdf.Key
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, s := range f.sources[src.PaperID] {
		if s.SourceID == src.SourceID {
			f.sources[src.PaperID][i] = src
		}
	}
	if f.imports == nil {
		f.imports = map[string]string{}
	}
	f.imports[src.PaperID+":"+old] = src.SourceID
	return src, nil
}
func (f *fakeIngestCatalog) RegisterFrozenPaperSource(ctx context.Context, store objstore.Store, paper, origin, key string) (registry.PaperSource, error) {
	if prior, ok, err := f.GetImportedPaperSource(ctx, paper, key); err != nil || ok {
		if err != nil {
			return prior, err
		}
		return f.FreezePaperSource(ctx, store, prior)
	}
	r, _, err := store.Get(ctx, key)
	if err != nil {
		return registry.PaperSource{}, err
	}
	b, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return registry.PaperSource{}, err
	}
	sha := paperbundle.SHA256(b)
	f.mu.Lock()
	var src registry.PaperSource
	for _, s := range f.sources[paper] {
		if s.Sha256 == sha {
			src = s
			break
		}
	}
	if src.SourceID == "" {
		src = registry.PaperSource{PaperID: paper, SourceID: registry.NewSourceID(), Origin: origin, ObjstoreKey: key, Sha256: sha, SizeBytes: int64(len(b)), CreatedAt: time.Now()}
		f.sources[paper] = append(f.sources[paper], src)
	}
	f.mu.Unlock()
	src, err = f.FreezePaperSource(ctx, store, src)
	if err != nil {
		return src, err
	}
	return f.BindPaperSourceImport(ctx, store, paper, src.SourceID, key)
}
func (f *fakeIngestCatalog) BindPaperSourceImport(ctx context.Context, store objstore.Store, paper, id, key string) (registry.PaperSource, error) {
	src, found, err := f.GetPaperSource(ctx, paper, id)
	if err != nil {
		return src, err
	}
	if !found {
		return src, objstore.ErrNotFound
	}
	if prior, ok, err := f.GetImportedPaperSource(ctx, paper, key); err != nil || ok {
		if err != nil {
			return prior, err
		}
		if prior.Sha256 != src.Sha256 {
			return src, paperbundle.ErrIntegrity
		}
		return f.FreezePaperSource(ctx, store, prior)
	}
	src, err = f.FreezePaperSource(ctx, store, src)
	if err != nil {
		return src, err
	}
	f.mu.Lock()
	if f.imports == nil {
		f.imports = map[string]string{}
	}
	f.imports[paper+":"+key] = src.SourceID
	f.mu.Unlock()
	return src, nil
}
func (f *fakeIngestCatalog) PublishBundle(ctx context.Context, store objstore.Store, rev registry.ParseRevision, current bool) (registry.ParseBundle, error) {
	m, err := paperbundle.New(store).VerifyBundle(ctx, rev.PaperID, rev.SourceID, rev.RevisionID)
	if err != nil {
		return registry.ParseBundle{}, err
	}
	src, found, err := f.GetPaperSource(ctx, rev.PaperID, rev.SourceID)
	if err != nil {
		return registry.ParseBundle{}, err
	}
	if !found || src.Sha256 != m.SourcePDFSHA256 {
		return registry.ParseBundle{}, paperbundle.ErrIntegrity
	}
	middle, _ := m.Member(m.MiddlePath)
	rev.ArtifactSha256 = middle.SHA256
	rev.ObjstoreKey = paperbundle.FileKey(rev.PaperID, rev.SourceID, rev.RevisionID, m.MiddlePath)
	rev.IsCurrent = current
	r, _, err := store.Get(ctx, paperbundle.ManifestKey(rev.PaperID, rev.SourceID, rev.RevisionID))
	if err != nil {
		return registry.ParseBundle{}, err
	}
	raw, err := io.ReadAll(r)
	r.Close()
	if err != nil {
		return registry.ParseBundle{}, err
	}
	bundle := registry.ParseBundle{ParseRevision: rev, ManifestKey: paperbundle.ManifestKey(rev.PaperID, rev.SourceID, rev.RevisionID), SourcePDFSHA256: src.Sha256, MiddlePath: m.MiddlePath, MarkdownPath: m.MarkdownPath, ManifestSHA256: paperbundle.SHA256(raw)}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.publishErr != nil {
		return registry.ParseBundle{}, f.publishErr
	}
	if current {
		for i := range f.revisions[rev.PaperID] {
			f.revisions[rev.PaperID][i].IsCurrent = false
		}
	}
	f.revisions[rev.PaperID] = append(f.revisions[rev.PaperID], rev)
	if f.bundles == nil {
		f.bundles = map[string]registry.ParseBundle{}
	}
	f.bundles[rev.RevisionID] = bundle
	return bundle, nil
}

var _ mineruIngestCatalog = (*fakeIngestCatalog)(nil)

func callIngest(t *testing.T, store objstore.Store, catalog *fakeIngestCatalog, canonical, pdfSha, tier string, zipBytes []byte) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/papers/"+canonical+"/upload-mineru", bytes.NewReader(zipBytes))
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := ingestMinerUNewFormat(re, store, catalog, canonical, zipBytes, pdfSha, tier, "tester", "synthetic"); err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	return rec, body
}
func newIngestFixture(t *testing.T) (*fakeIngestCatalog, objstore.Store) {
	t.Helper()
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := &fakeIngestCatalog{byArxiv: map[string]string{"2501.09999": fixturePaperID}, byDOI: map[string]string{"10.1234/intake": fixturePaperID}, sources: map[string][]registry.PaperSource{}, revisions: map[string][]registry.ParseRevision{}, imports: map[string]string{}, bundles: map[string]registry.ParseBundle{}}
	if _, err := store.Put(t.Context(), paperassets.AssetKey("pdf", "2501.09999v1"), bytes.NewReader(ingestPDF), int64(len(ingestPDF)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	return c, store
}
func assertIngestPackage(t *testing.T, c *fakeIngestCatalog, store objstore.Store, body map[string]any, archive []byte) registry.ParseBundle {
	t.Helper()
	b := c.bundles[body["revision_id"].(string)]
	m, err := paperbundle.New(store).VerifyBundle(t.Context(), b.PaperID, b.SourceID, b.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	original, err := mineru.ExtractPackage(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Files) != len(original.Members) {
		t.Fatal("original member inventory changed")
	}
	for name, raw := range original.Members {
		stored, err := readAllFromStore(t, store, paperbundle.FileKey(b.PaperID, b.SourceID, b.RevisionID, name))
		if err != nil || !bytes.Equal(stored, raw) {
			t.Fatalf("member %s lost/rewritten: %v", name, err)
		}
	}
	if !strings.HasSuffix(b.ObjstoreKey, "/files/"+original.MiddlePath) {
		t.Fatal("Middle original name changed")
	}
	if b.SourcePDFSHA256 != paperbundle.SHA256(ingestPDF) {
		t.Fatal("source SHA not derived from stored bytes")
	}
	// List the whole actual inventory: LocalStore's partial basename lookup
	// can return unrelated nested markdown.md when a root prefix is absent.
	objects, err := store.ListPrefix(t.Context(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, object := range objects {
		for _, prefix := range []string{"papers/", "markdown/", "images/", "parses/"} {
			if strings.HasPrefix(object.Key, prefix) {
				t.Fatalf("legacy output written: %s", object.Key)
			}
		}
	}
	return b
}
func TestIngestNewFormatHappyPath(t *testing.T) {
	c, store := newIngestFixture(t)
	z := buildNewFormatZipPublic(t, "lite", map[string][]byte{"fig1.png": {0x89, 0x50}})
	rec, body := callIngest(t, store, c, "2501.09999v1", paperbundle.SHA256(ingestPDF), "", z)
	if rec.Code != 201 || body["paper_id"] != fixturePaperID || body["tier"] != "lite" || body["is_current"] != true {
		t.Fatalf("intake %d %+v", rec.Code, body)
	}
	b := assertIngestPackage(t, c, store, body, z)
	if b.Schema != "docvortex.middle" || b.SchemaVersion != "2.0" || b.ArtifactSha256 != body["artifact_sha256"] {
		t.Fatal("published metadata mismatch")
	}
}
func TestIngestNewFormatSourceReuseAndCurrentFlip(t *testing.T) {
	c, store := newIngestFixture(t)
	z1 := buildNewFormatZipPublic(t, "", nil)
	r1, b1 := callIngest(t, store, c, "2501.09999v1", "", "", z1)
	if r1.Code != 201 {
		t.Fatal(r1.Body.String())
	}
	old := assertIngestPackage(t, c, store, b1, z1)
	if err := store.Delete(t.Context(), paperassets.AssetKey("pdf", "2501.09999v1")); err != nil {
		t.Fatal(err)
	}
	z2 := buildNewFormatZipPublic2(t, "", "second parse", nil)
	r2, b2 := callIngest(t, store, c, "2501.09999v1", paperbundle.SHA256(ingestPDF), "premium", z2)
	if r2.Code != 201 || b1["source_id"] != b2["source_id"] || b1["revision_id"] == b2["revision_id"] || b2["tier"] != "premium" {
		t.Fatalf("new immutable revision %d %+v", r2.Code, b2)
	}
	assertIngestPackage(t, c, store, b2, z2)
	if len(c.sources[fixturePaperID]) != 1 || len(c.revisions[fixturePaperID]) != 2 || c.revisions[fixturePaperID][0].IsCurrent || !c.revisions[fixturePaperID][1].IsCurrent {
		t.Fatal("source reuse/current flip failed")
	}
	oldBytes, err := readAllFromStore(t, store, old.ObjstoreKey)
	if err != nil || !bytes.Equal(oldBytes, mustPackage(t, z1).MiddleJSON) {
		t.Fatal("old revision overwritten")
	}
}
func mustPackage(t *testing.T, z []byte) mineru.Result {
	t.Helper()
	r, err := mineru.ExtractPackage(z)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestIngestPreA1AliasPreservesHistoricalSourceAndExplicitPin(t *testing.T) {
	c, store := newIngestFixture(t)
	canonical := "quant-ph/9508027v2"
	c.byArxiv["quant-ph/9508027"] = fixturePaperID
	legacy := "pdf/9508/9508027v2.pdf"
	original := "pdf/upload-original.pdf"
	for _, key := range []string{legacy, original} {
		if _, err := store.Put(t.Context(), key, bytes.NewReader(ingestPDF), int64(len(ingestPDF)), "application/pdf"); err != nil {
			t.Fatal(err)
		}
	}
	c.sources[fixturePaperID] = []registry.PaperSource{{PaperID: fixturePaperID, SourceID: "src_old_upload", Origin: "upload", ObjstoreKey: original, Sha256: paperbundle.SHA256(ingestPDF), SizeBytes: int64(len(ingestPDF))}}
	z := buildNewFormatZipPublic(t, "", nil)
	rec, body := callIngest(t, store, c, canonical, "", "", z)
	if rec.Code != 201 || body["source_id"] != "src_old_upload" {
		t.Fatalf("historical source reminted: %d %+v", rec.Code, body)
	}
	for _, key := range []string{legacy, original} {
		if err := store.Delete(t.Context(), key); err != nil {
			t.Fatal(err)
		}
	}
	rec, _ = callIngest(t, store, c, canonical, "", "", z)
	if rec.Code != 201 || len(c.sources[fixturePaperID]) != 1 || c.sources[fixturePaperID][0].Origin != "upload" {
		t.Fatal("canonical alias fallback or source history rewrite")
	}
	request := httptest.NewRequest(http.MethodPost, "/api/papers/"+canonical+"/upload-mineru?source_id=src_old_upload", nil)
	rec = httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = request
	re.Response = rec
	if err := ingestMinerUNewFormat(re, store, c, canonical, z, paperbundle.SHA256(ingestPDF), "", "tester", "synthetic"); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 201 {
		t.Fatalf("alias-proven exact source/version rejected: %d %s", rec.Code, rec.Body.String())
	}
}

func TestIngestNewFormatBadSchemaRejected(t *testing.T) {
	c, store := newIngestFixture(t)
	var b bytes.Buffer
	z := newZipWriter(t, &b)
	z.write("middle_json.json", []byte(`{"schema":"not.middle","schema_version":"9","blocks":[]}`))
	z.write("markdown.md", []byte("# x\n"))
	z.close()
	rec, _ := callIngest(t, store, c, "2501.09999v1", paperbundle.SHA256(ingestPDF), "", b.Bytes())
	if rec.Code != 422 || len(c.sources[fixturePaperID]) != 0 || len(c.revisions[fixturePaperID]) != 0 {
		t.Fatalf("invalid schema mutated intake: %d", rec.Code)
	}
	objs, _ := store.ListPrefix(t.Context(), "content/", 0)
	if len(objs) != 0 {
		t.Fatal("invalid package persisted")
	}
}
func TestIngestNewFormatIncompleteRejected(t *testing.T) {
	for _, member := range []string{"middle_json.json", "markdown.md"} {
		t.Run(member, func(t *testing.T) {
			c, store := newIngestFixture(t)
			var b bytes.Buffer
			z := newZipWriter(t, &b)
			raw := []byte("# md only")
			if member == "middle_json.json" {
				raw = []byte(`{"schema":"docvortex.middle","schema_version":"2.0","blocks":[]}`)
			}
			z.write(member, raw)
			z.close()
			rec, _ := callIngest(t, store, c, "2501.09999v1", "", "", b.Bytes())
			if rec.Code != 422 || len(c.revisions[fixturePaperID]) != 0 || len(c.sources[fixturePaperID]) != 0 {
				t.Fatal("partial package accepted")
			}
		})
	}
}
func TestIngestNewFormatUnknownPaperAndMissingSha(t *testing.T) {
	c, store := newIngestFixture(t)
	z := buildNewFormatZipPublic(t, "", nil)
	r, _ := callIngest(t, store, c, "9999.00001v1", paperbundle.SHA256(ingestPDF), "", z)
	if r.Code != 404 {
		t.Fatal("unknown paper accepted")
	}
	r, b := callIngest(t, store, c, "2501.09999v1", "", "", z)
	if r.Code != 201 || b["source_sha256"] != paperbundle.SHA256(ingestPDF) {
		t.Fatal("stored exact PDF did not provide SHA provenance")
	}
	missing, empty := newIngestFixture(t)
	if err := empty.Delete(t.Context(), paperassets.AssetKey("pdf", "2501.09999v1")); err != nil {
		t.Fatal(err)
	}
	r, _ = callIngest(t, empty, missing, "2501.09999v1", strings.Repeat("ab", 32), "", z)
	if r.Code != 404 || len(missing.sources[fixturePaperID]) != 0 {
		t.Fatal("claimed SHA fabricated absent PDF")
	}
}
func TestIngestClaimedSHAAndPublicationFailures(t *testing.T) {
	c, store := newIngestFixture(t)
	z := buildNewFormatZipPublic(t, "", nil)
	r, _ := callIngest(t, store, c, "2501.09999v1", strings.Repeat("ab", 32), "", z)
	if r.Code != 400 || len(c.revisions[fixturePaperID]) != 0 {
		t.Fatal("false claimed source SHA published")
	}
	r, b := callIngest(t, store, c, "2501.09999v1", "", "", z)
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	old := b["revision_id"]
	c.publishErr = registry.ErrCatalogUnavailable
	r, _ = callIngest(t, store, c, "2501.09999v1", "", "", buildNewFormatZipPublic2(t, "", "must not become current", nil))
	if r.Code != 503 || len(c.revisions[fixturePaperID]) != 1 || !c.revisions[fixturePaperID][0].IsCurrent || c.revisions[fixturePaperID][0].RevisionID != old {
		t.Fatal("failed publication changed current")
	}
}
func TestIngestDOINewRevisionAndFrozenNoFallback(t *testing.T) {
	c, store := newIngestFixture(t)
	doi := "10.1234/intake"
	key := paperassets.DOIAssetKey("pdf", doi)
	if _, err := store.Put(t.Context(), key, bytes.NewReader(ingestPDF), int64(len(ingestPDF)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	z := buildNewFormatZipPublic(t, "", nil)
	r, b := callIngest(t, store, c, doi, "", "", z)
	if r.Code != 201 {
		t.Fatal(r.Body.String())
	}
	first := c.bundles[b["revision_id"].(string)]
	if err := store.Delete(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	r, b2 := callIngest(t, store, c, doi, "", "", z)
	if r.Code != 201 || b["source_id"] != b2["source_id"] || b["revision_id"] == b2["revision_id"] {
		t.Fatal("DOI revision overwrite/source remint")
	}
	if err := store.Delete(t.Context(), paperbundle.PDFKey(first.PaperID, first.SourceID)); err != nil {
		t.Fatal(err)
	}
	_, _ = store.Put(t.Context(), key, bytes.NewReader(ingestPDF), int64(len(ingestPDF)), "application/pdf")
	r, _ = callIngest(t, store, c, doi, "", "", z)
	if r.Code != 404 || len(c.revisions[fixturePaperID]) != 2 {
		t.Fatal("missing frozen PDF fell back to old bytes")
	}
}
func TestUploadMinerUBranchSelection(t *testing.T) {
	c, store := newIngestFixture(t)
	archive := buildNewFormatZipPublic(t, "standard", nil)
	var mp bytes.Buffer
	mw := multipart.NewWriter(&mp)
	w, _ := mw.CreateFormFile("mineru_zip", "result.zip")
	_, _ = w.Write(archive)
	_ = mw.Close()
	req := httptest.NewRequest(http.MethodPost, "/api/papers/2501.09999v1/upload-mineru?pdf_sha256="+paperbundle.SHA256(ingestPDF), &mp)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := uploadFrozenMinerU(re, &config.Config{}, store, c, "2501.09999v1", false); err != nil {
		t.Fatal(err)
	}
	if rec.Code != 201 || len(c.revisions[fixturePaperID]) != 1 {
		t.Fatalf("multipart complete intake %d %s", rec.Code, rec.Body.String())
	}
}
func buildNewFormatZipPublic(t *testing.T, tier string, images map[string][]byte) []byte {
	return buildNewFormatZipPublic2(t, tier, "HELLO NEW FORMAT", images)
}
func buildNewFormatZipPublic2(t *testing.T, tier, text string, images map[string][]byte) []byte {
	t.Helper()
	var b bytes.Buffer
	z := newZipWriter(t, &b)
	middle := map[string]any{"schema": "docvortex.middle", "schema_version": "2.0", "pdf_info": map[string]any{"pages": 1}, "blocks": []map[string]any{{"page_idx": 0, "index": 1, "type": "text", "content": text}}}
	raw, _ := json.Marshal(middle)
	z.write("out/middle_json.json", raw)
	z.write("out/markdown.md", []byte("# "+text+"\n"))
	z.write("out/structured_content.json", []byte(`{"consumer_view":true}`))
	z.write("out/unknown/model.bin", []byte{0, 0xff, 1})
	if tier != "" {
		z.write("out/metadata.json", []byte(fmt.Sprintf(`{"tier":%q}`, tier)))
	}
	for name, raw := range images {
		z.write("out/images/"+name, raw)
	}
	z.close()
	return b.Bytes()
}
