package registry

// list_integration_test.go: ListPapers against a live PostgreSQL
// (QATLAS_TEST_PG_DSN; skipped when unset, same as the other
// integration suites).

import (
	"testing"
)

// TestIntegrationListPapers covers filtering (has_md / status / title
// substring), pagination, and the default-asset projections (has_pdf /
// has_md / image_count).
func TestIntegrationListPapers(t *testing.T) {
	s, ctx := migrateStore(t)

	// Three papers scoped by a unique title prefix so the assertions are
	// independent of whatever else lives in the shared test database.
	pdfOnly, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.93001v1", Title: "ZZListTest alpha"}, 1,
		"", 100, "2401/2401.93001v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF pdf-only: %v", err)
	}
	converted, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.93002v1", Title: "ZZListTest beta"}, 1,
		"", 100, "2401/2401.93002v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF converted: %v", err)
	}
	if err := s.UpsertMD(ctx, converted, 1, "", 0,
		"2401/2401.93002v1.md", "2401/2401.93002v1.json", 4); err != nil {
		t.Fatalf("UpsertMD: %v", err)
	}
	pending, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93003", Title: "ZZListTest gamma"})
	if err != nil {
		t.Fatalf("ResolveOrMint: %v", err)
	}
	if ok, err := s.UpdateStatus(ctx, pending, "pending"); err != nil || !ok {
		t.Fatalf("UpdateStatus: ok=%v err=%v", ok, err)
	}
	defer cleanupPapers(t, s, pdfOnly, converted, pending)

	scoped := ListFilter{Query: "zzlisttest", Page: 1, PerPage: 100}

	index := func(items []ListItem) map[string]ListItem {
		out := map[string]ListItem{}
		for _, it := range items {
			out[it.PaperID] = it
		}
		return out
	}

	// Default listing: all three, newest-created first.
	items, total, err := s.ListPapers(ctx, scoped)
	if err != nil {
		t.Fatalf("ListPapers: %v", err)
	}
	if total != 3 || len(items) != 3 {
		t.Fatalf("default list = %d items / total %d, want 3/3", len(items), total)
	}
	if items[0].PaperID != pending {
		t.Errorf("default order: first = %q, want newest %q", items[0].PaperID, pending)
	}
	byID := index(items)
	if it := byID[pdfOnly]; !it.HasPDF || it.HasMD || it.ImageCount != 0 {
		t.Errorf("pdf-only item = %+v, want has_pdf only", it)
	}
	if it := byID[converted]; !it.HasPDF || !it.HasMD || it.ImageCount != 4 {
		t.Errorf("converted item = %+v, want has_pdf+has_md, image_count 4", it)
	}
	if it := byID[pending]; it.HasPDF || it.HasMD {
		t.Errorf("pending item = %+v, want no asset projections", it)
	}

	// has_md filter.
	hasMD := true
	items, total, err = s.ListPapers(ctx, ListFilter{Query: "zzlisttest", HasMD: &hasMD, Page: 1, PerPage: 100})
	if err != nil {
		t.Fatalf("ListPapers has_md=true: %v", err)
	}
	if total != 1 || len(items) != 1 || items[0].PaperID != converted {
		t.Errorf("has_md=true = %v (total %d), want only the converted paper", items, total)
	}
	hasMD = false
	_, total, err = s.ListPapers(ctx, ListFilter{Query: "zzlisttest", HasMD: &hasMD, Page: 1, PerPage: 100})
	if err != nil {
		t.Fatalf("ListPapers has_md=false: %v", err)
	}
	if total != 2 {
		t.Errorf("has_md=false total = %d, want 2 (pdf-only + assetless pending)", total)
	}

	// Status filter.
	items, total, err = s.ListPapers(ctx, ListFilter{Query: "zzlisttest", Status: "pending", Page: 1, PerPage: 100})
	if err != nil {
		t.Fatalf("ListPapers status: %v", err)
	}
	if total != 1 || items[0].Status != "pending" {
		t.Errorf("status=pending = %v (total %d)", items, total)
	}

	// Title substring (case-insensitive).
	_, total, err = s.ListPapers(ctx, ListFilter{Query: "zzlisttest ALPHA", Page: 1, PerPage: 100})
	if err != nil {
		t.Fatalf("ListPapers q: %v", err)
	}
	if total != 1 {
		t.Errorf("q=zzlisttest ALPHA total = %d, want 1", total)
	}

	// Pagination: per_page=2 covers page 1 (2 items) + page 2 (1 item).
	page1, total, err := s.ListPapers(ctx, ListFilter{Query: "zzlisttest", Page: 1, PerPage: 2})
	if err != nil {
		t.Fatalf("ListPapers page1: %v", err)
	}
	page2, _, err := s.ListPapers(ctx, ListFilter{Query: "zzlisttest", Page: 2, PerPage: 2})
	if err != nil {
		t.Fatalf("ListPapers page2: %v", err)
	}
	if total != 3 || len(page1) != 2 || len(page2) != 1 {
		t.Errorf("pagination = page1 %d, page2 %d, total %d; want 2/1/3", len(page1), len(page2), total)
	}

	// sort=updated_at keeps the same set.
	items, total, err = s.ListPapers(ctx, ListFilter{Query: "zzlisttest", Sort: "updated_at", Page: 1, PerPage: 100})
	if err != nil {
		t.Fatalf("ListPapers sort=updated_at: %v", err)
	}
	if total != 3 || len(index(items)) != 3 {
		t.Errorf("sort=updated_at = %d items / total %d, want 3/3", len(items), total)
	}

	// Unconfigured store degrades to ErrCatalogUnavailable.
	if _, _, err := NewStore(nil).ListPapers(ctx, scoped); err != ErrCatalogUnavailable {
		t.Errorf("nil store ListPapers err = %v, want ErrCatalogUnavailable", err)
	}
}
