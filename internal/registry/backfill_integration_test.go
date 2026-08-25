package registry

// backfill_integration_test.go: ListUntitled keyset pagination and
// UpdateMetadata against a live PostgreSQL (QATLAS_TEST_PG_DSN; skipped
// when unset, same as the other integration suites).

import (
	"testing"
	"time"
)

// TestIntegrationListUntitled covers the untitled filter (titled papers
// and merged tombstones excluded) and the keyset pagination order.
func TestIntegrationListUntitled(t *testing.T) {
	s, ctx := migrateStore(t)

	untitled1, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93101"})
	if err != nil {
		t.Fatalf("mint untitled1: %v", err)
	}
	untitled2, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93102"})
	if err != nil {
		t.Fatalf("mint untitled2: %v", err)
	}
	titled, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93103", Title: "ZZBackfillTest titled"})
	if err != nil {
		t.Fatalf("mint titled: %v", err)
	}
	merged, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93104"})
	if err != nil {
		t.Fatalf("mint merged: %v", err)
	}
	if ok, err := s.UpdateStatus(ctx, merged, "merged_into:"+titled); err != nil || !ok {
		t.Fatalf("UpdateStatus merged: ok=%v err=%v", ok, err)
	}
	defer cleanupPapers(t, s, untitled1, untitled2, titled, merged)

	// Keyset pages from just before the lower untitled id: both untitled
	// papers appear, in paper_id order, and the titled / merged ones do
	// not. (The listing is global, so filter to this test's ids rather
	// than asserting exact page contents.)
	if untitled1 > untitled2 {
		untitled1, untitled2 = untitled2, untitled1
	}
	floor := untitled1[:len(untitled1)-1] + string(untitled1[len(untitled1)-1]-1) // untitled1 - 1 in the last char
	page, err := s.ListUntitled(ctx, floor, 1000)
	if err != nil {
		t.Fatalf("ListUntitled: %v", err)
	}
	var order []string
	arxivOf := map[string]string{}
	for _, p := range page {
		switch p.PaperID {
		case untitled1, untitled2:
			order = append(order, p.PaperID)
			arxivOf[p.PaperID] = p.ArxivID
		case titled, merged:
			t.Errorf("ListUntitled returned %s (titled or merged)", p.PaperID)
		}
	}
	if len(order) != 2 || order[0] != untitled1 || order[1] != untitled2 {
		t.Errorf("keyset order = %v, want [%s %s]", order, untitled1, untitled2)
	}
	if got := arxivOf[untitled1]; got != "2401.93101" && got != "2401.93102" {
		t.Errorf("arxiv_id projection = %q", got)
	}

	// The keyset cursor is strictly paper_id-based: re-querying after
	// untitled2 never re-returns it.
	rest, err := s.ListUntitled(ctx, untitled2, 10)
	if err != nil {
		t.Fatalf("ListUntitled tail: %v", err)
	}
	for _, p := range rest {
		if p.PaperID == untitled1 || p.PaperID == untitled2 || p.PaperID == titled || p.PaperID == merged {
			t.Errorf("tail listing re-returned test paper %s", p.PaperID)
		}
	}

	// Unconfigured store degrades to ErrCatalogUnavailable.
	if _, err := NewStore(nil).ListUntitled(ctx, "", 10); err != ErrCatalogUnavailable {
		t.Errorf("nil store ListUntitled err = %v, want ErrCatalogUnavailable", err)
	}
}

