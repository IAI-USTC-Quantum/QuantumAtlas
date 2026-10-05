package ingest

import (
	"bytes"
	"context"
	"errors"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"testing"
)

func TestFrozenIngestNeverWritesLegacyAndFailsClosedOnBoundMissing(t *testing.T) {
	stub := &arxivStub{pdfBytes: map[string][]byte{"2401.12345v2": testPDF}}
	reg := &fakeReg{}
	ing, store := newTestIngester(t, stub, reg)
	ref := registry.PaperRef{ArxivID: "2401.12345v2"}
	alias := paperassets.AssetKey("pdf", ref.ArxivID)
	src, err := ing.registerFreshSource(context.Background(), ref, "arxiv:"+ref.ArxivID, alias, testPDF, testPDFSha(), int64(len(testPDF)))
	if err != nil {
		t.Fatal(err)
	}
	if items, err := store.ListPrefix(context.Background(), "pdf/", 0); err != nil || len(items) != 0 {
		t.Fatalf("fresh legacy write %v %v", items, err)
	}
	ing.OnMint(context.Background(), "qa_reuse", ref)
	waitFor(t, "cached reuse", func() bool { return ing.Snapshot()["fetched"] == 1 })
	if stub.pdfHits.Load() != 0 {
		t.Fatal("bound alias redownloaded")
	}
	if err := store.Delete(context.Background(), src.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(context.Background(), alias, bytes.NewReader(testPDF), int64(len(testPDF)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	ing.OnMint(context.Background(), "qa_missing", ref)
	waitFor(t, "missing frozen failure", func() bool { return ing.Snapshot()["failed"] == 1 })
	if stub.pdfHits.Load() != 0 {
		t.Fatal("missing frozen restored via network")
	}
	if _, exists, _ := store.Stat(context.Background(), src.ObjstoreKey); exists {
		t.Fatal("missing frozen restored from mutable legacy")
	}
}
func TestFrozenIngestAliasConflictDoesNotCreateSuccessfulOverwrite(t *testing.T) {
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ing := &Ingester{reg: &fakeReg{}, store: store}
	ref := registry.PaperRef{DOI: "10.1000/frozen"}
	alias := paperassets.DOIAssetKey("pdf", ref.DOI)
	src, err := ing.registerFreshSource(context.Background(), ref, "doi:"+ref.DOI, alias, testPDF, testPDFSha(), int64(len(testPDF)))
	if err != nil {
		t.Fatal(err)
	}
	other := []byte("%PDF-different bytes")
	if _, err := ing.registerFreshSource(context.Background(), ref, "doi:"+ref.DOI, alias, other, paperbundle.SHA256(other), int64(len(other))); !errors.Is(err, paperbundle.ErrIntegrity) {
		t.Fatalf("conflict accepted %v", err)
	}
	got, err := paperbundle.New(store).ReadPDF(context.Background(), src.PaperID, src.SourceID, src.Sha256, src.SizeBytes)
	if err != nil || !bytes.Equal(got, testPDF) {
		t.Fatal("frozen source changed")
	}
}
