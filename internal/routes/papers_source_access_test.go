package routes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// Pure in-memory catalog + local bytes: no live PostgreSQL or inference API.
type fakeAccessCatalog struct {
	contentCatalog
	paper   registry.Paper
	assets  []registry.Asset
	sources map[string]registry.PaperSource
	imports map[string]string
	err     error
	freezes int
}

func newAccessFixture(t *testing.T) (*fakeAccessCatalog, objstore.Store) {
	t.Helper()
	s, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &fakeAccessCatalog{paper: registry.Paper{PaperID: "qa_access", ArxivID: "1605.01488", DOI: "10.1234/access"}, sources: map[string]registry.PaperSource{}, imports: map[string]string{}}, s
}
func (c *fakeAccessCatalog) Get(_ context.Context, id string) (*registry.Paper, bool, error) {
	if c.err != nil {
		return nil, false, c.err
	}
	if id != c.paper.PaperID {
		return nil, false, nil
	}
	p := c.paper
	return &p, true, nil
}
func (c *fakeAccessCatalog) GetPaperIDByIdentity(_ context.Context, scheme, id string) (string, bool, error) {
	if c.err != nil {
		return "", false, c.err
	}
	found := scheme == "arxiv" && registry.NormalizeArxivID(id) == registry.NormalizeArxivID(c.paper.ArxivID) || scheme == "doi" && registry.NormalizeDOI(id) == c.paper.DOI
	return c.paper.PaperID, found, nil
}
func (c *fakeAccessCatalog) GetWithAssets(_ context.Context, id string) (*registry.PaperDetail, bool, error) {
	if c.err != nil {
		return nil, false, c.err
	}
	if id != c.paper.PaperID {
		return nil, false, nil
	}
	p := c.paper
	return &registry.PaperDetail{Paper: &p, Assets: c.assets}, true, nil
}
func (c *fakeAccessCatalog) GetPaperSource(_ context.Context, paper, id string) (registry.PaperSource, bool, error) {
	if c.err != nil {
		return registry.PaperSource{}, false, c.err
	}
	src, ok := c.sources[id]
	return src, ok && src.PaperID == paper, nil
}
func (c *fakeAccessCatalog) ListPaperSources(_ context.Context, paper string) ([]registry.PaperSource, error) {
	if c.err != nil {
		return nil, c.err
	}
	var out []registry.PaperSource
	for _, src := range c.sources {
		if src.PaperID == paper {
			out = append(out, src)
		}
	}
	return out, nil
}
func (c *fakeAccessCatalog) GetImportedPaperSource(ctx context.Context, paper, key string) (registry.PaperSource, bool, error) {
	id, ok := c.imports[key]
	if !ok {
		return registry.PaperSource{}, false, c.err
	}
	return c.GetPaperSource(ctx, paper, id)
}
func (c *fakeAccessCatalog) FreezePaperSource(ctx context.Context, s objstore.Store, src registry.PaperSource) (registry.PaperSource, error) {
	c.freezes++
	if src.ObjstoreKey == paperbundle.PDFKey(src.PaperID, src.SourceID) {
		_, err := paperbundle.New(s).ReadPDF(ctx, src.PaperID, src.SourceID, src.Sha256, src.SizeBytes)
		return src, err
	}
	r, _, err := s.Get(ctx, src.ObjstoreKey)
	if err != nil {
		return src, err
	}
	body, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		return src, err
	}
	if int64(len(body)) != src.SizeBytes || paperbundle.SHA256(body) != src.Sha256 {
		return src, paperbundle.ErrIntegrity
	}
	frozen, err := paperbundle.New(s).FreezePDF(ctx, src.PaperID, src.SourceID, body, src.Sha256)
	if err != nil {
		return src, err
	}
	c.imports[src.ObjstoreKey] = src.SourceID
	src.ObjstoreKey = frozen.Key
	c.sources[src.SourceID] = src
	return src, nil
}
func (c *fakeAccessCatalog) RegisterFrozenPaperSource(ctx context.Context, s objstore.Store, paper, origin, key string) (registry.PaperSource, error) {
	if src, ok, err := c.GetImportedPaperSource(ctx, paper, key); err != nil || ok {
		if err != nil {
			return src, err
		}
		return c.FreezePaperSource(ctx, s, src)
	}
	for _, src := range c.sources {
		if src.PaperID == paper && src.ObjstoreKey == key {
			return c.FreezePaperSource(ctx, s, src)
		}
	}
	r, _, err := s.Get(ctx, key)
	if err != nil {
		return registry.PaperSource{}, err
	}
	body, err := io.ReadAll(r)
	_ = r.Close()
	if err != nil {
		return registry.PaperSource{}, err
	}
	sha := paperbundle.SHA256(body)
	for _, src := range c.sources {
		if src.PaperID == paper && src.Sha256 == sha {
			src, err = c.FreezePaperSource(ctx, s, src)
			if err == nil {
				c.imports[key] = src.SourceID
			}
			return src, err
		}
	}
	src := registry.PaperSource{SourceID: registry.NewSourceID(), PaperID: paper, Origin: origin, Sha256: sha, ObjstoreKey: key, SizeBytes: int64(len(body)), CreatedAt: time.Now()}
	c.sources[src.SourceID] = src
	return c.FreezePaperSource(ctx, s, src)
}
func (c *fakeAccessCatalog) BindPaperSourceImport(ctx context.Context, s objstore.Store, paper, id, key string) (registry.PaperSource, error) {
	src, found, err := c.GetPaperSource(ctx, paper, id)
	if err != nil || !found {
		return src, err
	}
	if prior, found, _ := c.GetImportedPaperSource(ctx, paper, key); found {
		if prior.Sha256 != src.Sha256 {
			return src, paperbundle.ErrIntegrity
		}
		return c.FreezePaperSource(ctx, s, prior)
	}
	src, err = c.FreezePaperSource(ctx, s, src)
	if err == nil {
		c.imports[key] = src.SourceID
	}
	return src, err
}
func accessSource(t *testing.T, c *fakeAccessCatalog, s objstore.Store, id, origin, key string, pdf []byte) registry.PaperSource {
	t.Helper()
	if _, err := s.Put(context.Background(), key, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	src := registry.PaperSource{SourceID: id, PaperID: c.paper.PaperID, Origin: origin, ObjstoreKey: key, Sha256: paperbundle.SHA256(pdf), SizeBytes: int64(len(pdf)), CreatedAt: time.Now()}
	c.sources[id] = src
	return src
}

func TestContentSelectionPinsNeverSubstitute(t *testing.T) {
	c, s := newAccessFixture(t)
	pdf := []byte("%PDF-v2\n")
	accessSource(t, c, s, "src_v2", "arxiv:1605.01488v2", "pdf/v2.pdf", pdf)
	accessSource(t, c, s, "src_v3", "arxiv:v3", "pdf/v3.pdf", []byte("%PDF-v3\n"))
	accessSource(t, c, s, "src_pub", "published", "pdf/pub.pdf", []byte("%PDF-published\n"))
	_, src, err := sourceForAccess(context.Background(), s, c, "1605.01488v2", "", "v2")
	if err != nil || src.SourceID != "src_v2" || src.Origin != "arxiv:v2" {
		t.Fatalf("v2 pin=%+v %v", src, err)
	}
	for _, tc := range []struct{ request, id, version string }{{"1605.01488v2", "src_v3", ""}, {"qa_access", "src_pub", "v2"}, {"qa_access", "src_missing", "v2"}, {"1605.01488v2", "", "v3"}, {"qa_access", "", "v99"}} {
		_, _, err := sourceForAccess(context.Background(), s, c, tc.request, tc.id, tc.version)
		if err == nil {
			t.Fatalf("pin substituted %+v", tc)
		}
		var ae *contentAccessError
		if errors.As(err, &ae) && tc.id != "" && ae.AllowFetch {
			t.Fatalf("explicitsource allowed fetch %+v", tc)
		}
	}
}

func TestContentUploadOriginAliasProvesVersionWithoutHistoryRewrite(t *testing.T) {
	c, s := newAccessFixture(t)
	pdf := []byte("%PDF-original upload\n")
	original := accessSource(t, c, s, "src_upload", "upload", "pdf/legacy.pdf", pdf)
	frozen, err := c.FreezePaperSource(context.Background(), s, original)
	if err != nil {
		t.Fatal(err)
	}
	key := paperassets.AssetKey("pdf", "1605.01488v2")
	if _, err := c.BindPaperSourceImport(context.Background(), s, c.paper.PaperID, frozen.SourceID, key); err != nil {
		t.Fatal(err)
	}
	if err := s.Delete(context.Background(), original.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ request, id, version string }{{"1605.01488v2", "", ""}, {"qa_access", "src_upload", "v2"}, {"1605.01488v2", "src_upload", "v2"}} {
		_, got, err := sourceForAccess(context.Background(), s, c, tc.request, tc.id, tc.version)
		if err != nil || got.SourceID != original.SourceID || got.Sha256 != original.Sha256 || got.Origin != "arxiv:v2" {
			t.Fatalf("alias proof %+v %+v %v", tc, got, err)
		}
	}
	if c.sources[original.SourceID].Origin != "upload" {
		t.Fatal("stored origin/history rewritten")
	}
	if err := s.Delete(context.Background(), frozen.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	accessSource(t, c, s, "src_other", "arxiv:v3", "pdf/other.pdf", []byte("%PDF-other"))
	_, _, err = sourceForAccess(context.Background(), s, c, "1605.01488v2", "", "v2")
	if !errors.Is(err, objstore.ErrNotFound) {
		t.Fatalf("missing frozen fell back %v", err)
	}
	var ae *contentAccessError
	if errors.As(err, &ae) && ae.AllowFetch {
		t.Fatal("missing frozen permits freshfetch")
	}
}

func TestContentLegacyPDFOnlyAndCanonicalAliasPin(t *testing.T) {
	c, s := newAccessFixture(t)
	pdf := []byte("%PDF-legacy original\n")
	key := paperassets.AssetKey("pdf", "1605.01488v2")
	if _, err := s.Put(context.Background(), key, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	c.assets = []registry.Asset{{Source: "arxiv", ArxivVersion: 2, PDFPath: key, PDFSha256: paperbundle.SHA256(pdf), MinerUMDPath: "markdown/old.md", MinerUJSONPath: "json/old.json", ImageCount: 5}}
	rp, src, err := sourceForAccess(context.Background(), s, c, "1605.01488v2", "", "")
	if err != nil || rp.canonical != c.paper.PaperID || src.ObjstoreKey != paperbundle.PDFKey(c.paper.PaperID, src.SourceID) {
		t.Fatalf("selection=%+v %+v %v", rp, src, err)
	}
	items, err := s.ListPrefix(context.Background(), "content/", 0)
	if err != nil || len(items) != 1 {
		t.Fatalf("legacy outputs migrated %v %v", items, err)
	}
	if err := s.Delete(context.Background(), key); err != nil {
		t.Fatal(err)
	}
	_, again, err := sourceForAccess(context.Background(), s, c, "1605.01488v2", "", "")
	if err != nil || again.SourceID != src.SourceID {
		t.Fatalf("alias accessed missingold %v %+v", err, again)
	}
}

func TestSourceOriginNormalization(t *testing.T) {
	for _, origin := range []string{"arxiv:v2", "arxiv:1605.01488v2", "arxiv:quant-ph/9508027v2"} {
		if normalizedSourceOrigin(origin) != "arxiv:v2" || !sourceVersionMatches(origin, "v2") || sourceVersionMatches(origin, "v3") {
			t.Fatal(origin)
		}
	}
	if sourceVersionMatches("upload", "v2") || sourceVersionMatches("published", "v2") {
		t.Fatal("nonarxiv asserted version")
	}
}
