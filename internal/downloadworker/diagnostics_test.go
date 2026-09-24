package downloadworker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

type diagnosticFetcher struct{ out *downloader.FetchOutcome }

func (f diagnosticFetcher) FetchPDF(context.Context, registry.PaperRef) (*downloader.FetchOutcome, error) {
	return f.out, errors.New("upstream returned credential=TOPSECRET")
}

func TestFailureDiagnosticsPersistAndReportAfterRestart(t *testing.T) {
	s := testSpool(t, MaxPDFBytes, time.Hour)
	a := assignment("diagnostic")
	if err := s.Start(a, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	out := &downloader.FetchOutcome{Trace: []downloader.Attempt{
		{Strategy: "landing", URL: "https://user:TOPSECRET@publisher.invalid/TOPSECRET?token=TOPSECRET", Error: "TOPSECRET", FailureKind: "challenge", HTTPStatus: 403, Millis: 200},
		{Strategy: "browser", Error: "TOPSECRET", FailureKind: "challenge", HTTPStatus: 403, PageTitle: "Just a moment...", Millis: 1234},
	}}
	var logs bytes.Buffer
	r := Runner{Config: DefaultConfig(), Spool: s, Fetcher: diagnosticFetcher{out}, Log: slog.New(slog.NewTextHandler(&logs, nil)), active: map[string]activeTask{}, fatal: make(chan error, 1)}
	r.startTask(context.Background(), a)
	r.wg.Wait()
	records := s.Records()
	if len(records) != 1 || records[0].State != "failed" || records[0].Failure != wp.FailureNotFound {
		t.Fatalf("unexpected records: %+v", records)
	}
	want := records[0].Metadata.Trace
	if len(want) != 2 || want[1].Millis != 1234 || !strings.Contains(want[1].Error, "403") || !strings.Contains(want[1].Error, "Just a moment") {
		t.Fatalf("diagnostics lost: %+v", want)
	}
	if !strings.Contains(records[0].Error, "last strategy browser") {
		t.Fatal(records[0].Error)
	}
	encoded, _ := json.Marshal(records[0])
	if strings.Contains(string(encoded), "TOPSECRET") || strings.Contains(logs.String(), "TOPSECRET") {
		t.Fatal("raw secrets escaped into catalog or logs")
	}
	if !strings.Contains(logs.String(), "worker download strategy") || !strings.Contains(logs.String(), a.AttemptID) {
		t.Fatal("missing correlated strategy log")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenSpool(s.dir, MaxPDFBytes, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	defer recovered.Close()
	records = recovered.Records()
	if len(records) != 1 || !reflect.DeepEqual(records[0].Metadata.Trace, want) {
		t.Fatal("failure trace lost on restart")
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var report wp.ReportRequest
		if req.URL.Path != wp.ReportPath {
			t.Errorf("unexpected path: %s", req.URL.Path)
		}
		if err := json.NewDecoder(req.Body).Decode(&report); err != nil {
			t.Error(err)
		}
		if !reflect.DeepEqual(report.Trace, want) || report.AttemptID != a.AttemptID {
			t.Errorf("lost wire trace: %+v", report)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_ = json.NewEncoder(w).Encode(wp.Receipt{TaskID: a.TaskID, AttemptID: a.AttemptID, State: "failed"})
	}))
	defer server.Close()
	r.Spool = recovered
	r.Client = testClient(t, server.URL)
	defer r.Client.Close()
	if err := r.deliver(context.Background(), records[0]); err == nil {
		t.Fatal("expected first delivery failure")
	}
	if len(recovered.Records()) != 1 {
		t.Fatal("failed report discarded diagnostics")
	}
	if err := r.deliver(context.Background(), records[0]); err != nil {
		t.Fatal(err)
	}
	if len(recovered.Records()) != 0 {
		t.Fatal("acknowledged report not removed")
	}
}

func TestFailureTraceBoundedAndSanitized(t *testing.T) {
	out := &downloader.FetchOutcome{}
	for i := 0; i < 20; i++ {
		out.Trace = append(out.Trace, downloader.Attempt{Strategy: "pattern", Error: "TOPSECRET", Millis: int64(i)})
	}
	out.Trace = append(out.Trace, downloader.Attempt{Strategy: "TOPSECRET", Error: "TOPSECRET", FailureKind: "TOPSECRET", PageTitle: "TOPSECRET", HTTPStatus: 9999, Millis: -1})
	out.Trace = append(out.Trace, downloader.Attempt{Strategy: "browser", Error: "TOPSECRET", FailureKind: "challenge", PageTitle: "Access Denied", HTTPStatus: 403})
	trace := failureTrace(out)
	if len(trace) != 12 || trace[0].Millis != 10 || trace[11].Strategy != "browser" {
		t.Fatalf("final diagnostics truncated: %+v", trace)
	}
	if trace[10].Strategy != "other" || trace[10].Millis != 0 {
		t.Fatal(trace[10])
	}
	encoded, _ := json.Marshal(trace)
	if strings.Contains(string(encoded), "TOPSECRET") || strings.Contains(string(encoded), "9999") {
		t.Fatalf("untrusted text exported: %s", encoded)
	}
	if failureTrace(nil) != nil {
		t.Fatal("nil outcome")
	}
}

func TestFailureWithoutOutcomeStillReported(t *testing.T) {
	s := testSpool(t, MaxPDFBytes, time.Hour)
	a := assignment("no-outcome")
	if err := s.Start(a, 1, time.Now()); err != nil {
		t.Fatal(err)
	}
	r := Runner{Config: DefaultConfig(), Spool: s, Fetcher: diagnosticFetcher{}, active: map[string]activeTask{}, fatal: make(chan error, 1)}
	r.startTask(context.Background(), a)
	r.wg.Wait()
	records := s.Records()
	if len(records) != 1 || records[0].State != "failed" || records[0].Error != "download strategies did not produce a PDF" {
		t.Fatalf("unexpected result: %+v", records)
	}
}
