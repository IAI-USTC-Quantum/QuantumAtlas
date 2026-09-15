package mineru

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestTokenStoreCRUD exercises the mineru_tokens table against a live
// PostgreSQL, gated on QATLAS_TEST_PG_DSN (same skip convention as
// internal/registry integration tests — the default `go test` stays
// hermetic). The table is created by the registry migrations; the
// test uses a savepoint-free TRUNCATE to stay idempotent.
func TestTokenStoreCRUD(t *testing.T) {
	dsn := os.Getenv("QATLAS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QATLAS_TEST_PG_DSN unset; skipping live-PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pool.Close()

	store := NewTokenStore(pool)
	if !store.Configured() {
		t.Fatal("Configured = false with a live pool")
	}
	if _, err := pool.Exec(ctx, `TRUNCATE mineru_tokens`); err != nil {
		t.Fatalf("truncate (migration not applied?): %v", err)
	}

	// Seed from config: both tokens land, duplicates collapse.
	if err := store.SyncFromConfig(ctx, []string{"tok-alpha", "tok-beta", "tok-beta", ""}); err != nil {
		t.Fatalf("SyncFromConfig: %v", err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2; rows=%v", len(rows), rows)
	}

	// Re-sync must NOT clobber admin-managed rotated_at.
	manual := time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC)
	if err := store.Upsert(ctx, "tok-alpha", manual); err != nil {
		t.Fatalf("Upsert: %v", err)
	}
	if err := store.SyncFromConfig(ctx, []string{"tok-alpha", "tok-beta"}); err != nil {
		t.Fatalf("re-SyncFromConfig: %v", err)
	}
	rows, err = store.List(ctx)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	var alpha TokenRow
	for _, r := range rows {
		if r.Token == "tok-alpha" {
			alpha = r
		}
	}
	if alpha.Token == "" || !alpha.RotatedAt.Equal(manual) {
		t.Errorf("tok-alpha = %+v, want rotated_at %v to survive a boot sync", alpha, manual)
	}

	// Upsert of a new token = insert; delete then re-list.
	stamp2 := manual.Add(time.Hour)
	if err := store.Upsert(ctx, "tok-gamma", stamp2); err != nil {
		t.Fatalf("Upsert gamma: %v", err)
	}
	if err := store.Delete(ctx, "tok-beta"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	rows, err = store.List(ctx)
	if err != nil {
		t.Fatalf("List after delete: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) after delete = %d, want 2", len(rows))
	}

	// Deleting an unknown token reports ErrTokenNotFound.
	if err := store.Delete(ctx, "tok-beta"); !errors.Is(err, ErrTokenNotFound) {
		t.Errorf("re-Delete err = %v, want ErrTokenNotFound", err)
	}

	// BootSync combines both steps for main.go.
	entries, err := store.BootSync(ctx, []string{"tok-delta"})
	if err != nil {
		t.Fatalf("BootSync: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("BootSync entries = %d, want 3; entries=%v", len(entries), entries)
	}

	// Nil store degrades everywhere instead of panicking.
	var nilStore *TokenStore
	if nilStore.Configured() {
		t.Error("nil store Configured = true")
	}
	if err := nilStore.SyncFromConfig(ctx, nil); err == nil {
		t.Error("nil SyncFromConfig err = nil")
	}
	if _, err := nilStore.List(ctx); err == nil {
		t.Error("nil List err = nil")
	}
}
