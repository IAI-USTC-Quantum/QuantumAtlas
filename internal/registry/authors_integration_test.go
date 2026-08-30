package registry

import "testing"

func TestIntegrationResolveOrMintBackfillsEmptyAuthors(t *testing.T) {
	s, ctx := migrateStore(t)
	id, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93991", Title: "ZZAuthorsBackfill"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	defer cleanupPapers(t, s, id)

	id2, created, err := s.ResolveOrMint(ctx, PaperRef{
		ArxivID: "2401.93991",
		Title:   "ZZAuthorsBackfill",
		Authors: []string{"Alice Example", "Bob Example"},
		Year:    2026,
	})
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if created || id2 != id {
		t.Fatalf("enrich resolved (%q,%v), want (%q,false)", id2, created, id)
	}
	p, found, err := s.Get(ctx, id)
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if len(p.Authors) != 2 || p.Authors[0] != "Alice Example" || p.Authors[1] != "Bob Example" {
		t.Fatalf("authors = %v", p.Authors)
	}

	// A later sparse hit must never erase the enriched byline.
	if _, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93991"}); err != nil {
		t.Fatalf("sparse resolve: %v", err)
	}
	p, _, _ = s.Get(ctx, id)
	if len(p.Authors) != 2 {
		t.Fatalf("sparse resolve erased authors: %v", p.Authors)
	}
}