// TestIntegrationUpdateMetadata covers the metadata fill (title set,
// title_hash recomputed, date/abstract stored) and the DOI claim
// semantics: claimed once, never overwritten, never stolen from a second
// paper holding the same DOI.
func TestIntegrationUpdateMetadata(t *testing.T) {
	s, ctx := migrateStore(t)

	pid1, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93111"})
	if err != nil {
		t.Fatalf("mint pid1: %v", err)
	}
	pid2, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93112"})
	if err != nil {
		t.Fatalf("mint pid2: %v", err)
	}
	defer cleanupPapers(t, s, pid1, pid2)

	pub := time.Date(2024, 3, 14, 0, 0, 0, 0, time.UTC)
	md := Metadata{
		Title:           "ZZBackfillTest Metadata",
		Authors:         []string{"Jane Doe", "John Smith"},
		Year:            2024,
		PublicationDate: pub,
		Abstract:        "An abstract.",
		DOI:             "10.5555/zzbackfilltest.001",
	}
	claims, err := s.UpdateMetadata(ctx, pid1, md)
	if err != nil {
		t.Fatalf("UpdateMetadata pid1: %v", err)
	}
	if !claims.DOIClaimed {
		t.Error("pid1 DOIClaimed = false, want true (first claim)")
	}

	got, found, err := s.Get(ctx, pid1)
	if err != nil || !found {
		t.Fatalf("Get pid1: found=%v err=%v", found, err)
	}
	if got.Title != md.Title {
		t.Errorf("title = %q, want %q", got.Title, md.Title)
	}
	if len(got.Authors) != 2 || got.Authors[0] != "Jane Doe" {
		t.Errorf("authors = %v", got.Authors)
	}
	if got.DOI != "10.5555/zzbackfilltest.001" {
		t.Errorf("doi = %q", got.DOI)
	}
	if want := TitleHash(md.Title, md.Authors, md.Year); got.TitleHash != want {
		t.Errorf("title_hash = %q, want recomputed %q", got.TitleHash, want)
	}
	var (
		pubDate time.Time
		abs     string
	)
	if err := s.pool.QueryRow(ctx,
		`SELECT publication_date, abstract FROM papers WHERE paper_id = $1`, pid1).
		Scan(&pubDate, &abs); err != nil {
		t.Fatalf("select date/abstract: %v", err)
	}
	if !pubDate.Equal(pub) {
		t.Errorf("publication_date = %v, want %v", pubDate, pub)
	}
	if abs != "An abstract." {
		t.Errorf("abstract = %q", abs)
	}
	// The doi identity row routes lookups to pid1.
	owner, found, err := s.LookupByIdentity(ctx, DOIKey(md.DOI))
	if err != nil || !found || owner != pid1 {
		t.Errorf("doi identity owner = %q found=%v err=%v, want %q", owner, found, err, pid1)
	}

	// A second paper referencing the same DOI must NOT claim it: the
	// update still lands (title etc.), but doi stays NULL and no
	// identity is re-pointed.
	claims, err = s.UpdateMetadata(ctx, pid2, Metadata{
		Title:   "ZZBackfillTest Other",
		Authors: []string{"Jane Doe"},
		Year:    2024,
		DOI:     "10.5555/zzbackfilltest.001",
	})
	if err != nil {
		t.Fatalf("UpdateMetadata pid2: %v", err)
	}
	if claims.DOIClaimed {
		t.Error("pid2 DOIClaimed = true, want false (DOI owned by pid1)")
	}
	got2, _, err := s.Get(ctx, pid2)
	if err != nil {
		t.Fatalf("Get pid2: %v", err)
	}
	if got2.DOI != "" {
		t.Errorf("pid2 doi = %q, want empty (claim refused)", got2.DOI)
	}
	if got2.Title != "ZZBackfillTest Other" {
		t.Errorf("pid2 title = %q (metadata should still update)", got2.Title)
	}

	// Re-updating pid1 with a different DOI never overwrites the
	// existing one.
	claims, err = s.UpdateMetadata(ctx, pid1, Metadata{DOI: "10.5555/zzbackfilltest.002"})
	if err != nil {
		t.Fatalf("UpdateMetadata pid1 second doi: %v", err)
	}
	if claims.DOIClaimed {
		t.Error("pid1 second DOIClaimed = true, want false (doi already set)")
	}
	got, _, _ = s.Get(ctx, pid1)
	if got.DOI != "10.5555/zzbackfilltest.001" {
		t.Errorf("pid1 doi = %q after second update, want unchanged", got.DOI)
	}

	// Unknown paper_id is a no-op, not an error.
	claims, err = s.UpdateMetadata(ctx, "qa_nonexistent", md)
	if err != nil || claims != (MetadataClaims{}) {
		t.Errorf("missing paper: claims=%+v err=%v, want zero/nil", claims, err)
	}

	// Unconfigured store degrades to ErrCatalogUnavailable.
	if _, err := NewStore(nil).UpdateMetadata(ctx, pid1, md); err != ErrCatalogUnavailable {
		t.Errorf("nil store UpdateMetadata err = %v, want ErrCatalogUnavailable", err)
	}
}

