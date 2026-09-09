package downloader

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

type lifecycleJournal struct {
	mu                 sync.Mutex
	finished           int
	adopted            bool
	pendingBeforeAdopt bool
	stopRecovery       context.CancelFunc
}

func (j *lifecycleJournal) SaveDownloadRequest(context.Context, string, string, string, registry.PaperRef) (string, error) {
	return "admission-lifecycle", nil
}
func (j *lifecycleJournal) FinishDownloadRequest(context.Context, string, string, string, string) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.finished++
	return nil
}
func (j *lifecycleJournal) PendingDownloadRequests(context.Context, int) ([]registry.DownloadRequest, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.pendingBeforeAdopt = !j.adopted
	return nil, nil
}
func (j *lifecycleJournal) PruneDownloadRequests(context.Context, time.Duration) error {
	if j.stopRecovery != nil {
		j.stopRecovery()
	}
	return nil
}
func (j *lifecycleJournal) AdoptPendingDownloadRequests(context.Context, int) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.adopted = true
	return nil
}

type lifecycleRemote struct{ entered chan context.Context }

func (r *lifecycleRemote) Enabled() bool { return true }
func (r *lifecycleRemote) FetchPDF(ctx context.Context, _ registry.PaperRef) (*FetchOutcome, error) {
	r.entered <- ctx
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestLifecycleShutdownCancelsOwnedWorkPreservesAdmission(t *testing.T) {
	journal := &lifecycleJournal{}
	remote := &lifecycleRemote{entered: make(chan context.Context, 1)}
	d := New(nil, nil, nil, nil, Config{Concurrency: 1, RemoteConcurrency: 1, Remote: remote, Journal: journal})
	t.Cleanup(func() { _ = d.Shutdown(context.Background()) })
	type requestValue struct{}
	requestCtx, disconnect := context.WithCancel(context.WithValue(context.Background(), requestValue{}, "kept"))
	defer disconnect()
	if !d.Enqueue(requestCtx, "paper-lifecycle", "2401.12345", KindArxiv, registry.PaperRef{ArxivID: "2401.12345"}) {
		t.Fatal("admission rejected")
	}
	var execution context.Context
	select {
	case execution = <-remote.entered:
	case <-time.After(time.Second):
		t.Fatal("remote never started")
	}
	disconnect()
	if execution.Err() != nil {
		t.Fatal("HTTP disconnect cancelled accepted work")
	}
	if execution.Value(requestValue{}) != "kept" || AdmissionID(execution) != "admission-lifecycle" {
		t.Fatal("request/admission values lost")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatalf("owned remote wait did not cancel: %v", err)
	}
	if !errors.Is(execution.Err(), context.Canceled) {
		t.Fatalf("execution error = %v", execution.Err())
	}
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if journal.finished != 0 {
		t.Fatalf("shutdown terminalized recoverable admission %d times", journal.finished)
	}
}

type blockedAdmissionRegistry struct {
	*fakeReg
	entered chan struct{}
	release chan struct{}
}

func (r *blockedAdmissionRegistry) RecordAcquisitionEvent(_ context.Context, _, phase, _, _ string) error {
	if phase == "queued" {
		close(r.entered)
		<-r.release
	}
	return nil
}

func TestLifecycleShutdownJoinsAndFencesPresendProducer(t *testing.T) {
	reg := &blockedAdmissionRegistry{fakeReg: newFakeReg(), entered: make(chan struct{}), release: make(chan struct{})}
	d := New(reg, nil, nil, nil, Config{Concurrency: 1})
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(reg.release) }) }
	t.Cleanup(func() { release(); _ = d.Shutdown(context.Background()) })
	if !d.Enqueue(context.Background(), "blocked", "2401.12345", KindArxiv, registry.PaperRef{ArxivID: "2401.12345"}) {
		t.Fatal("admission rejected")
	}
	select {
	case <-reg.entered:
	case <-time.After(time.Second):
		t.Fatal("pre-send producer not reached")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := d.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown failed to join producer: %v", err)
	}
	release()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	if err := d.Shutdown(ctx2); err != nil {
		t.Fatalf("second shutdown did not join: %v", err)
	}
	if len(d.jobs) != 0 || len(d.scheduled) != 0 {
		t.Fatalf("producer sent after drain: jobs=%d scheduled=%d", len(d.jobs), len(d.scheduled))
	}
}

func TestLifecycleConcurrentAdmissionAndShutdown(t *testing.T) {
	d := New(nil, nil, nil, nil, Config{Concurrency: 2})
	if d.maxScheduled != cap(d.jobs) {
		t.Fatal("admission bound must reserve a nonblocking queue send")
	}
	var callers sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < 256; i++ {
		callers.Add(1)
		go func(i int) {
			defer callers.Done()
			<-start
			d.Enqueue(context.Background(), fmt.Sprint(i), "2401.12345", KindArxiv, registry.PaperRef{ArxivID: "2401.12345"})
		}(i)
	}
	close(start)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	callers.Wait()
	if len(d.jobs) != 0 || len(d.scheduled) != 0 {
		t.Fatalf("undrained admissions: jobs=%d scheduled=%d", len(d.jobs), len(d.scheduled))
	}
	if d.Enqueue(context.Background(), "after", "2401.12345", KindArxiv, registry.PaperRef{ArxivID: "2401.12345"}) {
		t.Fatal("accepted after shutdown")
	}
}

func TestLifecycleRecoveryAdoptsBeforeReadingJournal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	journal := &lifecycleJournal{stopRecovery: cancel}
	d := New(nil, nil, nil, nil, Config{Journal: journal})
	defer d.Shutdown(context.Background())
	d.RunRecovery(ctx)
	journal.mu.Lock()
	defer journal.mu.Unlock()
	if !journal.adopted || journal.pendingBeforeAdopt {
		t.Fatal("pending admission recovery did not run adoption first")
	}
}
