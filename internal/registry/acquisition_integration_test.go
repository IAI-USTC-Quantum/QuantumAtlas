package registry

import "testing"

func TestIntegrationAcquisitionFailureLog(t *testing.T) {
	s, ctx := migrateStore(t)
	id, _, err := s.ResolveOrMint(ctx, PaperRef{DOI: "10.5555/zz-acquisition-failure", Title: "ZZ Acquisition Failure"})
	if err != nil {
		t.Fatalf("mint: %v", err)
	}
	defer cleanupPapers(t, s, id)

	if _, err := s.UpdateStatus(ctx, id, "failed"); err != nil {
		t.Fatalf("mark failed: %v", err)
	}
	if err := s.RecordAcquisitionEvent(ctx, id, "queued", "queued", ""); err != nil {
		t.Fatalf("record queued: %v", err)
	}
	if err := s.RecordAcquisitionEvent(ctx, id, "fetch-doi", "failed", "publisher returned HTTP 403"); err != nil {
		t.Fatalf("record failed: %v", err)
	}

	rows, err := s.ListAcquisitionFailures(ctx, 100)
	if err != nil {
		t.Fatalf("ListAcquisitionFailures: %v", err)
	}
	for _, row := range rows {
		if row.PaperID != id {
			continue
		}
		if row.Stage != "fetch-doi" || row.Reason != "publisher returned HTTP 403" || row.Attempts != 1 {
			t.Fatalf("failure row = %+v", row)
		}
		return
	}
	t.Fatalf("failure %s absent from %+v", id, rows)
}

func TestIntegrationStatsCountsConvertedMarkdown(t *testing.T) {
	s, ctx := migrateStore(t)
	before, err := s.QueryStats(ctx)
	if err != nil {
		t.Fatalf("stats before: %v", err)
	}
	ref := PaperRef{ArxivID: "2401.93992v1", Title: "ZZ Converted Markdown Count"}
	id, _, err := s.UpsertPDF(ctx, ref, 1, "", 123, "2401/2401.93992v1.pdf")
	if err != nil {
		t.Fatalf("UpsertPDF: %v", err)
	}
	defer cleanupPapers(t, s, id)
	if err := s.UpsertMD(ctx, id, 1, "", 0, "2401/2401.93992v1.md", "2401/2401.93992v1.json", 2); err != nil {
		t.Fatalf("UpsertMD: %v", err)
	}
	after, err := s.QueryStats(ctx)
	if err != nil {
		t.Fatalf("stats after: %v", err)
	}
	if after.ConvertedMarkdown != before.ConvertedMarkdown+1 {
		t.Fatalf("converted markdown before=%d after=%d", before.ConvertedMarkdown, after.ConvertedMarkdown)
	}
}
