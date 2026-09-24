package downloadfleet

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

func TestQueuedExpiryError(t *testing.T) {
	const timeout = "fleet final reason: queued timeout (task deadline expired while queued)"
	const limit = "fleet final reason: attempt limit reached while queued"
	for _, tc := range []struct {
		name, previous, want string
		deadline, limit      bool
	}{
		{"never assigned", "", timeout, true, false},
		{"worker failure", "HTTP 403: publisher challenge", "HTTP 403: publisher challenge; " + timeout, true, false},
		{"attempt limit", "network unavailable", "network unavailable; " + limit, false, true},
		{"both reasons", "not found", "not found; " + timeout + "; attempt limit reached while queued", true, true},
		{"already final", "network unavailable; " + timeout, "network unavailable; " + timeout, true, false},
		{"no expiry", "network unavailable", "network unavailable", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := queuedExpiryError(tc.previous, tc.deadline, tc.limit)
			if got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
			if again := queuedExpiryError(got, tc.deadline, tc.limit); again != got {
				t.Fatalf("duplicate final reason: %q", again)
			}
		})
	}
	// A full-length report, including its tail, must survive the added reason.
	original := strings.Repeat("x", 2042) + "原错"
	got := queuedExpiryError(original, true, false)
	if got != original+"; "+timeout || !utf8.ValidString(got) {
		t.Fatalf("full-length original failure lost: %q", got)
	}
}

func TestPostgresFailedFetchReturnsWorkerTrace(t *testing.T) {
	s := postgresService(t)
	ref := registry.PaperRef{DOI: "10.1234/failed-trace"}
	request := randomID()
	task := admitTask(t, s, request, ref)
	ctx := downloader.WithAdmissionID(context.Background(), request)
	var attempts []wp.Assignment
	var expected []downloader.Attempt
	for _, failure := range []string{wp.FailureNetwork, wp.FailureChallenge} {
		node, _ := approvedNode(t, s)
		a := claimOne(t, s, node)
		attempts = append(attempts, a)
		steps := []wp.Trace{
			{Strategy: "landing", URL: "https://publisher.invalid/article", Error: "HTTP 403", Millis: 17},
			{Strategy: "browser", URL: "https://publisher.invalid/p.pdf", Error: "challenge", Millis: 29},
		}
		if _, err := s.report(ctx, node, wp.ReportRequest{AttemptID: a.AttemptID, Failure: failure, Error: failure + " original", Trace: steps}); err != nil {
			t.Fatal(err)
		}
		prefix := "worker:" + node.ID
		expected = append(expected, downloader.Attempt{Strategy: prefix, Error: failure + ": " + failure + " original"})
		for _, step := range steps {
			expected = append(expected, downloader.Attempt{Strategy: prefix + ":" + step.Strategy, URL: step.URL, Error: step.Error, Millis: step.Millis})
		}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE download_fleet_tasks SET current_attempt=NULL,deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, task); err != nil {
		t.Fatal(err)
	}
	if err := s.Maintain(ctx); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		out, err := s.FetchPDF(ctx, ref)
		if err == nil || errors.Is(err, downloader.ErrRemotePending) || !strings.Contains(err.Error(), wp.FailureChallenge+" original; fleet final reason: queued timeout") || out == nil || out.Pending || out.Archived || out.RemoteTaskID != task {
			t.Fatalf("lost terminal failure: %+v, %v", out, err)
		}
		if len(out.Trace) != len(expected) {
			t.Fatalf("trace=%+v, want %+v", out.Trace, expected)
		}
		for j := range expected {
			if out.Trace[j] != expected[j] {
				t.Fatalf("trace[%d]=%+v, want %+v", j, out.Trace[j], expected[j])
			}
		}
	}
	// Corrupt historical diagnostics must not mask or reclassify the task's
	// failure. Other attempts' valid traces remain available.
	if _, err := s.pool.Exec(ctx, `UPDATE download_fleet_attempts SET trace='{}'::jsonb WHERE id=$1`, attempts[0].AttemptID); err != nil {
		t.Fatal(err)
	}
	out, err := s.FetchPDF(ctx, ref)
	if err == nil || errors.Is(err, downloader.ErrRemotePending) || !strings.Contains(err.Error(), "queued timeout") || !strings.Contains(err.Error(), "load remote failure trace") || out == nil || out.Pending || out.Archived || len(out.Trace) != 4 || out.Trace[3] != expected[5] {
		t.Fatalf("trace error masked terminal failure: %+v, %v", out, err)
	}
}

