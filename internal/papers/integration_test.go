package papers

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestIntegrationCatalog exercises the two-table catalog (papers +
// paper_assets), the default-asset trigger, the MinerU lease, and the DOI
// attach-to-arxiv path against a live PostgreSQL. Skipped unless
// QATLAS_TEST_PG_DSN points at a disposable database, so CI and the
// default `go test` stay hermetic.
func TestIntegrationCatalog(t *testing.T) {
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
	s := NewStore(pool)
	if err := s.EnsureSchema(ctx); err != nil {
		t.Fatalf("EnsureSchema: %v", err)
	}

	const arxiv = "2401.90001" // unique to this test
	const doi = "10.9999/qatlas.test.90001"
	defer func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM papers WHERE paper_arxiv_id = $1 OR paper_doi = $2`, arxiv, doi)
	}()

	// Two arXiv versions → default asset is the highest (v2).
	if err := s.UpsertPDF(ctx, arxiv+"v1", "aa", 10); err != nil {
		t.Fatalf("UpsertPDF v1: %v", err)
	}
	if err := s.UpsertPDF(ctx, arxiv+"v2", "bb", 20); err != nil {
		t.Fatalf("UpsertPDF v2: %v", err)
	}
	var defVer int
	if err := pool.QueryRow(ctx, `
		SELECT a.arxiv_version FROM papers p
		JOIN paper_assets a ON a.asset_id = p.paper_default_asset_id
		WHERE p.paper_arxiv_id = $1`, arxiv).Scan(&defVer); err != nil {
		t.Fatalf("read default asset: %v", err)
	}
	if defVer != 2 {
		t.Errorf("default asset version = %d, want 2 (highest arxiv version)", defVer)
	}

	// NeedsMineru: the v2 (and v1) PDFs have no markdown yet.
	rows, err := s.NeedsMineru(ctx, 10)
	if err != nil {
		t.Fatalf("NeedsMineru: %v", err)
	}
	var sawV2 bool
	for _, r := range rows {
		if r.ArxivID == arxiv+"v2" {
			sawV2 = true
		}
	}
	if !sawV2 {
		t.Errorf("NeedsMineru did not return %sv2 (pdf without markdown)", arxiv)
	}

	// Lease the default asset, then a second lease must conflict.
	lease, err := s.Lease(ctx, CreateOptions{ArxivID: arxiv + "v2", Requester: "alice", TTLSeconds: 300})
	if err != nil {
		t.Fatalf("Lease: %v", err)
	}
	if _, err := s.Lease(ctx, CreateOptions{ArxivID: arxiv + "v2", Requester: "bob"}); err == nil {
		t.Error("second Lease should conflict while the first is active")
	} else if _, ok := err.(*ErrAlreadyLeased); !ok {
		t.Errorf("second Lease err = %T, want *ErrAlreadyLeased", err)
	}
	// Release with the right id clears it.
	if ok, err := s.ReleaseLease(ctx, arxiv+"v2", lease.LeaseID); err != nil || !ok {
		t.Fatalf("ReleaseLease = (%v, %v), want (true, nil)", ok, err)
	}

	// UpsertMD on v2 records markdown + clears any lease; NeedsMineru no
	// longer returns v2.
	if err := s.UpsertMD(ctx, arxiv+"v2", "cc", 30); err != nil {
		t.Fatalf("UpsertMD v2: %v", err)
	}
	var mdPath *string
	if err := pool.QueryRow(ctx, `
		SELECT a.mineru_md_path FROM papers p
		JOIN paper_assets a ON a.paper_id = p.paper_id
		WHERE p.paper_arxiv_id = $1 AND a.arxiv_version = 2`, arxiv).Scan(&mdPath); err != nil {
		t.Fatalf("read md path: %v", err)
	}
	if mdPath == nil {
		t.Error("UpsertMD did not set mineru_md_path on the v2 asset")
	}

	// Stats reflect the arxiv paper.
	st, err := s.QueryStats(ctx)
	if err != nil {
		t.Fatalf("QueryStats: %v", err)
	}
	if st.HasPDF < 1 || st.HasMD < 1 {
		t.Errorf("QueryStats HasPDF=%d HasMD=%d, want >=1 each", st.HasPDF, st.HasMD)
	}

	// DOI upload whose OpenAlex verification links to this arxiv id must
	// ATTACH to the same paper (one work, arxiv + published assets).
	if err := s.UpsertPDFByDOI(ctx, doi, "dd", 40, DOIVerification{
		Status: VerifyVerified, Title: "Test Work", ArxivID: arxiv + "v2",
	}); err != nil {
		t.Fatalf("UpsertPDFByDOI: %v", err)
	}
	var (
		nArxiv, nPublished int
		gotDOI, gotTitle   *string
	)
	if err := pool.QueryRow(ctx, `
		SELECT
			count(*) FILTER (WHERE a.source='arxiv'),
			count(*) FILTER (WHERE a.source='published'),
			max(p.paper_doi), max(p.paper_title)
		FROM papers p JOIN paper_assets a ON a.paper_id = p.paper_id
		WHERE p.paper_arxiv_id = $1
		GROUP BY p.paper_id`, arxiv).Scan(&nArxiv, &nPublished, &gotDOI, &gotTitle); err != nil {
		t.Fatalf("read attached paper: %v", err)
	}
	if nArxiv < 1 || nPublished != 1 {
		t.Errorf("attached paper assets: arxiv=%d published=%d, want arxiv>=1 published=1", nArxiv, nPublished)
	}
	if gotDOI == nil || *gotDOI != doi {
		t.Errorf("attached paper doi = %v, want %s", gotDOI, doi)
	}
	if gotTitle == nil || *gotTitle != "Test Work" {
		t.Errorf("attached paper title = %v, want 'Test Work'", gotTitle)
	}

	// Lookups.
	if _, hit, err := s.LookupDOI(ctx, doi); err != nil || !hit {
		t.Errorf("LookupDOI = (hit=%v, err=%v), want hit", hit, err)
	}
	if twin, hit, err := s.LookupArxivToDOI(ctx, arxiv); err != nil || !hit || twin != doi {
		t.Errorf("LookupArxivToDOI = (%q, %v, %v), want (%s, true, nil)", twin, hit, err, doi)
	}
	for _, tc := range []struct {
		scheme, id string
	}{{"arxiv", arxiv}, {"doi", doi}} {
		if hosted, err := s.IsHosted(ctx, tc.scheme, tc.id); err != nil || !hosted {
			t.Errorf("IsHosted(%s,%s) = (%v,%v), want hosted", tc.scheme, tc.id, hosted, err)
		}
	}

	// GCExpiredLeases is a no-op here (no expired leases) but must not error.
	if _, err := s.GCExpiredLeases(ctx); err != nil {
		t.Errorf("GCExpiredLeases: %v", err)
	}
}
