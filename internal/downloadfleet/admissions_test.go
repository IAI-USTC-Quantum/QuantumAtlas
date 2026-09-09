package downloadfleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

func TestRemotePendingPreservesReason(t *testing.T) {
	out, e := remotePending("known-task", context.Canceled)
	if out == nil || !out.Pending || out.Archived || out.RemoteTaskID != "known-task" || !errors.Is(e, downloader.ErrRemotePending) || !errors.Is(e, context.Canceled) {
		t.Fatal(out, e)
	}
}
func admitTask(t *testing.T, s *Service, request string, ref registry.PaperRef) string {
	t.Helper()
	raw, e := json.Marshal(ref)
	if e != nil {
		t.Fatal(e)
	}
	key, e := identity(ref)
	if e != nil {
		t.Fatal(e)
	}
	id, _, e := s.enqueue(downloader.WithAdmissionID(context.Background(), request), key, raw)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func TestPostgresAdmissionReplayKeepsTerminalFailure(t *testing.T) {
	s := postgresService(t)
	ref := registry.PaperRef{DOI: "10.1234/admission"}
	request := randomID()
	id := admitTask(t, s, request, ref)
	if _, e := s.pool.Exec(context.Background(), `UPDATE download_fleet_tasks SET state='failed',attempt_count=3,error='all eligible workers failed',deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	ctx := downloader.WithAdmissionID(context.Background(), request)
	for i := 0; i < 2; i++ {
		out, e := s.FetchPDF(ctx, ref)
		if e == nil || errors.Is(e, downloader.ErrRemotePending) || out == nil || out.RemoteTaskID != id || out.Pending {
			t.Fatalf("terminal replay reset or became pending: %+v %v", out, e)
		}
	}
	var count int
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM download_fleet_tasks`).Scan(&count); e != nil || count != 1 {
		t.Fatal(count, e)
	}
	// Explicit user retry has a fresh admission, and may start a new task budget.
	retry := randomID()
	retryID := admitTask(t, s, retry, ref)
	if retryID == id {
		t.Fatal("explicit retry was pinned to old failure")
	}
	if e := s.pool.QueryRow(ctx, `SELECT count(*) FROM download_fleet_tasks`).Scan(&count); e != nil || count != 2 {
		t.Fatal(count, e)
	}
}
func TestPostgresAdmissionReadsDoneAfterStoredDeadline(t *testing.T) {
	s := postgresService(t)
	ref := registry.PaperRef{DOI: "10.1234/recovered"}
	request := randomID()
	id := admitTask(t, s, request, ref)
	encoded, e := json.Marshal(downloader.FetchOutcome{Archived: true, RemoteTaskID: id, WorkerID: "recovered-worker"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.pool.Exec(context.Background(), `UPDATE download_fleet_tasks SET state='done',outcome=$2,deadline=clock_timestamp()-interval '1 day' WHERE id=$1`, id, encoded); e != nil {
		t.Fatal(e)
	}
	out, e := s.FetchPDF(downloader.WithAdmissionID(context.Background(), request), ref)
	if e != nil || out == nil || !out.Archived || out.Pending || out.RemoteTaskID != id {
		t.Fatal(out, e)
	}
}
func TestPostgresDetachedAndStagedWaitersStayPending(t *testing.T) {
	s := postgresService(t)
	ref := registry.PaperRef{DOI: "10.1234/still-staged"}
	request := randomID()
	id := admitTask(t, s, request, ref)
	if _, e := s.pool.Exec(context.Background(), `UPDATE download_fleet_tasks SET state='staged' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(downloader.WithAdmissionID(context.Background(), request), 100*time.Millisecond)
	defer cancel()
	out, e := s.FetchPDF(ctx, ref)
	if !errors.Is(e, downloader.ErrRemotePending) || !errors.Is(e, context.DeadlineExceeded) || out == nil || !out.Pending || out.RemoteTaskID != id {
		t.Fatal("detached waiter became terminal", out, e)
	}
	if _, e = s.pool.Exec(context.Background(), `UPDATE download_fleet_tasks SET deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, id); e != nil {
		t.Fatal(e)
	}
	out, e = s.FetchPDF(downloader.WithAdmissionID(context.Background(), request), ref)
	if !errors.Is(e, downloader.ErrRemotePending) || out == nil || !out.Pending || out.RemoteTaskID != id {
		t.Fatal("expired staged waiter became terminal", out, e)
	}
	var state string
	if e = s.pool.QueryRow(context.Background(), `SELECT state FROM download_fleet_tasks WHERE id=$1`, id).Scan(&state); e != nil || state != "staged" {
		t.Fatal(state, e)
	}
}
func TestPostgresArchiveAndHookCarryLinkedAdmission(t *testing.T) {
	s := postgresService(t)
	ctx := context.Background()
	ref := registry.PaperRef{DOI: "10.1234/context"}
	request := randomID()
	id := admitTask(t, s, request, ref)
	// A newer parent admission can attach to the same active identity; callbacks
	// fence completion using that newest request, never the superseded one.
	request = randomID()
	if linked := admitTask(t, s, request, ref); linked != id {
		t.Fatal("active admission failed to coalesce", linked, id)
	}
	n, _ := approvedNode(t, s)
	a := claimOne(t, s, n)
	if a.TaskID != id {
		t.Fatal(a)
	}
	var archived, hooked bool
	s.SetArchive(func(ctx context.Context, _ registry.PaperRef, _ *downloader.FetchOutcome) error {
		if got := downloader.AdmissionID(ctx); got != request {
			t.Errorf("archive admission=%q expected %q", got, request)
		}
		archived = true
		return nil
	})
	s.SetHooks(func(ctx context.Context, _ registry.PaperRef, _ *downloader.FetchOutcome) error {
		if got := downloader.AdmissionID(ctx); got != request {
			t.Errorf("hook admission=%q expected %q", got, request)
		}
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 31*time.Second {
			t.Error("hook callback has no bounded child deadline")
		}
		hooked = true
		return nil
	})
	data := testPDF()
	sum := sha256.Sum256(data)
	r, e := s.upload(ctx, n, a.AttemptID, hex.EncodeToString(sum[:]), int64(len(data)), "", "", wp.ResultMetadata{}, bytes.NewReader(data))
	if e != nil || r.State != "done" {
		t.Fatal(r, e)
	}
	if e = s.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	if !archived || !hooked {
		t.Fatal(archived, hooked)
	}
}
