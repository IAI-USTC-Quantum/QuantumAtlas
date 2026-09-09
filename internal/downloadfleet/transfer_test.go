package downloadfleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
)

type brokenReader struct{}

func (brokenReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
func TestPostgresInterruptedUploadRetriesSameAttempt(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "restart"}[restart], func(t *testing.T) {
			s := postgresService(t)
			ctx := context.Background()
			n, _ := approvedNode(t, s)
			seedTask(t, s, "retryupload")
			a := claimOne(t, s, n)
			data := testPDF()
			sum := sha256.Sum256(data)
			sha := hex.EncodeToString(sum[:])
			size := int64(len(data))
			s.SetArchive(func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error { return nil })
			if _, e := s.upload(ctx, n, a.AttemptID, sha, size, "", "", wp.ResultMetadata{}, io.MultiReader(bytes.NewReader(data[:100]), brokenReader{})); !errors.Is(e, io.ErrUnexpectedEOF) {
				t.Fatal(e)
			}
			var path string
			var originalExpiry time.Time
			if e := s.pool.QueryRow(ctx, `SELECT spool_path,lease_expires FROM download_fleet_attempts WHERE id=$1`, a.AttemptID).Scan(&path, &originalExpiry); e != nil || path != "" {
				t.Fatal("failed upload retained reservation", path, e)
			}
			if restart {
				// Model process death after persisting a reservation and partial file, before
				// the deferred cleanup ran. A new service must replace that generation.
				path = filepath.Join(s.cfg.SpoolDir, a.AttemptID+"-abandoned.pdf")
				if e := os.WriteFile(path, data[:100], 0600); e != nil {
					t.Fatal(e)
				}
				if _, e := s.pool.Exec(ctx, `UPDATE download_fleet_attempts SET spool_path=$2,upload_id='abandoned' WHERE id=$1`, a.AttemptID, path); e != nil {
					t.Fatal(e)
				}
				recovered, e := New(s.pool, s.cfg)
				if e != nil {
					t.Fatal(e)
				}
				recovered.SetArchive(func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error { return nil })
				s = recovered
			}
			r, e := s.upload(ctx, n, a.AttemptID, sha, size, "", "", wp.ResultMetadata{}, bytes.NewReader(data))
			if e != nil || r.State != "done" || r.SHA256 != sha || r.Size != size {
				t.Fatal(r, e)
			}
			var expiry time.Time
			if e = s.pool.QueryRow(ctx, `SELECT lease_expires FROM download_fleet_attempts WHERE id=$1`, a.AttemptID).Scan(&expiry); e != nil || !expiry.Equal(originalExpiry) {
				t.Fatal("retry extended upload deadline", expiry, originalExpiry, e)
			}
			entries, e := os.ReadDir(s.cfg.SpoolDir)
			if e != nil || len(entries) != 0 {
				t.Fatal(entries, e)
			}
		})
	}
}
func TestPostgresSlowUploadHeartbeatAndIndependentBudget(t *testing.T) {
	s := postgresService(t)
	s.cfg.MaxWorkerInFlight = 2
	s.cfg.LeaseDuration = 150 * time.Millisecond
	s.cfg.WorkerTimeout = 400 * time.Millisecond
	s.cfg.UploadTimeout = 2 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	n, _ := approvedNode(t, s)
	seedTask(t, s, "slow1")
	seedTask(t, s, "slow2")
	claims, e := s.claim(ctx, n, wp.ClaimRequest{Limit: 2})
	if e != nil || len(claims.Attempts) != 2 {
		t.Fatal(claims, e)
	}
	a, b := claims.Attempts[0], claims.Attempts[1]
	s.SetArchive(func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error { return nil })
	data := testPDF()
	sum := sha256.Sum256(data)
	body := &gatedReader{started: make(chan struct{}), release: make(chan struct{}), body: bytes.NewReader(data)}
	done := make(chan error, 1)
	go func() {
		r, e := s.upload(ctx, n, a.AttemptID, hex.EncodeToString(sum[:]), int64(len(data)), "", "", wp.ResultMetadata{}, body)
		if e == nil && r.State != "done" {
			e = errors.New("upload not archived")
		}
		done <- e
	}()
	select {
	case <-body.started:
	case e := <-done:
		t.Fatal(e)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	heartbeatCtx, hcancel := context.WithTimeout(ctx, 100*time.Millisecond)
	hb, e := s.heartbeat(heartbeatCtx, n, wp.HeartbeatRequest{Capacity: 2, RunningAttemptIDs: []string{a.AttemptID, b.AttemptID}})
	hcancel()
	if e != nil || len(hb.LeaseExpires) != 2 {
		close(body.release)
		t.Fatal("upload blocked heartbeat or fenced IDs", hb, e)
	}
	// The execution deadline can pass during a valid independently bounded upload.
	select {
	case <-time.After(450 * time.Millisecond):
	case <-ctx.Done():
		close(body.release)
		t.Fatal(ctx.Err())
	}
	hb, e = s.heartbeat(ctx, n, wp.HeartbeatRequest{Capacity: 2, RunningAttemptIDs: []string{a.AttemptID}})
	if e != nil || len(hb.LeaseExpires) != 1 {
		close(body.release)
		t.Fatal("upload lease lost at execution deadline", hb, e)
	}
	if e = s.Maintain(ctx); e != nil {
		close(body.release)
		t.Fatal(e)
	}
	close(body.release)
	if e = <-done; e != nil {
		t.Fatal(e)
	}
}
