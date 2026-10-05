package routes

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/pocketbase/pocketbase/core"
)

func TestOriginalImagesZIPRetainsDistinctPathsAndDigest(t *testing.T) {
	members := map[string][]byte{"images/a..png": []byte("dots"), "images/foo.png": []byte("nested"), "foo.png": []byte("root"), "producer/images/sub/a.jpg": []byte("deep")}
	first, err := buildOriginalMembersZIP(members)
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildOriginalMembersZIP(members)
	if err != nil || !bytes.Equal(first, second) {
		t.Fatal("derived archive is nondeterministic", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(first), int64(len(first)))
	if err != nil {
		t.Fatal(err)
	}
	if len(archive.File) != len(members) {
		t.Fatal("original member dropped or collided")
	}
	for _, file := range archive.File {
		r, err := file.Open()
		if err != nil {
			t.Fatal(err)
		}
		data, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil || !bytes.Equal(data, members[file.Name]) {
			t.Fatalf("member %q changed: %v", file.Name, err)
		}
	}
	c, store, _ := newReadingFixture(t)
	rec, _ := readRouteRequest(t, "/api/papers/"+readTestPaper+"/images/zip?source_id="+readTestSource, func(re *core.RequestEvent) error {
		return contentDerivativeHandler(re, &config.Config{PaperAccessEnabled: true}, store, c, nil, readTestPaper, "images/zip")
	})
	digest := paperbundle.SHA256(rec.Body.Bytes())
	if rec.Code != 200 || rec.Header().Get("X-QAtlas-Sha256") != digest || rec.Header().Get("X-QAtlas-Artifact-SHA256") != digest {
		t.Fatalf("archive digest/header wrong: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("X-QAtlas-PDF-SHA256") != c.source.Sha256 {
		t.Fatal("PDF source proof lost")
	}
}

func TestContentReadResolvesImagesRelativeToOriginalMiddleDirectory(t *testing.T) {
	c, store, old := newReadingFixture(t)
	original, err := paperbundle.New(store).GetManifest(t.Context(), old.PaperID, old.SourceID, old.RevisionID)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{}
	for _, member := range original.Files {
		data, err := verifiedBundleMember(t.Context(), store, old, original, member.Path)
		if err != nil {
			t.Fatal(err)
		}
		files["producer/"+member.Path] = data
	}
	revision := "pr_readnested"
	m, err := paperbundle.New(store).WriteBundle(t.Context(), paperbundle.Input{PaperID: old.PaperID, SourceID: old.SourceID, RevisionID: revision, SourcePDFSHA256: c.source.Sha256, Files: files, MiddlePath: "producer/" + original.MiddlePath, MarkdownPath: "producer/" + original.MarkdownPath})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(m)
	b := old
	b.RevisionID = revision
	b.MiddlePath = m.MiddlePath
	b.MarkdownPath = m.MarkdownPath
	b.ManifestKey = paperbundle.ManifestKey(b.PaperID, b.SourceID, revision)
	b.ManifestSHA256 = paperbundle.SHA256(encoded)
	b.ObjstoreKey = paperbundle.FileKey(b.PaperID, b.SourceID, revision, b.MiddlePath)
	c.bundles[revision] = b
	c.current = revision
	rec, body := callReading(t, c, store, nil, "source_id="+readTestSource+"&page=1&block=5")
	want := bundleMemberURL(b.PaperID, revision, "producer/images/nested/figure (1).jpg")
	if rec.Code != 200 || !strings.Contains(body["content"].(string), want) {
		t.Fatalf("nested original image URL lost: %d %+v", rec.Code, body)
	}
}

type statusEditionCatalog struct {
	*readTestCatalog
	assets []registry.Asset
}

func (c *statusEditionCatalog) GetWithAssets(ctx context.Context, id string) (*registry.PaperDetail, bool, error) {
	p, ok, err := c.Get(ctx, id)
	return &registry.PaperDetail{Paper: p, Assets: c.assets}, ok, err
}
func TestFrozenStatusNewerAssetOutranksReadyOldSourceWithoutMigration(t *testing.T) {
	base, store, _ := newReadingFixture(t)
	pdf := []byte("%PDF-v3 untouched legacy")
	key := "pdf/newer-v3.pdf"
	if _, err := store.Put(t.Context(), key, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	c := &statusEditionCatalog{readTestCatalog: base, assets: []registry.Asset{{Source: "arxiv", ArxivVersion: 3, PDFPath: key, PDFSize: int64(len(pdf)), MinerUMDPath: "obsolete.md"}}}
	for _, id := range []string{readTestPaper, "2501.09999"} {
		e := frozenPaperStatus(t.Context(), c, store, nil, id)
		if !e.PdfReady || e.MdReady || e.State == "cached" || c.readyCalls != 0 {
			t.Fatalf("status selected wrong edition or read-ready marker: %+v", e)
		}
	}
	objects, err := store.ListPrefix(t.Context(), "content/", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, obj := range objects {
		if strings.Contains(obj.Key, "newer-v3") {
			t.Fatal("metadata probe migrated source")
		}
	}
}

func TestAdminDerivedBatchDigestIsArchiveNotLastSource(t *testing.T) {
	c, store, _ := newReadingFixture(t)
	rec, _ := readRouteRequest(t, "/api/admin/assets/batch/download?paper_ids="+readTestPaper+"&kind=markdown", func(re *core.RequestEvent) error {
		return adminFrozenBatch(re, &config.Config{PaperAccessEnabled: true}, store, c, nil)
	})
	if rec.Code != http.StatusOK || rec.Header().Get("X-QAtlas-Sha256") != paperbundle.SHA256(rec.Body.Bytes()) {
		t.Fatal("admin archive hash mismatch")
	}
	if rec.Header().Get("X-QAtlas-Source-Id") != "" || rec.Header().Get("X-QAtlas-PDF-SHA256") != "" {
		t.Fatal("batch falsely identified as last PDF source")
	}
}
