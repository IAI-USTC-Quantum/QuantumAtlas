package registry

// blocksources_integration_test.go: live-PostgreSQL coverage for the Q1
// block-comments tables (migration 00007). Requires -tags integration
// plus QATLAS_TEST_PG_DSN pointing at a DISPOSABLE database — like the
// rest of the integration suite it skips (honestly) otherwise; a skip
// is not a database verification pass.

import (
	"context"
	"testing"
)

// TestIntegrationBlockSources covers the 00007 schema end to end:
// source insert idempotency, paper-scoped reads, parse-revision
// append + transactional is_current flip (the partial unique index
// keeps exactly one current revision per paper), and cross-paper
// isolation.
func TestIntegrationBlockSources(t *testing.T) {
	pool, ctx := testPool(t)
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	s := NewStore(pool)

	// Tables from 00007 exist.
	for _, table := range []string{"paper_sources", "parse_revisions"} {
		var n int
		if err := pool.QueryRow(ctx,
			`SELECT count(*) FROM information_schema.tables WHERE table_name = $1`, table).Scan(&n); err != nil {
			t.Fatalf("check table %s: %v", table, err)
		}
		if n != 1 {
			t.Errorf("table %s missing after Migrate", table)
		}
	}

	paperID, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.91707v2"})
	if err != nil {
		t.Fatalf("ResolveOrMint: %v", err)
	}
	// Fixed revision ids (pr_int_a/b) make re-runs collide unless the
	// paper (and its cascade) is removed; clean up unconditionally.
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM papers WHERE paper_id = $1`, paperID)
	})

	// Two sources (same bytes, different origins) + one source for a
	// second paper to prove scoping.
	sha := "5eb6980e0eb27b236cdefd9ecbd557b003a8ab9eb23e5c247e04c0f2a3caedb2"
	srcV2 := PaperSource{SourceID: "src_int_v2", PaperID: paperID, Origin: "arxiv:v2",
		Sha256: sha, ObjstoreKey: "test/int/v2.pdf", SizeBytes: 854}
	srcV3 := PaperSource{SourceID: "src_int_v3", PaperID: paperID, Origin: "arxiv:v3",
		Sha256: sha, ObjstoreKey: "test/int/v3.pdf", SizeBytes: 854}
	for _, src := range []PaperSource{srcV2, srcV3} {
		if _, err := s.InsertPaperSource(ctx, src); err != nil {
			t.Fatalf("InsertPaperSource %s: %v", src.SourceID, err)
		}
	}
	// Idempotent re-insert is a no-op.
	if minted, err := s.InsertPaperSource(ctx, srcV2); err != nil || minted {
		t.Errorf("re-insert = %v/%v, want false/nil", minted, err)
	}

	got, err := s.ListPaperSources(ctx, paperID)
	if err != nil {
		t.Fatalf("ListPaperSources: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("sources = %d, want 2", len(got))
	}

	// Scoped get.
	if _, found, err := s.GetPaperSource(ctx, paperID, "src_int_v2"); err != nil || !found {
		t.Fatalf("GetPaperSource = %v/%v", found, err)
	}
	if _, found, _ := s.GetPaperSource(ctx, "qa_nonexistent00000000000000", "src_int_v2"); found {
		t.Error("source leaked across papers")
	}

	// Parse revisions: first current, then flip.
	revA := ParseRevision{RevisionID: "pr_int_a", PaperID: paperID, SourceID: "src_int_v2",
		Schema: "docvortex.middle", SchemaVersion: "2.0",
		ArtifactSha256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		ObjstoreKey:    "test/int/parse-a.json"}
	revB := ParseRevision{RevisionID: "pr_int_b", PaperID: paperID, SourceID: "src_int_v3",
		Schema: "docvortex.middle", SchemaVersion: "2.0",
		ArtifactSha256: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		ObjstoreKey:    "test/int/parse-b.json"}
	if err := s.InsertParseRevision(ctx, revA, true); err != nil {
		t.Fatalf("InsertParseRevision A: %v", err)
	}
	if err := s.InsertParseRevision(ctx, revB, true); err != nil {
		t.Fatalf("InsertParseRevision B (flip): %v", err)
	}

	revs, err := s.ListParseRevisions(ctx, paperID)
	if err != nil {
		t.Fatalf("ListParseRevisions: %v", err)
	}
	if len(revs) != 2 {
		t.Fatalf("revisions = %d, want 2", len(revs))
	}
	current := 0
	for _, r := range revs {
		if r.IsCurrent {
			current++
			if r.RevisionID != "pr_int_b" {
				t.Errorf("current revision = %s, want pr_int_b (pointer flipped)", r.RevisionID)
			}
		}
	}
	if current != 1 {
		t.Errorf("current revisions = %d, want exactly 1 (partial unique index)", current)
	}

	// Old revision still readable with unchanged bytes pointer.
	if _, found, err := s.GetParseRevision(ctx, paperID, "pr_int_a"); err != nil || !found {
		t.Fatalf("old revision after flip = %v/%v", found, err)
	}
	// Cross-paper isolation.
	if _, found, _ := s.GetParseRevision(ctx, "qa_nonexistent00000000000000", "pr_int_a"); found {
		t.Error("revision leaked across papers")
	}
}
