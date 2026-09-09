package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type fakeRemote struct {
	fetch func(context.Context, registry.PaperRef) (*FetchOutcome, error)
	calls int
}

func (f *fakeRemote) Enabled() bool { return f != nil }
func (f *fakeRemote) FetchPDF(ctx context.Context, r registry.PaperRef) (*FetchOutcome, error) {
	f.calls++
	return f.fetch(ctx, r)
}

func TestRemotePreservesArchiveIdentityAndReleasesLocalPermit(t *testing.T) {
	slots := make(chan struct{}, 1)
	permit := &localPermit{slots: slots}
	ctx := context.WithValue(context.Background(), localPermitKey{}, permit)
	if err := permit.acquire(ctx); err != nil {
		t.Fatal(err)
	}
	remote := &fakeRemote{fetch: func(context.Context, registry.PaperRef) (*FetchOutcome, error) {
		if len(slots) != 0 {
			t.Error("remote wait holds a local fetch slot")
		}
		return &FetchOutcome{Archived: true, WorkerID: "worker-a", RemoteTaskID: "task-1", ArxivCanonical: "2401.12345v2", ArxivVersion: 2, Strategy: "remote-worker:browser", URL: "https://arxiv.org/pdf/2401.12345v2", Trace: []Attempt{{Strategy: "browser"}}}, nil
	}}
	d := &Downloader{cfg: Config{Remote: remote}}
	out := &FetchOutcome{Trace: []Attempt{{Strategy: "local"}}}
	if !d.tryRemote(ctx, registry.PaperRef{ArxivID: "2401.12345"}, out) {
		t.Fatal("delegation failed")
	}
	if !out.Archived || out.WorkerID != "worker-a" || out.ArxivVersion != 2 || len(out.Trace) != 3 {
		t.Fatalf("outcome=%+v", out)
	}
	if len(slots) != 1 {
		t.Fatal("local slot not reacquired before fallback")
	}
	permit.release()
}
func TestRemoteFailureMarker(t *testing.T) {
	d := &Downloader{cfg: Config{Remote: &fakeRemote{fetch: func(context.Context, registry.PaperRef) (*FetchOutcome, error) { return nil, errors.New("no workers") }}}}
	out := &FetchOutcome{}
	if d.tryRemote(context.Background(), registry.PaperRef{DOI: "10.1000/test"}, out) || !sawStrategy(out.Trace, "remote-worker") {
		t.Fatal("failure must mark delegation already attempted")
	}
}
func TestArxivOnlyFallsBackToFleet(t *testing.T) {
	remote := &fakeRemote{fetch: func(context.Context, registry.PaperRef) (*FetchOutcome, error) {
		return &FetchOutcome{Archived: true, ArxivCanonical: "2401.12345v1"}, nil
	}}
	d := &Downloader{cfg: Config{Remote: remote}}
	out, err := d.FetchPDF(context.Background(), registry.PaperRef{ArxivID: "2401.12345"})
	if err != nil || !out.Archived || remote.calls != 1 {
		t.Fatalf("out=%+v err=%v calls=%d", out, err, remote.calls)
	}
}
func TestExistingObjectRegistersActualHash(t *testing.T) {
	ctx := context.Background()
	store := newLocalStore(t, t.TempDir())
	reg := newFakeReg()
	d := &Downloader{store: store, reg: reg}
	original := makePDF(16384)
	doi := "10.1000/conflict"
	_, err := store.Put(ctx, paperassets.DOIAssetKey("pdf", doi), bytes.NewReader(original), int64(len(original)), "application/pdf")
	if err != nil {
		t.Fatal(err)
	}
	replacement := makePDF(20000)
	out := &FetchOutcome{DOI: doi, Strategy: "remote-worker:browser", Result: &FetchResult{Body: bytes.NewReader(replacement), Size: int64(len(replacement)), Sha256: "not-the-stored-hash"}}
	if err := d.storeOutcome(ctx, job{ref: registry.PaperRef{DOI: doi}}, out); err != nil {
		t.Fatal(err)
	}
	h := sha256.Sum256(original)
	want := hex.EncodeToString(h[:])
	if reg.upsertDOI[doi] != want || out.Result.Size != int64(len(original)) {
		t.Fatalf("registered wrong object: %s size %d", reg.upsertDOI[doi], out.Result.Size)
	}
}

type mintingReg struct{ *fakeReg }

func (m *mintingReg) ResolveOrMint(context.Context, registry.PaperRef) (string, bool, error) {
	return "paper-remote", false, nil
}
func TestRemoteArchiveDoesNotWaitForMinerU(t *testing.T) {
	ctx := context.Background()
	reg := &mintingReg{newFakeReg()}
	calls := 0
	d := &Downloader{store: newLocalStore(t, t.TempDir()), reg: reg, onPDFReady: func(context.Context, string, bool) { calls++ }, progress: map[string]*Progress{}}
	pdf := makePDF(16384)
	h := sha256.Sum256(pdf)
	out := &FetchOutcome{DOI: "10.1000/archive", Strategy: "remote-worker:browser", Result: &FetchResult{Body: bytes.NewReader(pdf), Size: int64(len(pdf)), Sha256: hex.EncodeToString(h[:])}}
	if err := d.ArchiveRemote(ctx, registry.PaperRef{DOI: out.DOI}, out); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("archive invoked MinerU before durable outbox")
	}
	if reg.upsertDOI[out.DOI] == "" {
		t.Fatal("archive did not register PDF")
	}
	if err := d.AfterRemoteArchive(ctx, registry.PaperRef{DOI: out.DOI}, out); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("post-archive hook not invoked")
	}
}