// TestIntegrationListUntitledDOI covers the DOI-keyed untitled listing:
// untitled DOI papers are returned in keyset order (including ones that
// also carry an arxiv_id), titled / merged / DOI-less papers excluded.
func TestIntegrationListUntitledDOI(t *testing.T) {
	s, ctx := migrateStore(t)

	doiOnly, _, err := s.ResolveOrMint(ctx, PaperRef{DOI: "10.5555/zzbackfilltest.list.001"})
	if err != nil {
		t.Fatalf("mint doiOnly: %v", err)
	}
	doiAndArxiv, _, err := s.ResolveOrMint(ctx, PaperRef{DOI: "10.5555/zzbackfilltest.list.002", ArxivID: "2401.93121"})
	if err != nil {
		t.Fatalf("mint doiAndArxiv: %v", err)
	}
	arxivOnly, _, err := s.ResolveOrMint(ctx, PaperRef{ArxivID: "2401.93122"})
	if err != nil {
		t.Fatalf("mint arxivOnly: %v", err)
	}
	titled, _, err := s.ResolveOrMint(ctx, PaperRef{DOI: "10.5555/zzbackfilltest.list.003", Title: "ZZBackfillTest doi titled"})
	if err != nil {
		t.Fatalf("mint titled: %v", err)
	}
	merged, _, err := s.ResolveOrMint(ctx, PaperRef{DOI: "10.5555/zzbackfilltest.list.004"})
	if err != nil {
		t.Fatalf("mint merged: %v", err)
	}
	if ok, err := s.UpdateStatus(ctx, merged, "merged_into:"+titled); err != nil || !ok {
		t.Fatalf("UpdateStatus merged: ok=%v err=%v", ok, err)
	}
	defer cleanupPapers(t, s, doiOnly, doiAndArxiv, arxivOnly, titled, merged)

	if doiOnly > doiAndArxiv {
		doiOnly, doiAndArxiv = doiAndArxiv, doiOnly
	}
	floor := doiOnly[:len(doiOnly)-1] + string(doiOnly[len(doiOnly)-1]-1)
	page, err := s.ListUntitledDOI(ctx, floor, 1000)
	if err != nil {
		t.Fatalf("ListUntitledDOI: %v", err)
	}
	var order []string
	doiOf := map[string]string{}
	for _, p := range page {
		switch p.PaperID {
		case doiOnly, doiAndArxiv:
			order = append(order, p.PaperID)
			doiOf[p.PaperID] = p.DOI
		case arxivOnly, titled, merged:
			t.Errorf("ListUntitledDOI returned %s (doi-less, titled or merged)", p.PaperID)
		}
	}
	if len(order) != 2 || order[0] != doiOnly || order[1] != doiAndArxiv {
		t.Errorf("keyset order = %v, want [%s %s]", order, doiOnly, doiAndArxiv)
	}
	if doiOf[doiOnly] == "" || doiOf[doiAndArxiv] == "" {
		t.Errorf("doi projection empty: %v", doiOf)
	}

	// Unconfigured store degrades to ErrCatalogUnavailable.
	if _, err := NewStore(nil).ListUntitledDOI(ctx, "", 10); err != ErrCatalogUnavailable {
		t.Errorf("nil store ListUntitledDOI err = %v, want ErrCatalogUnavailable", err)
	}
}

