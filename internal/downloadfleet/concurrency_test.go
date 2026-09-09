package downloadfleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

func TestPostgresFetchDedupSurvivesCallerCancellation(t *testing.T) {
	s := postgresService(t)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, e := s.FetchPDF(ctx, registry.PaperRef{DOI: "https://doi.org/10.1234/dedup"})
			errs <- e
		}()
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		if !errors.Is(e, context.DeadlineExceeded) {
			t.Error(e)
		}
	}
	var count int
	if e := s.pool.QueryRow(context.Background(), `SELECT count(*) FROM download_fleet_tasks WHERE identity='doi:10.1234/dedup' AND state='queued'`).Scan(&count); e != nil || count != 1 {
		t.Fatalf("durable dedup count=%d err=%v", count, e)
	}
	n, _ := approvedNode(t, s)
	a := claimOne(t, s, n)
	if a.Ref.DOI != "https://doi.org/10.1234/dedup" {
		t.Fatal(a)
	}
}

type gatedReader struct {
	once    sync.Once
	started chan struct{}
	release chan struct{}
	body    io.Reader
}

func (g *gatedReader) Read(p []byte) (int, error) {
	g.once.Do(func() { close(g.started); <-g.release })
	return g.body.Read(p)
}
func TestPostgresCleanupCannotRaceActiveTransfer(t *testing.T) {
	s := postgresService(t)
	s.cfg.LeaseDuration = 150 * time.Millisecond
	s.cfg.WorkerTimeout = time.Second
	s.cfg.UploadTimeout = 150 * time.Millisecond
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, _ := approvedNode(t, s)
	seedTask(t, s, "stream")
	a := claimOne(t, s, n)
	data := testPDF()
	sum := sha256.Sum256(data)
	body := &gatedReader{started: make(chan struct{}), release: make(chan struct{}), body: bytes.NewReader(data)}
	done := make(chan error, 1)
	go func() {
		_, e := s.upload(ctx, n, a.AttemptID, hex.EncodeToString(sum[:]), int64(len(data)), "", "", wp.ResultMetadata{}, body)
		done <- e
	}()
	select {
	case <-body.started:
	case e := <-done:
		t.Fatalf("upload failed before streaming: %v", e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	// Let the original lease expire while the transfer still owns row locks.
	select {
	case <-time.After(200 * time.Millisecond):
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if e := s.Maintain(ctx); e != nil {
		close(body.release)
		t.Fatal(e)
	}
	r, e := s.receipt(ctx, n.ID, a.AttemptID)
	if e != nil || r.State != "running" {
		close(body.release)
		t.Fatal("cleanup changed a live transfer", r, e)
	}
	close(body.release)
	if e = <-done; !errors.Is(e, ErrConflict) {
		t.Fatal("expired upload accepted", e)
	}
	if e = s.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	r, e = s.receipt(ctx, n.ID, a.AttemptID)
	if e != nil || r.State != "expired" {
		t.Fatal(r, e)
	}
}
