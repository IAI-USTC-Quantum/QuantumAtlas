package registry

// lookup_integration_test.go: the identity/detail lookups added with the
// external-feedback fixes (GetPaperIDByIdentity / HostedWithMD /
// LatestArxivAssetVersion / PaperSummaries) against a live PostgreSQL
// (QATLAS_TEST_PG_DSN; skipped when unset, same as the other
// integration suites).

import (
	"testing"
)

// TestIntegrationGetPaperIDByIdentity covers the detail-by-identifier
// resolution: every scheme, the version/URL normalization the store
// applies, and the miss/error contract.
func TestIntegrationGetPaperIDByIdentity(t *testing.T) {
	s, ctx := migrateStore(t)

	pid, _, err := s.ResolveOrMint(ctx, PaperRef{
		ArxivID: "2401.93011v2",
		DOI:     "10.22331/ZZGetByIdentity-93011",
		Title:   "ZZGetByIdentity alpha",
	})
	if err != nil {
		t.Fatalf("ResolveOrMint: %v", err)
	}
	defer cleanupPapers(t, s, pid)

	// arxiv: bare, versioned, and old-style forms all resolve (version
	// stripped + arXiv: prefix removed by NormalizeArxivID).
	for _, in := range []string{"2401.93011", "2401.93011v9", "arXiv:2401.93011"} {
		got, found, err := s.GetPaperIDByIdentity(ctx, "arxiv", in)
		if err != nil || !found || got != pid {
			t.Errorf("arxiv %q = (%q, %v, %v), want (%q, true, nil)", in, got, found, err, pid)
		}
	}

	// doi: URL prefix stripped + lower-cased.
	got, found, err := s.GetPaperIDByIdentity(ctx, "doi", "https://doi.org/10.22331/ZZGetByIdentity-93011")
	if err != nil || !found || got != pid {
		t.Errorf("doi URL form = (%q, %v, %v), want (%q, true, nil)", got, found, err, pid)
	}

	// Misses.
	for _, c := range []struct{ scheme, id string }{
		{"arxiv", "2401.99999"},
		{"doi", "10.22331/zzgetbyidentity-does-not-exist"},
		{"openalex", "W9999999999"},
		{"unknown-scheme", "whatever"},
		{"arxiv", ""},
	} {
		got, found, err := s.GetPaperIDByIdentity(ctx, c.scheme, c.id)
		if err != nil || found || got != "" {
			t.Errorf("(%s, %q) = (%q, %v, %v), want clean miss", c.scheme, c.id, got, found, err)
		}
	}

	// Unconfigured store reports ErrCatalogUnavailable (NOT a miss) so
	// callers can 503 instead of lying 404.
	if _, found, err := NewStore(nil).GetPaperIDByIdentity(ctx, "arxiv", "2401.93011"); err != ErrCatalogUnavailable || found {
		t.Errorf("nil store = (found %v, err %v), want (false, ErrCatalogUnavailable)", found, err)
	}
}

// TestIntegrationHostedWithMD covers the lookup endpoint's combined
// hosted + has_md probe across the paper lifecycle.
func TestIntegrationHostedWithMD(t *testing.T) {
	s, ctx := migrateStore(t)

	pdfOnly, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.93012v1", Title: "ZZHostedWithMD alpha"},
		1, "", 100, "2401/2401.93012v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF: %v", err)
	}
	converted, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.93013v1", Title: "ZZHostedWithMD beta"},
		1, "", 100, "2401/2401.93013v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF converted: %v", err)
	}
	if err := s.UpsertMD(ctx, converted, 1, "", 0, "2401/2401.93013v1.md", "", 0); err != nil {
		t.Fatalf("UpsertMD: %v", err)
	}
	defer cleanupPapers(t, s, pdfOnly, converted)

	if hosted, hasMD, err := s.HostedWithMD(ctx, "arxiv", "2401.93012v1"); err != nil || !hosted || hasMD {
		t.Errorf("pdf-only = (hosted %v, hasMD %v, err %v), want (true, false, nil)", hosted, hasMD, err)
	}
	if hosted, hasMD, err := s.HostedWithMD(ctx, "arxiv", "2401.93013"); err != nil || !hosted || !hasMD {
		t.Errorf("converted = (hosted %v, hasMD %v, err %v), want (true, true, nil)", hosted, hasMD, err)
	}
	if hosted, hasMD, err := s.HostedWithMD(ctx, "doi", "10.22331/zzhostedwithmd-does-not-exist"); err != nil || hosted || hasMD {
		t.Errorf("unknown = (hosted %v, hasMD %v, err %v), want (false, false, nil)", hosted, hasMD, err)
	}
	if hosted, _, err := s.HostedWithMD(ctx, "unknown", "x"); err != nil || hosted {
		t.Errorf("unknown scheme err = %v, want nil / not hosted", err)
	}
}