// TestIntegrationUpdateMetadataArxivClaim covers the arXiv-id claim in
// UpdateMetadata (the OpenAlex-source path): claimed once with the
// arxiv:<id> identity inserted, a second paper holding the same arXiv id
// is not claimed, and an existing arxiv_id is never overwritten.
func TestIntegrationUpdateMetadataArxivClaim(t *testing.T) {
	s, ctx := migrateStore(t)

	pid1, _, err := s.ResolveOrMint(ctx, PaperRef{DOI: "10.5555/zzbackfilltest.claim.001"})
	if err != nil {
		t.Fatalf("mint pid1: %v", err)
	}
	pid2, _, err := s.ResolveOrMint(ctx, PaperRef{DOI: "10.5555/zzbackfilltest.claim.002"})
	if err != nil {
		t.Fatalf("mint pid2: %v", err)
	}
	defer cleanupPapers(t, s, pid1, pid2)

	claims, err := s.UpdateMetadata(ctx, pid1, Metadata{
		Title:   "ZZBackfillTest Arxiv Claim",
		Year:    2023,
		ArxivID: "2401.93131v2", // version suffix stripped on claim
	})
	if err != nil {
		t.Fatalf("UpdateMetadata pid1: %v", err)
	}
	if !claims.ArxivClaimed || claims.DOIClaimed {
		t.Errorf("pid1 claims = %+v, want {ArxivClaimed:true}", claims)
	}
	got, _, err := s.Get(ctx, pid1)
	if err != nil {
		t.Fatalf("Get pid1: %v", err)
	}
	if got.ArxivID != "2401.93131" {
		t.Errorf("pid1 arxiv_id = %q, want version-stripped 2401.93131", got.ArxivID)
	}
	owner, found, err := s.LookupByIdentity(ctx, ArxivKey("2401.93131"))
	if err != nil || !found || owner != pid1 {
		t.Errorf("arxiv identity owner = %q found=%v err=%v, want %q", owner, found, err, pid1)
	}

	// Second paper claiming the same arXiv id: update lands, claim refused.
	claims, err = s.UpdateMetadata(ctx, pid2, Metadata{
		Title:   "ZZBackfillTest Arxiv Claim Other",
		Year:    2023,
		ArxivID: "2401.93131",
	})
	if err != nil {
		t.Fatalf("UpdateMetadata pid2: %v", err)
	}
	if claims.ArxivClaimed {
		t.Error("pid2 ArxivClaimed = true, want false (owned by pid1)")
	}
	got2, _, err := s.Get(ctx, pid2)
	if err != nil {
		t.Fatalf("Get pid2: %v", err)
	}
	if got2.ArxivID != "" {
		t.Errorf("pid2 arxiv_id = %q, want empty (claim refused)", got2.ArxivID)
	}
	if got2.Title != "ZZBackfillTest Arxiv Claim Other" {
		t.Errorf("pid2 title = %q (metadata should still update)", got2.Title)
	}

	// Re-updating pid1 with a different arXiv id never overwrites.
	claims, err = s.UpdateMetadata(ctx, pid1, Metadata{ArxivID: "2401.93139"})
	if err != nil {
		t.Fatalf("UpdateMetadata pid1 second arxiv: %v", err)
	}
	if claims.ArxivClaimed {
		t.Error("pid1 second ArxivClaimed = true, want false (arxiv_id already set)")
	}
	got, _, _ = s.Get(ctx, pid1)
	if got.ArxivID != "2401.93131" {
		t.Errorf("pid1 arxiv_id = %q after second update, want unchanged", got.ArxivID)
	}
}
