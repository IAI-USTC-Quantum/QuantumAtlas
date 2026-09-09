package downloadfleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Opt-in only. Uses a uniquely named temporary schema, never public tables or
// goose migration state. Run against a disposable PostgreSQL test database.
func postgresService(t *testing.T) *Service {
	t.Helper()
	url := os.Getenv("TEST_DOWNLOADFLEET_DATABASE_URL")
	if url == "" {
		t.Skip("set TEST_DOWNLOADFLEET_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx := context.Background()
	admin, e := pgxpool.New(ctx, url)
	if e != nil {
		t.Fatal(e)
	}
	schema := "fleet_test_" + randomID()
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, e = admin.Exec(ctx, "CREATE SCHEMA "+quoted); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() {
		_, e := admin.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE")
		if e != nil {
			t.Error(e)
		}
		admin.Close()
	})
	cfg, e := pgxpool.ParseConfig(url)
	if e != nil {
		t.Fatal(e)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	cfg.MaxConns = 20
	pool, e := pgxpool.NewWithConfig(ctx, cfg)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	migration, e := os.ReadFile("../registry/migrations/00004_downloadfleet.sql")
	if e != nil {
		t.Fatal(e)
	}
	up := strings.Split(string(migration), "-- +goose Down")[0]
	if _, e = pool.Exec(ctx, up); e != nil {
		t.Fatal(e)
	}
	s, e := New(pool, Config{Enabled: true, SpoolDir: t.TempDir(), MaxInFlight: 2, MaxWorkerInFlight: 1})
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func approvedNode(t *testing.T, s *Service) (wp.Node, string) {
	t.Helper()
	ctx := context.Background()
	en, e := s.Enrollment(ctx)
	if e != nil {
		t.Fatal(e)
	}
	secret := randomID()
	n, e := s.register(ctx, wp.RegisterRequest{ID: randomID(), Name: "test", EnrollmentToken: en.Token, Secret: secret})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Action(ctx, n.ID, "approve"); e != nil {
		t.Fatal(e)
	}
	n.Status = "approved"
	if _, e = s.heartbeat(ctx, n, wp.HeartbeatRequest{Capacity: 100, BrowserOK: true, DiskFreeBytes: 1 << 30}); e != nil {
		t.Fatal(e)
	}
	return n, secret
}
func seedTask(t *testing.T, s *Service, suffix string) string {
	t.Helper()
	id := randomID()
	ref, _ := json.Marshal(registry.PaperRef{DOI: "10.1234/" + suffix})
	_, e := s.pool.Exec(context.Background(), `INSERT INTO download_fleet_tasks(id,identity,ref,deadline) VALUES($1,$2,$3,clock_timestamp()+interval '15 minutes')`, id, "doi:10.1234/"+suffix, ref)
	if e != nil {
		t.Fatal(e)
	}
	return id
}
func claimOne(t *testing.T, s *Service, n wp.Node) wp.Assignment {
	t.Helper()
	r, e := s.claim(context.Background(), n, wp.ClaimRequest{Limit: 20})
	if e != nil || len(r.Attempts) != 1 {
		t.Fatalf("claim=%+v error=%v", r, e)
	}
	return r.Attempts[0]
}
func TestPostgresEnrollmentApprovalRevocation(t *testing.T) {
	s := postgresService(t)
	ctx := context.Background()
	en, e := s.Enrollment(ctx)
	if e != nil {
		t.Fatal(e)
	}
	req := wp.RegisterRequest{ID: randomID(), Name: "pending", Secret: randomID(), EnrollmentToken: en.Token}
	n, e := s.register(ctx, req)
	if e != nil || n.Status != "pending" {
		t.Fatal(n, e)
	}
	req.EnrollmentToken = ""
	again, e := s.register(ctx, req)
	if e != nil || again.ID != n.ID {
		t.Fatal(again, e)
	}
	req.ID = randomID()
	req.EnrollmentToken = en.Token
	req.Secret = randomID()
	if _, e = s.register(ctx, req); !errors.Is(e, ErrUnauthorized) {
		t.Fatal("token reused", e)
	}
	if _, e = s.claim(ctx, n, wp.ClaimRequest{}); !errors.Is(e, ErrForbidden) {
		t.Fatal("pending claim", e)
	}
	if _, e = s.heartbeat(ctx, n, wp.HeartbeatRequest{}); !errors.Is(e, ErrForbidden) {
		t.Fatal("pending heartbeat", e)
	}
	if e = s.Action(ctx, n.ID, "approve"); e != nil {
		t.Fatal(e)
	}
	if e = s.Action(ctx, n.ID, "drain"); e != nil {
		t.Fatal(e)
	}
	if r, e := s.claim(ctx, n, wp.ClaimRequest{}); e != nil || len(r.Attempts) != 0 {
		t.Fatal(r, e)
	}
	if e = s.Action(ctx, n.ID, "enable"); e != nil {
		t.Fatal(e)
	}
	if e = s.Action(ctx, n.ID, "revoke"); e != nil {
		t.Fatal(e)
	}
	if e = s.Action(ctx, n.ID, "enable"); e == nil {
		t.Fatal("revoked node re-enabled")
	}
	var stored []byte
	if e = s.pool.QueryRow(ctx, `SELECT secret_hash FROM download_fleet_nodes WHERE id=$1`, n.ID).Scan(&stored); e != nil || len(stored) != 32 {
		t.Fatal(stored, e)
	}
}
func TestPostgresClaimsRetryFenceAndGlobalCap(t *testing.T) {
	s := postgresService(t)
	ctx := context.Background()
	n1, _ := approvedNode(t, s)
	n2, _ := approvedNode(t, s)
	n3, _ := approvedNode(t, s)
	task := seedTask(t, s, "retry")
	a := claimOne(t, s, n1)
	if r, e := s.claim(ctx, n1, wp.ClaimRequest{Limit: 100}); e != nil || len(r.Attempts) != 0 {
		t.Fatal(r, e)
	}
	if _, e := s.report(ctx, n2, wp.ReportRequest{AttemptID: a.AttemptID, Failure: wp.FailureNetwork}); !errors.Is(e, ErrNotFound) {
		t.Fatal("wrong worker report", e)
	}
	if _, e := s.report(ctx, n1, wp.ReportRequest{AttemptID: a.AttemptID, Failure: wp.FailureNetwork, Error: "network unavailable"}); e != nil {
		t.Fatal(e)
	}
	if r, e := s.claim(ctx, n1, wp.ClaimRequest{}); e != nil || len(r.Attempts) != 0 {
		t.Fatal("tried worker reused", r, e)
	}
	b := claimOne(t, s, n2)
	if b.TaskID != task {
		t.Fatal(b)
	}
	if _, e := s.pool.Exec(ctx, `UPDATE download_fleet_attempts SET lease_expires=clock_timestamp()-interval '1 second' WHERE id=$1`, b.AttemptID); e != nil {
		t.Fatal(e)
	}
	hb, e := s.heartbeat(ctx, n2, wp.HeartbeatRequest{Capacity: 1, RunningAttemptIDs: []string{b.AttemptID}})
	if e != nil || len(hb.LeaseExpires) != 0 {
		t.Fatal("expired lease resurrected", hb, e)
	}
	if e = s.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	c := claimOne(t, s, n3)
	if _, e = s.report(ctx, n3, wp.ReportRequest{AttemptID: c.AttemptID, Failure: wp.FailureTimeout}); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = s.pool.QueryRow(ctx, `SELECT state FROM download_fleet_tasks WHERE id=$1`, task).Scan(&state); e != nil || state != "failed" {
		t.Fatal(state, e)
	}
	for i := 0; i < 8; i++ {
		seedTask(t, s, fmt.Sprintf("cap%d", i))
	}
	var wg sync.WaitGroup
	var count atomic.Int32
	errs := make(chan error, 3)
	for _, n := range []wp.Node{n1, n2, n3} {
		wg.Add(1)
		go func(n wp.Node) {
			defer wg.Done()
			r, e := s.claim(ctx, n, wp.ClaimRequest{Limit: 100})
			if e != nil {
				errs <- e
			}
			count.Add(int32(len(r.Attempts)))
		}(n)
	}
	wg.Wait()
	close(errs)
	for e := range errs {
		t.Error(e)
	}
	if count.Load() != 2 {
		t.Fatalf("global cap: %d", count.Load())
	}
}
func TestPostgresStagedRecoveryReceiptsAndHooks(t *testing.T) {
	s := postgresService(t)
	ctx := context.Background()
	n, secret := approvedNode(t, s)
	other, _ := approvedNode(t, s)
	seedTask(t, s, "archive")
	a := claimOne(t, s, n)
	data := testPDF()
	sum := sha256.Sum256(data)
	sha := hex.EncodeToString(sum[:])
	size := int64(len(data))
	var archived, hooks atomic.Int32
	s.SetArchive(func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error {
		return errors.New("storage temporarily down")
	})
	r, e := s.upload(ctx, n, a.AttemptID, sha, size, "https://publisher.invalid/p.pdf", "direct", wp.ResultMetadata{DOI: "10.1234/archive", Trace: []wp.Trace{{Strategy: "direct"}}}, bytes.NewReader(data))
	if e != nil || r.State != "staged" {
		t.Fatal(r, e)
	}
	if _, e = s.receipt(ctx, other.ID, a.AttemptID); !errors.Is(e, ErrNotFound) {
		t.Fatal("cross-worker receipt", e)
	}
	recovered, e := New(s.pool, s.cfg)
	if e != nil {
		t.Fatal(e)
	}
	recovered.SetArchive(func(_ context.Context, ref registry.PaperRef, out *downloader.FetchOutcome) error {
		if ref.DOI != "10.1234/archive" || out.Result.Size != size || out.Result.Sha256 != sha || out.WorkerID != n.ID {
			t.Error("archive lost metadata")
		}
		if _, ok := out.Result.Body.(*os.File); !ok {
			t.Error("archive must remain file-backed")
		}
		got, readErr := io.ReadAll(out.Result.Body)
		if readErr != nil || !bytes.Equal(got, data) {
			t.Error("archive body not rewound after integrity check", readErr)
		}
		archived.Add(1)
		out.Result.Sha256 = "preexisting-asset-sha"
		return nil
	})
	recovered.SetHooks(func(_ context.Context, _ registry.PaperRef, out *downloader.FetchOutcome) error {
		if !out.Archived || out.Result.Body != nil {
			t.Error("hook outcome")
		}
		hooks.Add(1)
		return nil
	})
	if e = recovered.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 2; i++ {
		r, e = recovered.receipt(ctx, n.ID, a.AttemptID)
		if e != nil || r.State != "done" || r.SHA256 != sha || r.Size != size {
			t.Fatal(r, e)
		}
	}
	r, e = recovered.upload(ctx, n, a.AttemptID, sha, size, "", "", wp.ResultMetadata{}, bytes.NewReader(data))
	if e != nil || r.State != "done" {
		t.Fatal(r, e)
	}
	if e = recovered.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	if archived.Load() != 1 || hooks.Load() != 1 {
		t.Fatal(archived.Load(), hooks.Load())
	}
	entries, e := os.ReadDir(s.cfg.SpoolDir)
	if e != nil || len(entries) != 0 {
		t.Fatal(entries, e)
	}
	req := httptest.NewRequest(http.MethodGet, wp.ReceiptPath+a.AttemptID, nil)
	req.Header.Set("Authorization", "Bearer "+secret)
	w := httptest.NewRecorder()
	recovered.WorkerHandler().ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if e = recovered.Action(ctx, n.ID, "revoke"); e != nil {
		t.Fatal(e)
	}
	w = httptest.NewRecorder()
	recovered.WorkerHandler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatal(w.Code)
	}
}
func TestPostgresUploadValidationAndExpiry(t *testing.T) {
	s := postgresService(t)
	ctx := context.Background()
	n, _ := approvedNode(t, s)
	seedTask(t, s, "invalidpdf")
	a := claimOne(t, s, n)
	data := bytes.Repeat([]byte("x"), 12<<10)
	sum := sha256.Sum256(data)
	if _, e := s.upload(ctx, n, a.AttemptID, hex.EncodeToString(sum[:]), int64(len(data)), "", "", wp.ResultMetadata{}, bytes.NewReader(data)); e == nil {
		t.Fatal("HTML/non-PDF accepted")
	}
	if _, e := s.pool.Exec(ctx, `UPDATE download_fleet_attempts SET lease_expires=clock_timestamp()-interval '1 second' WHERE id=$1`, a.AttemptID); e != nil {
		t.Fatal(e)
	}
	if e := s.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	r, e := s.receipt(ctx, n.ID, a.AttemptID)
	if e != nil || r.State != "expired" {
		t.Fatal(r, e)
	}
	// Queue timeout completes even with no eligible workers.
	if _, e = s.pool.Exec(ctx, `UPDATE download_fleet_tasks SET deadline=clock_timestamp()-interval '1 second' WHERE id=$1`, a.TaskID); e != nil {
		t.Fatal(e)
	}
	if e = s.Maintain(ctx); e != nil {
		t.Fatal(e)
	}
	var state string
	if e = s.pool.QueryRow(ctx, `SELECT state FROM download_fleet_tasks WHERE id=$1`, a.TaskID).Scan(&state); e != nil || state != "failed" {
		t.Fatal(state, e)
	}
}
