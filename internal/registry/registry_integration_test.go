package registry

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// testPool connects to the disposable PostgreSQL pointed at by
// QATLAS_TEST_PG_DSN, skipping when unset so the default `go test` stays
// hermetic (same contract as internal/papers).
func testPool(t *testing.T) (*pgxpool.Pool, context.Context) {
	t.Helper()
	dsn := os.Getenv("QATLAS_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("QATLAS_TEST_PG_DSN unset; skipping live-PostgreSQL integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, ctx
}

// TestIntegrationMigrate covers goose migration on an (empty or reused)
// database: all bundled migrations apply, SchemaVersion reports the
// latest, and a second Migrate is a no-op.
func TestIntegrationMigrate(t *testing.T) {
	pool, ctx := testPool(t)
	s := NewStore(pool)

	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	v, err := SchemaVersion(ctx, pool)
	if err != nil {
		t.Fatalf("SchemaVersion: %v", err)
	}
	const wantLatest = 2 // 00001_init + 00002_usage
	if v != wantLatest {
		t.Errorf("SchemaVersion = %d, want %d", v, wantLatest)
	}
	// Tables exist.
	for _, table := range []string{"papers", "paper_assets", "paper_identities", "plans", "user_quotas", "usage_daily"} {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, table).Scan(&n); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s missing after Migrate", table)
		}
	}
	// Second run is a no-op.
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
	if v, _ := SchemaVersion(ctx, pool); v != wantLatest {
		t.Errorf("SchemaVersion after second Migrate = %d, want %d", v, wantLatest)
	}
	_ = s
}

// TestIntegrationResolveOrMint covers mint, dedup-by-arxiv, and the
// title-only rejection.
func TestIntegrationResolveOrMint(t *testing.T) {
	pool, ctx := testPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s := NewStore(pool)

	const arxiv = "2401.91001"
	const doi = "10.9999/qatlas.test.91001"
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM papers WHERE arxiv_id = $1 OR doi = $2`, arxiv, doi)
	}()

	// (b) Mint: qa_-prefixed id, created=true, all identity rows present.
	id, created, err := s.ResolveOrMint(ctx, PaperRef{
		ArxivID: "arXiv:" + arxiv + "v2",
		DOI:     "https://doi.org/" + doi,
		Title:   "Integration Test Paper",
		Authors: []string{"Jane Doe"},
		Year:    2024,
	})
	if err != nil {
		t.Fatalf("ResolveOrMint: %v", err)
	}
	if !created {
		t.Error("first ResolveOrMint created=false, want true")
	}
	if !strings.HasPrefix(id, "qa_") || len(id) != 3+26 {
		t.Errorf("minted id %q: want qa_ + 26 chars", id)
	}
	for _, key := range []string{
		DOIKey(doi),
		ArxivKey(arxiv),
		ArxivVersionKey(arxiv + "v2"),
		TitleKey(TitleHash("Integration Test Paper", []string{"Jane Doe"}, 2024)),
	} {
		got, found, err := s.LookupByIdentity(ctx, key)
		if err != nil || !found || got != id {
			t.Errorf("LookupByIdentity(%q) = (%q, %v, %v), want (%s, true, nil)", key, got, found, err, id)
		}
	}

	// (c) Same arxiv (different version, different formatting) → same id,
	// created=false, and the new arxiv_version key is backfilled.
	id2, created, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: arxiv + "v3"})
	if err != nil {
		t.Fatalf("second ResolveOrMint: %v", err)
	}
	if created || id2 != id {
		t.Errorf("second ResolveOrMint = (%q, %v), want (%q, false)", id2, created, id)
	}
	if _, found, _ := s.LookupByIdentity(ctx, ArxivVersionKey(arxiv+"v3")); !found {
		t.Error("arxiv_version:2401.91001v3 was not backfilled on the existing paper")
	}

	// DOI lookup also resolves to the same paper.
	if got, found, _ := s.LookupByIdentity(ctx, DOIKey(doi)); !found || got != id {
		t.Errorf("doi identity = (%q, %v), want (%s, true)", got, found, id)
	}

	// (e) Title-only ref: ErrTitleOnlyRef, nothing inserted.
	before, err := s.QueryStats(ctx)
	if err != nil {
		t.Fatalf("QueryStats: %v", err)
	}
	_, _, err = s.ResolveOrMint(ctx, PaperRef{Title: "Untraceable Work", Authors: []string{"Anon"}, Year: 1999})
	if !errors.Is(err, ErrTitleOnlyRef) {
		t.Errorf("title-only ResolveOrMint err = %v, want ErrTitleOnlyRef", err)
	}
	after, _ := s.QueryStats(ctx)
	if after.Total != before.Total {
		t.Errorf("title-only ref changed Total: %d -> %d", before.Total, after.Total)
	}
}

// TestIntegrationCrossIdentityMerge: mint an arxiv-only paper A, then a
// doi-only paper B; a reference carrying both identities must merge B
// into the older A and re-point B's identities.
func TestIntegrationCrossIdentityMerge(t *testing.T) {
	pool, ctx := testPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s := NewStore(pool)

	const arxiv = "2401.91002"
	const doi = "10.9999/qatlas.test.91002"
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM papers WHERE arxiv_id = $1 OR doi = $2`, arxiv, doi)
	}()

	idA, created, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: arxiv})
	if err != nil || !created {
		t.Fatalf("mint A = (%q, %v, %v)", idA, created, err)
	}
	// Guarantee distinct created_at ordering regardless of clock resolution.
	time.Sleep(20 * time.Millisecond)
	idB, created, err := s.ResolveOrMint(ctx, PaperRef{DOI: doi})
	if err != nil || !created {
		t.Fatalf("mint B = (%q, %v, %v)", idB, created, err)
	}
	if idA == idB {
		t.Fatal("arxiv-only and doi-only refs minted the same paper")
	}

	// Present both identities: A is older, so it survives; B merges in.
	id, created, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: arxiv, DOI: doi})
	if err != nil {
		t.Fatalf("merge ResolveOrMint: %v", err)
	}
	if created || id != idA {
		t.Errorf("merge = (%q, %v), want survivor A (%q, false)", id, created, idA)
	}

	// B is marked merged_into:A.
	b, found, err := s.Get(ctx, idB)
	if err != nil || !found {
		t.Fatalf("Get B: found=%v err=%v", found, err)
	}
	if b.Status != "merged_into:"+idA {
		t.Errorf("B status = %q, want merged_into:%s", b.Status, idA)
	}

	// B's identities now point at A.
	if got, found, _ := s.LookupByIdentity(ctx, DOIKey(doi)); !found || got != idA {
		t.Errorf("doi identity after merge = (%q, %v), want (%s, true)", got, found, idA)
	}
	if got, found, _ := s.LookupByIdentity(ctx, ArxivKey(arxiv)); !found || got != idA {
		t.Errorf("arxiv identity after merge = (%q, %v), want (%s, true)", got, found, idA)
	}
}
