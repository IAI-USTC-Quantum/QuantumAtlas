package mineru

// realartifact_test.go: re-verification against GENUINE MinerU 4.x
// result zips (plan Q1 验收 "真实MinerU烟测"). The fixtures live outside
// the repo, so the test is opt-in via env:
//
//	QATLAS_REAL_MINERU_ZIP=/path/to/full.zip
//
// Unset → skip with an explicit "real-artifact re-verification pending"
// marker so CI output never mistakes synthetic coverage for the real
// thing. The 2026-09-29 hardening round ran this against
// realpaper/remote/full.zip (MinerU producer 3.4.4, 17 pages, 13 images)
// and realpaper/local's middle.json.

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

func TestRealMinerUArtifact(t *testing.T) {
	zipPath := os.Getenv("QATLAS_REAL_MINERU_ZIP")
	if zipPath == "" {
		t.Skip("QATLAS_REAL_MINERU_ZIP unset; real-artifact re-verification pending (synthetic tests only)")
	}
	zipBytes, err := os.ReadFile(zipPath)
	if err != nil {
		t.Fatalf("read %s: %v", zipPath, err)
	}

	complete, err := ExtractPackage(zipBytes)
	if err != nil {
		t.Fatalf("complete production package extraction: %v", err)
	}
	archive, err := zip.NewReader(bytes.NewReader(zipBytes), int64(len(zipBytes)))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, member := range archive.File {
		if member.FileInfo().IsDir() {
			continue
		}
		reader, err := member.Open()
		if err != nil {
			t.Fatal(err)
		}
		original, err := io.ReadAll(reader)
		reader.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(complete.Members[member.Name], original) {
			t.Fatalf("original member changed or dropped: %s", member.Name)
		}
		count++
	}
	if len(complete.Members) != count {
		t.Fatal("complete member inventory mismatch")
	}

	if !HasMiddleJSON(zipBytes) {
		t.Fatal("genuine MinerU zip not detected by HasMiddleJSON")
	}
	res, err := ExtractNewFormat(zipBytes)
	if err != nil {
		t.Fatalf("ExtractNewFormat: %v", err)
	}
	if len(res.MiddleJSON) == 0 || len(res.Markdown) == 0 {
		t.Fatalf("extraction empty: middle=%d markdown=%d", len(res.MiddleJSON), len(res.Markdown))
	}
	doc, err := ParseMiddleJSON(res.MiddleJSON)
	if err != nil {
		t.Fatalf("ParseMiddleJSON on the real artifact: %v", err)
	}
	if doc.Pages < 1 || len(doc.Blocks) < 1 {
		t.Fatalf("doc empty: pages=%d blocks=%d", doc.Pages, len(doc.Blocks))
	}

	// Page 1 block 1 is the document title in the observed artifacts;
	// the 0-based page-block index must have been normalized to 1.
	title, ok := doc.FindBlock(0, 1)
	if !ok {
		t.Fatal("page_idx=0 index=1 not found — real 0-based index not normalized to the public 1-based numbering")
	}
	if strings.TrimSpace(title.ContentText()) == "" {
		t.Errorf("title block text empty (rich-content spans not concatenated?)")
	}

	// Every page/block the doc reports is exactly findable, and every
	// index is >= 1.
	for _, b := range doc.OrderedBlocks() {
		if b.Index < 1 {
			t.Fatalf("block index %d < 1 after normalization (page %d)", b.Index, b.PageIdx)
		}
		if _, ok := doc.FindBlock(b.PageIdx, b.Index); !ok {
			t.Fatalf("FindBlock(%d,%d) lost a block the doc reports", b.PageIdx, b.Index)
		}
	}

	// Images extracted under images/ keys.
	if len(res.Images) == 0 {
		t.Log("note: this zip carries no images/")
	}
	t.Logf("real artifact OK: pages=%d blocks=%d images=%d markdown=%dB",
		doc.Pages, len(doc.Blocks), len(res.Images), len(res.Markdown))
}

// TestRealMinerUMiddleFileOnly re-verifies a bare middle.json (the
// local-parse artifact layout: no zip, just the JSON next to
// markdown.md) via QATLAS_REAL_MINERU_MIDDLE=/path/to/middle.json.
func TestRealMinerUMiddleFileOnly(t *testing.T) {
	p := os.Getenv("QATLAS_REAL_MINERU_MIDDLE")
	if p == "" {
		t.Skip("QATLAS_REAL_MINERU_MIDDLE unset; skipping")
	}
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	doc, err := ParseMiddleJSON(data)
	if err != nil {
		t.Fatalf("ParseMiddleJSON: %v", err)
	}
	if len(doc.Blocks) == 0 {
		t.Fatal("no blocks parsed from the real local middle.json")
	}
	t.Logf("real local middle OK: pages=%d blocks=%d", doc.Pages, len(doc.Blocks))
}
