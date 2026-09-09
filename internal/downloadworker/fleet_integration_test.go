package downloadworker_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloadfleet"
	worker "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloadworker"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in integration uses only a disposable DB's unique schema, a loopback
// httptest master, and synthetic PDFs. No publisher/browser network is used.
func integrationMaster(t *testing.T) (*downloadfleet.Service, *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("TEST_DOWNLOADFLEET_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DOWNLOADFLEET_DATABASE_URL to an isolated disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	var id [16]byte
	if _, err = rand.Read(id[:]); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := "worker_e2e_" + hex.EncodeToString(id[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
		admin.Close()
	})
	config, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	config.MaxConns = 20
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	migration, err := os.ReadFile("../registry/migrations/00004_downloadfleet.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, strings.Split(string(migration), "-- +goose Down")[0]); err != nil {
		t.Fatal(err)
	}
	service, err := downloadfleet.New(pool, downloadfleet.Config{Enabled: true, SpoolDir: t.TempDir(), MaxInFlight: 2, MaxWorkerInFlight: 1, LeaseDuration: 10 * time.Second, WorkerTimeout: 45 * time.Second, TaskTimeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	return service, pool
}
func eventually(t *testing.T, description string, condition func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatal("timed out: " + description)
		}
	}
}
func syntheticPDF() []byte {
	return []byte("%PDF-1.7\n" + strings.Repeat("synthetic test document\n", 1024) + "\nstartxref\n0\n%%EOF\n")
}

type integrationFetcher struct {
	calls  atomic.Int32
	active atomic.Int32
	peak   atomic.Int32
	gate   <-chan struct{}
	fail   bool
}

func (f *integrationFetcher) FetchPDF(ctx context.Context, ref registry.PaperRef) (*downloader.FetchOutcome, error) {
	f.calls.Add(1)
	n := f.active.Add(1)
	defer f.active.Add(-1)
	for {
		old := f.peak.Load()
		if old >= n || f.peak.CompareAndSwap(old, n) {
			break
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if f.fail {
		return nil, downloader.ErrNoPDF
	}
	body := syntheticPDF()
	return &downloader.FetchOutcome{DOI: ref.DOI, Strategy: "synthetic", URL: "https://publisher.invalid/test.pdf", Trace: []downloader.Attempt{{Strategy: "synthetic", Millis: 1}}, Result: &downloader.FetchResult{Body: bytes.NewReader(body), Size: int64(len(body))}}, nil
}

type integrationWorker struct {
	spool  *worker.Spool
	client *worker.Client
	id     worker.Identity
	stop   context.CancelFunc
	done   chan error
}

func launchWorker(t *testing.T, service *downloadfleet.Service, masterURL string, fetcher *integrationFetcher) *integrationWorker {
	t.Helper()
	enrollment, err := service.Enrollment(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cfg := worker.DefaultConfig()
	cfg.MasterURL = masterURL
	cfg.AllowHTTP = true
	cfg.Name = "integration-worker"
	cfg.EnrollmentToken = enrollment.Token
	cfg.DataDir = t.TempDir()
	cfg.PollInterval = 25 * time.Millisecond
	cfg.MaxSpoolBytes = 3 * worker.MaxPDFBytes
	spool, err := worker.OpenSpool(cfg.DataDir, cfg.MaxSpoolBytes, cfg.ResultTTL)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := spool.LoadIdentity(masterURL)
	if err != nil {
		spool.Close()
		t.Fatal(err)
	}
	client, err := worker.NewClient(cfg, identity)
	if err != nil {
		spool.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &integrationWorker{spool: spool, client: client, id: identity, stop: cancel, done: make(chan error, 1)}
	r := &worker.Runner{Config: cfg, Spool: spool, Client: client, Identity: identity, Fetcher: fetcher, BrowserHealthy: func(context.Context) bool { return true }}
	go func() { w.done <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-w.done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("runner failed to shut down")
		}
		client.Close()
		if err := spool.Close(); err != nil {
			t.Error(err)
		}
	})
	eventually(t, "pending enrollment", func() bool {
		node, err := client.Status(context.Background())
		return err == nil && node.ID == identity.ID && node.Status == "pending"
	})
	return w
}
func queueRemote(t *testing.T, service *downloadfleet.Service, pool *pgxpool.Pool, doi string) <-chan error {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	result := make(chan error, 1)
	go func() {
		out, err := service.FetchPDF(ctx, registry.PaperRef{DOI: doi})
		if err == nil && (out == nil || !out.Archived) {
			err = errors.New("fleet returned non-archived success")
		}
		result <- err
	}()
	eventually(t, "queued remote task", func() bool {
		var exists bool
		err := pool.QueryRow(context.Background(), `SELECT EXISTS(SELECT 1 FROM download_fleet_tasks WHERE identity=$1)`, "doi:"+doi).Scan(&exists)
		return err == nil && exists
	})
	return result
}

func TestPostgresOutboundRunnerArchiveAndReceipt(t *testing.T) {
	for _, staged := range []bool{false, true} {
		name := "lost_archive_ack"
		if staged {
			name = "staged_archive_retry"
		}
		t.Run(name, func(t *testing.T) {
			service, pool := integrationMaster(t)
			var archiveAllowed atomic.Bool
			archiveAllowed.Store(!staged)
			var archiveCalls, archiveSuccesses, hooks, uploads atomic.Int32
			service.SetArchive(func(_ context.Context, ref registry.PaperRef, out *downloader.FetchOutcome) error {
				archiveCalls.Add(1)
				if !archiveAllowed.Load() {
					return errors.New("synthetic object store outage")
				}
				body, err := io.ReadAll(out.Result.Body)
				traceFound := false
				for _, attempt := range out.Trace {
					if attempt.Strategy == out.Strategy && attempt.Millis == 1 {
						traceFound = true
					}
				}
				if err != nil || !bytes.Equal(body, syntheticPDF()) || ref.DOI != "10.1234/runner" || out.Strategy != "worker:"+out.WorkerID+":synthetic" || !traceFound || out.WorkerID == "" {
					t.Errorf("archive callback mismatch: readerr=%v bytes=%d equal=%v DOI=%q strategy=%q worker=%q trace=%+v", err, len(body), bytes.Equal(body, syntheticPDF()), ref.DOI, out.Strategy, out.WorkerID, out.Trace)
				}
				archiveSuccesses.Add(1)
				return nil
			})
			service.SetHooks(func(_ context.Context, _ registry.PaperRef, out *downloader.FetchOutcome) error {
				if !out.Archived {
					t.Error("hook preceded durable archive")
				}
				hooks.Add(1)
				return nil
			})
			uploadProcessed := make(chan wp.Receipt, 1)
			handler := service.WorkerHandler()
			master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodPut && strings.HasPrefix(r.URL.Path, wp.UploadPath) {
					uploads.Add(1)
					recorded := httptest.NewRecorder()
					handler.ServeHTTP(recorded, r)
					var receipt wp.Receipt
					if err := json.Unmarshal(recorded.Body.Bytes(), &receipt); err != nil || recorded.Code != 200 {
						t.Errorf("upload rejected: status=%d response=%s", recorded.Code, recorded.Body.String())
					}
					select {
					case uploadProcessed <- receipt:
					default:
					}
					// Drop the entire response AFTER the master commits, simulating a lost
					// network acknowledgement, not an unprocessed request.
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					_ = conn.Close()
					return
				}
				handler.ServeHTTP(w, r)
			}))
			t.Cleanup(master.Close)
			fetcher := &integrationFetcher{}
			w := launchWorker(t, service, master.URL, fetcher)
			result := queueRemote(t, service, pool, "10.1234/runner")
			if fetcher.calls.Load() != 0 {
				t.Fatal("pending worker fetched without approval")
			}
			if err := service.Action(context.Background(), w.id.ID, "approve"); err != nil {
				t.Fatal(err)
			}
			var receipt wp.Receipt
			select {
			case receipt = <-uploadProcessed:
			case <-time.After(8 * time.Second):
				t.Fatal("outbound upload never reached master")
			}
			if staged {
				if receipt.State != "staged" || archiveSuccesses.Load() != 0 {
					t.Fatal("expected staged, not archived", receipt)
				}
				records := w.spool.Records()
				if len(records) != 1 || records[0].State != "ready" {
					t.Fatal("lost/staged receipt deleted local result")
				}
				if err := service.Maintain(context.Background()); err == nil {
					t.Fatal("staged archive outage was not surfaced")
				}
				records = w.spool.Records()
				if len(records) != 1 || records[0].State != "ready" {
					t.Fatal("archive retry failure deleted worker PDF")
				}
				archiveAllowed.Store(true)
			} else if receipt.State != "done" {
				t.Fatal("expected archived receipt", receipt)
			}
			if err := service.Maintain(context.Background()); err != nil {
				t.Fatal(err)
			}
			eventually(t, "matching archived receipt removes worker spool", func() bool { return len(w.spool.Records()) == 0 })
			select {
			case err := <-result:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("fleet requester never saw archived success")
			}
			if fetcher.calls.Load() != 1 || uploads.Load() != 1 || archiveSuccesses.Load() != 1 || hooks.Load() != 1 {
				t.Fatalf("non-idempotent pipeline: fetch=%d upload=%d archived=%d hook=%d", fetcher.calls.Load(), uploads.Load(), archiveSuccesses.Load(), hooks.Load())
			}
			if staged && archiveCalls.Load() < 3 {
				t.Fatal("staged archive retry was not exercised")
			}
			// Receipt remains available after local body removal and repeated queries.
			for range 2 {
				got, err := w.client.Receipt(context.Background(), receipt.AttemptID)
				if err != nil || got.State != "done" || got.SHA256 != receipt.SHA256 || got.Size != receipt.Size {
					t.Fatal("receipt was destructive or lost integrity", got, err)
				}
			}
		})
	}
}

