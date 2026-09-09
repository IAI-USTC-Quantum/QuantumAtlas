package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// This tests the REAL archive callback against all registry migrations and the
// filesystem store, complementing runner/fleet tests with synthetic callbacks.
func TestDownloadArchiveRealRegistryAndAdmission(t *testing.T) {
	dsn := os.Getenv("TEST_DOWNLOADFLEET_DATABASE_URL")
	if dsn == "" {
		t.Skip("disposable PostgreSQL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	var entropy [8]byte
	if _, err = rand.Read(entropy[:]); err != nil {
		t.Fatal(err)
	}
	schema := "archive_test_" + hex.EncodeToString(entropy[:])
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
	if err = registry.Migrate(ctx, pool); err != nil {
		t.Fatal(err)
	}
	reg := registry.NewStore(pool)
	store, err := objstore.NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	d := downloader.New(reg, store, nil, nil, downloader.Config{Journal: reg})
	defer d.Shutdown(context.Background())
	ref := registry.PaperRef{DOI: "10.1000/archive-full"}
	paperID, _, err := reg.ResolveOrMint(ctx, ref)
	if err != nil {
		t.Fatal(err)
	}
	admission, err := reg.SaveDownloadRequest(ctx, paperID, ref.DOI, "doi", ref)
	if err != nil {
		t.Fatal(err)
	}
	ctx = downloader.WithAdmissionID(ctx, admission)
	pdf := []byte("%PDF-1.4\n" + strings.Repeat("% harmless test comment\n", 1000) + "startxref\n0\n%%EOF\n")
	digest := sha256.Sum256(pdf)
	sha := hex.EncodeToString(digest[:])
	outcome := func() *downloader.FetchOutcome {
		return &downloader.FetchOutcome{DOI: ref.DOI, Strategy: "worker:test:browser", URL: "https://publisher.example/paper.pdf", Result: &downloader.FetchResult{Body: bytes.NewReader(pdf), Size: int64(len(pdf)), Sha256: sha}}
	}
	if err = d.ArchiveRemote(ctx, ref, outcome()); err != nil {
		t.Fatal(err)
	}
	// Replaying a lost post-commit response is idempotent, including create-only
	// object-store conflicts on a backend that does not support source metadata.
	if err = d.ArchiveRemote(ctx, ref, outcome()); err != nil {
		t.Fatal(err)
	}
	info, exists, err := store.Stat(ctx, paperassets.DOIAssetKey("pdf", ref.DOI))
	if err != nil || !exists || info.Size != int64(len(pdf)) {
		t.Fatalf("stored info=%+v exists=%v err=%v", info, exists, err)
	}
	detail, found, err := reg.GetWithAssets(ctx, paperID)
	if err != nil || !found || len(detail.Assets) == 0 {
		t.Fatalf("registry detail=%+v found=%v err=%v", detail, found, err)
	}
	var state string
	if err = pool.QueryRow(ctx, "SELECT state FROM downloader_requests WHERE request_id=$1", admission).Scan(&state); err != nil || state != "done" {
		t.Fatalf("admission state=%s err=%v", state, err)
	}
	newer, err := reg.SaveDownloadRequest(ctx, paperID, ref.DOI, "doi", ref)
	if err != nil || newer == admission {
		t.Fatal("explicit retry must mint a new generation")
	}
	if err = reg.FinishDownloadRequest(ctx, paperID, admission, "failed", "late old waiter"); err != nil {
		t.Fatal(err)
	}
	if err = pool.QueryRow(ctx, "SELECT state FROM downloader_requests WHERE request_id=$1", newer).Scan(&state); err != nil || state != "queued" {
		t.Fatalf("late generation overwrote new admission: %s %v", state, err)
	}
}
