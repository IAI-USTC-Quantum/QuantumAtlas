package downloader

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

func TestRemoteDetachedWaiterReconcilesOnLateArchive(t *testing.T) {
	journal := &lifecycleJournal{}
	reg := &mintingReg{newFakeReg()}
	remote := &fakeRemote{fetch: func(context.Context, registry.PaperRef) (*FetchOutcome, error) {
		return &FetchOutcome{Pending: true, RemoteTaskID: "remote-task"}, ErrRemotePending
	}}
	d := New(reg, newLocalStore(t, t.TempDir()), nil, nil, Config{Remote: remote, Journal: journal})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	ctx := WithAdmissionID(context.Background(), "admission-lifecycle")
	ref := registry.PaperRef{ArxivID: "2401.12345"}
	d.beginProgress(ctx, "paper-remote", ref.ArxivID, KindArxiv)
	d.process(job{paperID: "paper-remote", ref: ref, ctx: ctx, done: make(chan struct{})})
	journal.mu.Lock()
	finished := journal.finished
	journal.mu.Unlock()
	if finished != 0 || reg.statuses["paper-remote"] == "failed" {
		t.Fatal("detached waiter permanently failed recoverable task")
	}
	if p := d.Snapshot()[0]; p.State != "queued" || !p.Active {
		t.Fatalf("pending snapshot=%+v", p)
	}
	pdf := makePDF(16384)
	out := &FetchOutcome{ArxivCanonical: "2401.12345v1", ArxivVersion: 1, Strategy: "worker:campus:browser", Result: &FetchResult{Body: bytes.NewReader(pdf), Size: int64(len(pdf)), Sha256: "verified"}}
	d.progressMu.Lock()
	d.progress["paper-remote"].Error = "stale transient error"
	d.progressMu.Unlock()
	if err := d.ArchiveRemote(ctx, ref, out); err != nil {
		t.Fatal(err)
	}
	if p := d.Snapshot()[0]; p.State != "done" || p.Active || p.Error != "" || p.Strategy != out.Strategy {
		t.Fatalf("late archive not reconciled: %+v", p)
	}
	journal.mu.Lock()
	finished = journal.finished
	journal.mu.Unlock()
	if finished != 1 {
		t.Fatalf("late archive did not finish admission: %d", finished)
	}
}
func TestRemoteUsesResolvedPMCIdentity(t *testing.T) {
	var got registry.PaperRef
	remote := &fakeRemote{fetch: func(_ context.Context, ref registry.PaperRef) (*FetchOutcome, error) {
		got = ref
		return &FetchOutcome{Archived: true, DOI: ref.DOI}, nil
	}}
	d := &Downloader{cfg: Config{Remote: remote}}
	if !d.tryRemote(context.Background(), registry.PaperRef{DOI: "pmc:PMC12345"}, &FetchOutcome{DOI: "10.1000/resolved"}) || got.DOI != "10.1000/resolved" {
		t.Fatalf("delegated unresolved identity: %+v", got)
	}
}
func TestArchiveRejectsCorruptExistingObject(t *testing.T) {
	ctx := context.Background()
	store := newLocalStore(t, t.TempDir())
	reg := newFakeReg()
	doi := "10.1000/corrupt"
	bad := []byte("<html>access denied" + strings.Repeat("x", 16384))
	if _, err := store.Put(ctx, paperassets.DOIAssetKey("pdf", doi), bytes.NewReader(bad), int64(len(bad)), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	d := &Downloader{reg: reg, store: store}
	pdf := makePDF(16384)
	out := &FetchOutcome{DOI: doi, URL: "https://new-source.example/paper.pdf", Result: &FetchResult{Body: bytes.NewReader(pdf), Size: int64(len(pdf)), Sha256: "new"}}
	err := d.storeOutcome(ctx, job{ref: registry.PaperRef{DOI: doi}}, out)
	if !errors.Is(err, ErrNotPDF) || len(reg.upsertDOI) != 0 {
		t.Fatalf("corrupt existing object accepted: %v %+v", err, reg.upsertDOI)
	}
}
func TestStoredPDFValidatorCrossChunkXref(t *testing.T) {
	pdf := []byte("%PDF-1.4\n" + strings.Repeat("x", 16384) + "startxref\n0")
	// One-byte chunks exercise the cross-write signature detector.
	if _, n, err := inspectStoredPDF(&oneByteReader{data: pdf}); err != nil || n != int64(len(pdf)) {
		t.Fatalf("n=%d err=%v", n, err)
	}
}

type oneByteReader struct{ data []byte }

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

type snapshotJournal struct {
	lifecycleJournal
	requests []registry.DownloadRequest
}

func (j *snapshotJournal) PendingDownloadRequests(context.Context, int) ([]registry.DownloadRequest, error) {
	return j.requests, nil
}
func TestSnapshotIncludesDurableOverflowAndFencesOldGeneration(t *testing.T) {
	now := time.Now()
	journal := &snapshotJournal{requests: []registry.DownloadRequest{{PaperID: "paper-queued", RequestID: "new", Input: "10.1000/test", Kind: "doi", CreatedAt: now, UpdatedAt: now}}}
	d := New(nil, nil, nil, nil, Config{Journal: journal})
	defer d.Shutdown(context.Background())
	d.beginProgress(WithAdmissionID(context.Background(), "old"), "paper-queued", "old", KindDOI)
	d.transition(WithAdmissionID(context.Background(), "old"), "paper-queued", "pdf_ready", "done", "", true)
	snapshot := d.Snapshot()
	if len(snapshot) != 1 || snapshot[0].RequestID != "new" || snapshot[0].State != "queued" || !snapshot[0].Active {
		t.Fatalf("durable generation missing: %+v", snapshot)
	}
	d.beginProgress(WithAdmissionID(context.Background(), "new"), "paper-queued", "new", KindDOI)
	d.transition(WithAdmissionID(context.Background(), "old"), "paper-queued", "pdf_ready", "done", "", true)
	if p := d.Snapshot()[0]; p.RequestID != "new" || p.State != "queued" {
		t.Fatalf("old callback overwrote new generation: %+v", p)
	}
}