func TestPostgresQueuedExpiryPreservesWorkerFailure(t *testing.T) {
	s := postgresService(t)
	ctx := context.Background()
	const original = "HTTP 403: publisher challenge, not an execution timeout"
	for _, tc := range []struct {
		name                             string
		report, clearCurrent, laterLease bool
		deadline, limit                  bool
	}{
		{name: "never assigned", deadline: true},
		{name: "worker report", report: true, deadline: true},
		{name: "null current attempt", report: true, clearCurrent: true, deadline: true},
		{name: "later lease expiry", report: true, laterLease: true, deadline: true},
		{name: "attempt limit only", report: true, clearCurrent: true, limit: true},
		{name: "deadline and attempt limit", report: true, deadline: true, limit: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			task := seedTask(t, s, strings.ReplaceAll(tc.name, " ", "-"))
			var node wp.Node
			var attempt wp.Assignment
			trace := []wp.Trace{{Strategy: "publisher", Error: original}}
			if tc.report {
				node, _ = approvedNode(t, s)
				attempt = claimOne(t, s, node)
				if _, err := s.report(ctx, node, wp.ReportRequest{AttemptID: attempt.AttemptID, Failure: wp.FailureChallenge, Error: original, Trace: trace}); err != nil {
					t.Fatal(err)
				}
			}
			if tc.laterLease {
				other, _ := approvedNode(t, s)
				later := claimOne(t, s, other)
				if _, err := s.pool.Exec(ctx, `UPDATE download_fleet_attempts SET lease_expires=clock_timestamp()-interval '1 second' WHERE id=$1`, later.AttemptID); err != nil {
					t.Fatal(err)
				}
				if err := s.expire(ctx); err != nil {
					t.Fatal(err)
				}
				var state, msg string
				if err := s.pool.QueryRow(ctx, `SELECT state,error FROM download_fleet_tasks WHERE id=$1`, task).Scan(&state, &msg); err != nil || state != "queued" || msg != "worker lease/execution deadline expired" {
					t.Fatalf("running expiry changed: state=%q error=%q err=%v", state, msg, err)
				}
			}
			if _, err := s.pool.Exec(ctx, `UPDATE download_fleet_tasks SET current_attempt=CASE WHEN $2 THEN NULL ELSE current_attempt END,error=CASE WHEN $2 THEN '' ELSE error END,deadline=CASE WHEN $3 THEN clock_timestamp()-interval '1 second' ELSE deadline END,attempt_count=CASE WHEN $4 THEN $5 ELSE attempt_count END WHERE id=$1`, task, tc.clearCurrent, tc.deadline, tc.limit, s.cfg.MaxWorkerAttempts); err != nil {
				t.Fatal(err)
			}
			previous := ""
			if tc.report {
				previous = original
			}
			want := queuedExpiryError(previous, tc.deadline, tc.limit)
			for i := 0; i < 2; i++ {
				if err := s.Maintain(ctx); err != nil {
					t.Fatal(err)
				}
				var state, msg string
				if err := s.pool.QueryRow(ctx, `SELECT state,error FROM download_fleet_tasks WHERE id=$1`, task).Scan(&state, &msg); err != nil || state != "failed" || msg != want {
					t.Fatalf("maintenance %d: state=%q error=%q want=%q err=%v", i, state, msg, want, err)
				}
			}
			if tc.report {
				receipt, err := s.receipt(ctx, node.ID, attempt.AttemptID)
				if err != nil || receipt.State != "failed" || receipt.Error != original {
					t.Fatalf("original receipt changed: %+v, %v", receipt, err)
				}
				var failure string
				var raw []byte
				if err := s.pool.QueryRow(ctx, `SELECT failure,trace FROM download_fleet_attempts WHERE id=$1`, attempt.AttemptID).Scan(&failure, &raw); err != nil {
					t.Fatal(err)
				}
				var gotTrace []wp.Trace
				if err := json.Unmarshal(raw, &gotTrace); err != nil || failure != wp.FailureChallenge || len(gotTrace) != 1 || gotTrace[0] != trace[0] {
					t.Fatalf("worker failure/trace changed: %q %s, %v", failure, raw, err)
				}
			}
		})
	}
}
