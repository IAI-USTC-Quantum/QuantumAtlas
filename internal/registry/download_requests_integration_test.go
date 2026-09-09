package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestDownloadAdmissionPostgresRecovery(t *testing.T) {
	dsn := os.Getenv("TEST_DOWNLOADFLEET_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DOWNLOADFLEET_DATABASE_URL to a disposable PostgreSQL database")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var b [8]byte
	if _, err = rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	schema := "admission_test_" + hex.EncodeToString(b[:])
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := admin.Exec(cleanup, "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Error(err)
		}
	}()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	raw, err := migrationsFS.ReadFile("migrations/00005_download_requests.sql")
	if err != nil {
		t.Fatal(err)
	}
	up := strings.Split(string(raw), "-- +goose Down")[0]
	if _, err = pool.Exec(ctx, up); err != nil {
		t.Fatal(err)
	}
	store := NewStore(pool)
	ref := PaperRef{DOI: "10.1000/recover", Title: "accepted before remote delegation"}
	requestID, err := store.SaveDownloadRequest(ctx, "paper-1", "10.1000/recover", "doi", ref)
	if err != nil {
		t.Fatal(err)
	}
	// Duplicate active admission must not replace the request's original identity.
	if duplicateID, err := store.SaveDownloadRequest(ctx, "paper-1", "different", "doi", PaperRef{DOI: "10.1000/other"}); err != nil || duplicateID != requestID {
		t.Fatal(err)
	}
	restarted := NewStore(pool)
	pending, err := restarted.PendingDownloadRequests(ctx, 10)
	if err != nil || len(pending) != 1 || pending[0].Ref.DOI != ref.DOI {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	if err = restarted.FinishDownloadRequest(ctx, "paper-1", requestID, "done", ""); err != nil {
		t.Fatal(err)
	}
	pending, err = restarted.PendingDownloadRequests(ctx, 10)
	if err != nil || len(pending) != 0 {
		t.Fatalf("terminal request recovered: %+v %v", pending, err)
	}
	if requestID, err = restarted.SaveDownloadRequest(ctx, "paper-1", "10.1000/recover", "doi", ref); err != nil {
		t.Fatal(err)
	}
	pending, err = restarted.PendingDownloadRequests(ctx, 10)
	if err != nil || len(pending) != 1 {
		t.Fatalf("explicit retry not admitted: %+v %v", pending, err)
	}
	if err = restarted.FinishDownloadRequest(ctx, "paper-1", requestID, "failed", "no eligible worker"); err != nil {
		t.Fatal(err)
	}
	if _, err = pool.Exec(ctx, `UPDATE downloader_requests SET updated_at=now()-interval '8 days'`); err != nil {
		t.Fatal(err)
	}
	if err = restarted.PruneDownloadRequests(ctx, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = pool.QueryRow(ctx, "SELECT count(*) FROM downloader_requests").Scan(&count); err != nil || count != 0 {
		t.Fatalf("retention count=%d err=%v", count, err)
	}
}
