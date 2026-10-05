package routes

// This optional test ingests the genuine archived MinerU output with its
// captured source PDF. It verifies stored source SHA and every original ZIP
// member, without contacting MinerU or running inference.
import (
	"bytes"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
)

func TestRealMinerUIngestEndToEnd(t *testing.T) {
	zipPath := os.Getenv("QATLAS_REAL_MINERU_ZIP")
	if zipPath == "" {
		t.Skip("QATLAS_REAL_MINERU_ZIP unset; genuine ingest verification pending")
	}
	archive, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatal(err)
	}
	pdfPath := os.Getenv("QATLAS_REAL_MINERU_PDF")
	if pdfPath == "" {
		pdfPath = filepath.Join(filepath.Dir(filepath.Dir(zipPath)), "1605.01488.pdf")
	}
	pdf, err := os.ReadFile(pdfPath)
	if err != nil {
		t.Fatalf("captured source PDF required (set QATLAS_REAL_MINERU_PDF): %v", err)
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		t.Fatal("fixture source is not PDF bytes")
	}
	c, store := newIngestFixture(t)
	c.byArxiv["1605.01488"] = fixturePaperID
	canonical := "1605.01488v1"
	if _, err := store.Put(t.Context(), paperassets.AssetKey("pdf", canonical), bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	sha := paperbundle.SHA256(pdf)
	rec, body := callIngest(t, store, c, canonical, sha, "", archive)
	if rec.Code != http.StatusCreated {
		t.Fatalf("real intake %d %s", rec.Code, rec.Body.String())
	}
	revision := body["revision_id"].(string)
	bundle := c.bundles[revision]
	manifest, err := paperbundle.New(store).VerifyBundle(t.Context(), fixturePaperID, bundle.SourceID, revision)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.SourcePDFSHA256 != sha || bundle.SourcePDFSHA256 != sha || body["source_sha256"] != sha {
		t.Fatal("claimed/frozen/manifest source SHA mismatch")
	}
	frozen, err := paperbundle.New(store).ReadPDF(t.Context(), fixturePaperID, bundle.SourceID, sha, int64(len(pdf)))
	if err != nil || !bytes.Equal(frozen, pdf) {
		t.Fatalf("captured PDF not frozen byte-for-byte: %v", err)
	}
	result, err := mineru.ExtractPackage(archive)
	if err != nil {
		t.Fatal(err)
	}
	if len(manifest.Files) != len(result.Members) {
		t.Fatal("complete member inventory changed")
	}
	for name, original := range result.Members {
		stored, err := readAllFromStore(t, store, paperbundle.FileKey(fixturePaperID, bundle.SourceID, revision, name))
		if err != nil || !bytes.Equal(stored, original) {
			t.Fatalf("original %s lost/renamed/rewritten: %v", name, err)
		}
	}
	if bundle.MiddlePath != result.MiddlePath || bundle.MarkdownPath != result.MarkdownPath || bundle.ArtifactSha256 != paperbundle.SHA256(result.MiddleJSON) {
		t.Fatal("producer path/artifact SHA changed")
	}
	rawManifest, err := readAllFromStore(t, store, bundle.ManifestKey)
	if err != nil || paperbundle.SHA256(rawManifest) != bundle.ManifestSHA256 {
		t.Fatal("published manifest not pinned to persisted bytes")
	}
	var decoded paperbundle.Manifest
	if json.Unmarshal(rawManifest, &decoded) != nil || decoded.SourcePDFSHA256 != sha {
		t.Fatal("manifest not exact source-pinned")
	}
	doc, err := mineru.ParseMiddleJSON(result.MiddleJSON)
	if err != nil {
		t.Fatal(err)
	}
	first := doc.OrderedBlocks()[0]
	locator := mineru.Locator(sha, bundle.Tier, first.PageIdx, first.Index)
	if locator != "doc:"+sha[:7]+"/tier:standard/page:1/block:1" {
		t.Fatal("real block/source locator changed: ", locator)
	}
	t.Logf("genuine intake source=%s revision=%s members=%d pages=%d blocks=%d", sha, revision, len(manifest.Files), doc.Pages, len(doc.Blocks))
}