func TestPostgresOutboundTwoWorkersShareBoundedCapacity(t *testing.T) {
	service, pool := integrationMaster(t)
	var archived atomic.Int32
	service.SetArchive(func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error { archived.Add(1); return nil })
	master := httptest.NewServer(service.WorkerHandler())
	t.Cleanup(master.Close)
	gate := make(chan struct{})
	firstFetcher := &integrationFetcher{gate: gate}
	secondFetcher := &integrationFetcher{gate: gate}
	first := launchWorker(t, service, master.URL, firstFetcher)
	second := launchWorker(t, service, master.URL, secondFetcher)
	results := []<-chan error{}
	for _, doi := range []string{"10.1234/capacity-a", "10.1234/capacity-b", "10.1234/capacity-c"} {
		results = append(results, queueRemote(t, service, pool, doi))
	}
	for _, w := range []*integrationWorker{first, second} {
		if err := service.Action(context.Background(), w.id.ID, "approve"); err != nil {
			t.Fatal(err)
		}
	}
	eventually(t, "two workers each own one in-flight attempt", func() bool { return firstFetcher.active.Load() == 1 && secondFetcher.active.Load() == 1 })
	var running, queued int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FILTER(WHERE state='running'),count(*) FILTER(WHERE state='queued') FROM download_fleet_tasks`).Scan(&running, &queued); err != nil {
		t.Fatal(err)
	}
	if running != 2 || queued != 1 {
		t.Fatalf("global capacity exceeded: running=%d queued=%d", running, queued)
	}
	close(gate)
	for _, result := range results {
		select {
		case err := <-result:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(8 * time.Second):
			t.Fatal("capacity task stalled")
		}
	}
	eventually(t, "both worker spools drained", func() bool { return len(first.spool.Records()) == 0 && len(second.spool.Records()) == 0 })
	if firstFetcher.peak.Load() != 1 || secondFetcher.peak.Load() != 1 || archived.Load() != 3 {
		t.Fatalf("per-worker bound violated: peaks=%d,%d archived=%d", firstFetcher.peak.Load(), secondFetcher.peak.Load(), archived.Load())
	}
}

func TestPostgresOutboundTwoWorkersFailureRescheduled(t *testing.T) {
	service, pool := integrationMaster(t)
	var archived atomic.Int32
	service.SetArchive(func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error { archived.Add(1); return nil })
	firstReported := make(chan struct{}, 1)
	handler := service.WorkerHandler()
	master := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler.ServeHTTP(w, r)
		if r.URL.Path == wp.ReportPath {
			select {
			case firstReported <- struct{}{}:
			default:
			}
		}
	}))
	t.Cleanup(master.Close)
	firstFetcher := &integrationFetcher{fail: true}
	first := launchWorker(t, service, master.URL, firstFetcher)
	secondFetcher := &integrationFetcher{}
	second := launchWorker(t, service, master.URL, secondFetcher)
	if first.id.ID == second.id.ID || first.id.Secret == second.id.Secret {
		t.Fatal("workers share identity or credential")
	}
	result := queueRemote(t, service, pool, "10.1234/failover")
	if err := service.Action(context.Background(), first.id.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-firstReported:
	case <-time.After(8 * time.Second):
		t.Fatal("first worker failed to report failure")
	}
	// Drain prevents further claims while preserving terminal report receipts.
	if err := service.Action(context.Background(), first.id.ID, "drain"); err != nil {
		t.Fatal(err)
	}
	if err := service.Action(context.Background(), second.id.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("second worker did not recover failed task")
	}
	eventually(t, "both workers clean terminal spool records", func() bool { return len(first.spool.Records()) == 0 && len(second.spool.Records()) == 0 })
	var workers, attempts int
	if err := pool.QueryRow(context.Background(), `SELECT count(DISTINCT worker_id),count(*) FROM download_fleet_attempts`).Scan(&workers, &attempts); err != nil {
		t.Fatal(err)
	}
	if workers != 2 || attempts != 2 || firstFetcher.calls.Load() != 1 || secondFetcher.calls.Load() != 1 || archived.Load() != 1 {
		t.Fatalf("unexpected failover: workers=%d attempts=%d first=%d second=%d archives=%d", workers, attempts, firstFetcher.calls.Load(), secondFetcher.calls.Load(), archived.Load())
	}
}