// TestIntegrationLatestArxivAssetVersion covers the catalog-first
// latest-version resolution used by the bare-id asset endpoints.
func TestIntegrationLatestArxivAssetVersion(t *testing.T) {
	s, ctx := migrateStore(t)

	pid, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.93014v1", Title: "ZZLatestVersion alpha"},
		1, "", 100, "2401/2401.93014v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF v1: %v", err)
	}
	if _, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.93014v3"},
		3, "", 100, "2401/2401.93014v3.pdf"); err != nil {
		t.Fatalf("UpsertPDF v3: %v", err)
	}
	defer cleanupPapers(t, s, pid)

	// Versioned input normalizes to bare before matching; highest wins.
	for _, in := range []string{"2401.93014", "2401.93014v1"} {
		v, found, err := s.LatestArxivAssetVersion(ctx, in)
		if err != nil || !found || v != 3 {
			t.Errorf("LatestArxivAssetVersion(%q) = (%d, %v, %v), want (3, true, nil)", in, v, found, err)
		}
	}
	// Unknown paper / DOI-only / empty input → clean miss.
	if _, found, err := s.LatestArxivAssetVersion(ctx, "2401.99999"); err != nil || found {
		t.Errorf("unknown arxiv = (found %v, err %v), want clean miss", found, err)
	}
	if _, found, err := s.LatestArxivAssetVersion(ctx, ""); err != nil || found {
		t.Errorf("empty = (found %v, err %v), want clean miss", found, err)
	}
}

// TestIntegrationPaperSummaries covers the batch hosting projection
// used to decorate search results.
func TestIntegrationPaperSummaries(t *testing.T) {
	s, ctx := migrateStore(t)

	pdfOnly, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.93015v1", Title: "ZZSummaries alpha"},
		1, "", 100, "2401/2401.93015v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF: %v", err)
	}
	converted, _, err := s.UpsertPDF(ctx, PaperRef{ArxivID: "2401.93016v1", Title: "ZZSummaries beta"},
		1, "", 100, "2401/2401.93016v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF converted: %v", err)
	}
	if err := s.UpsertMD(ctx, converted, 1, "", 0, "2401/2401.93016v1.md", "", 2); err != nil {
		t.Fatalf("UpsertMD: %v", err)
	}
	defer cleanupPapers(t, s, pdfOnly, converted)

	sums, err := s.PaperSummaries(ctx, []string{pdfOnly, converted, "qa_summary_unknown"})
	if err != nil {
		t.Fatalf("PaperSummaries: %v", err)
	}
	if got := sums[pdfOnly]; !got.HasPDF || got.HasMD || got.Status != "ready" {
		t.Errorf("summary[pdf-only] = %+v, want has_pdf only / ready", got)
	}
	if got := sums[converted]; !got.HasPDF || !got.HasMD || got.Status != "ready" {
		t.Errorf("summary[converted] = %+v, want has_pdf+has_md / ready", got)
	}
	if _, has := sums["qa_summary_unknown"]; has {
		t.Error("unknown paper_id must be absent from the map")
	}

	// Degenerate inputs.
	if sums, err = s.PaperSummaries(ctx, nil); err != nil || len(sums) != 0 {
		t.Errorf("nil input = (%v, %v), want (empty, nil)", sums, err)
	}
	if _, err = NewStore(nil).PaperSummaries(ctx, []string{pdfOnly}); err != ErrCatalogUnavailable {
		t.Errorf("nil store err = %v, want ErrCatalogUnavailable", err)
	}
}
