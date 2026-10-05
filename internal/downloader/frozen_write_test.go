package downloader

import (
	"bytes"
	"context"
	"errors"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperbundle"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"log/slog"
	"strings"
	"testing"
)

func TestFreshDownloaderPDFUsesOnlyFullContentKey(t *testing.T) {
	store := newLocalStore(t, t.TempDir())
	reg := newFakeReg()
	d := &Downloader{store: store, reg: reg}
	pdf := makePDF(20000)
	ref := registry.PaperRef{ArxivID: "2401.12345v2"}
	out := &FetchOutcome{ArxivCanonical: ref.ArxivID, ArxivVersion: 2, URL: "https://fixture.example/original.pdf", Result: &FetchResult{Body: bytes.NewReader(pdf), Sha256: "untrusted-worker-hash", Size: 1}}
	if err := d.storeOutcome(context.Background(), job{ref: ref}, out); err != nil {
		t.Fatal(err)
	}
	key := reg.paths["arxiv:"+ref.ArxivID]
	if !strings.HasPrefix(key, "content/") || !strings.HasSuffix(key, "/source.pdf") {
		t.Fatalf("legacy assetkey retained: %q", key)
	}
	if items, err := store.ListPrefix(context.Background(), "pdf/", 0); err != nil || len(items) != 0 {
		t.Fatalf("fresh oldbucket write %v %v", items, err)
	}
	if out.Result.Sha256 != paperbundle.SHA256(pdf) || out.Result.Size != int64(len(pdf)) {
		t.Fatal("claimed worker metadata retained")
	}
}
func TestDownloaderBoundMissingDoesNotReadLegacyOrFetch(t *testing.T) {
	ctx := context.Background()
	store := newLocalStore(t, t.TempDir())
	reg := newFakeReg()
	d := &Downloader{store: store, reg: reg, log: slog.Default(), progress: map[string]*Progress{}}
	ref := registry.PaperRef{ArxivID: "2401.12345v2"}
	alias := paperassets.AssetKey("pdf", ref.ArxivID)
	pdf := makePDF(20000)
	src, err := d.registerFrozenOutcome(ctx, ref, "arxiv:"+ref.ArxivID, alias, pdf)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Delete(ctx, src.ObjstoreKey); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Put(ctx, alias, bytes.NewReader(pdf), int64(len(pdf)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	d.process(job{paperID: "paper-missing", ref: ref, ctx: ctx, done: make(chan struct{})})
	if reg.statuses["paper-missing"] != "failed" {
		t.Fatal("missing frozen was not failed")
	}
	if _, exists, _ := store.Stat(ctx, src.ObjstoreKey); exists {
		t.Fatal("legacy bytes restored frozen source")
	}
}

type legacyOnlyRegistry struct{ registryWriter }

func TestDownloaderUnsupportedFrozenRegistryFailsClosed(t *testing.T) {
	store := newLocalStore(t, t.TempDir())
	d := &Downloader{store: store, reg: &legacyOnlyRegistry{newFakeReg()}}
	ref := registry.PaperRef{ArxivID: "2401.12345v2"}
	pdf := makePDF(20000)
	out := &FetchOutcome{ArxivCanonical: ref.ArxivID, Result: &FetchResult{Body: bytes.NewReader(pdf)}}
	if err := d.storeOutcome(context.Background(), job{ref: ref}, out); !errors.Is(err, registry.ErrCatalogUnavailable) {
		t.Fatalf("unsupported protocol fell back: %v", err)
	}
	if items, _ := store.ListPrefix(context.Background(), "", 0); len(items) != 0 {
		t.Fatalf("unsupported protocol wrote data: %v", items)
	}
}
