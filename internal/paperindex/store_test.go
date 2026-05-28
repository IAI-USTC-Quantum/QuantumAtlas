package paperindex

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// makeTestParquet builds a parquet at `path` with N papers; first
// `withMD` have markdown, first `withJSON` have json. Authoritative
// in-test fixture so tests don't depend on ship binaries.
func makeTestParquet(t *testing.T, path string, total, withMD, withJSON int) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	// Use the package's own schemaDDL so test fixture and production
	// catalog stay in sync schema-wise (any future column addition
	// is automatically reflected here).
	if _, err := db.Exec(schemaDDL); err != nil {
		t.Fatalf("schema: %v", err)
	}
	for i := 0; i < total; i++ {
		_, err := db.Exec(`INSERT INTO papers
			(arxiv_id, yymm, has_pdf, has_md, has_json, image_count,
			 pdf_size_bytes, md_size_bytes, json_size_bytes,
			 pdf_uploaded_at)
			VALUES (?, '0704', true, ?, ?, 0, ?, ?, ?, TIMESTAMP '2026-05-28 12:00:00')`,
			fmt.Sprintf("0704.%04dv1", i+1),
			i < withMD,
			i < withJSON,
			int64(100000+i),
			int64(10000),
			int64(3000),
		)
		if err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	if _, err := db.Exec(`COPY (SELECT * FROM papers) TO ? (FORMAT 'parquet', COMPRESSION 'zstd')`, path); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func TestLoadFromLocalParquet(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 10, 6, 4)

	ctx := context.Background()
	s, err := New(ctx, Config{
		LocalParquetPath: pq,
		// Keep loops slow so they don't interfere with assertions.
		RefreshInterval: time.Hour,
		FlushInterval:   time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if got, want := s.RowCount(), 10; got != want {
		t.Fatalf("RowCount = %d, want %d", got, want)
	}

	st, err := s.QueryStats(ctx)
	if err != nil {
		t.Fatalf("QueryStats: %v", err)
	}
	if st.Total != 10 || st.HasPDF != 10 || st.HasMD != 6 || st.HasJSON != 4 {
		t.Errorf("stats wrong: %+v", st)
	}
	if st.NeedsMineru != 4 || st.NeedsJSON != 6 {
		t.Errorf("derived counts wrong: needsMineru=%d needsJSON=%d", st.NeedsMineru, st.NeedsJSON)
	}
}

func TestNeedsMineruLimits(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 20, 12, 5) // 8 need mineru

	ctx := context.Background()
	s, err := New(ctx, Config{
		LocalParquetPath: pq,
		RefreshInterval:  time.Hour, FlushInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	rows, err := s.NeedsMineru(ctx, 5)
	if err != nil {
		t.Fatalf("NeedsMineru: %v", err)
	}
	if len(rows) != 5 {
		t.Errorf("limit=5 → got %d rows", len(rows))
	}
	for _, r := range rows {
		if !strings.HasPrefix(r.PDFKey, "pdf/0704/") {
			t.Errorf("pdf_key shape wrong: %q", r.PDFKey)
		}
	}

	rows2, _ := s.NeedsMineru(ctx, 100)
	if len(rows2) != 8 {
		t.Errorf("limit=100 → got %d rows, want 8", len(rows2))
	}
}

func TestMissingParquetGracefulEmpty(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, Config{
		LocalParquetPath: "/nonexistent/path/papers.parquet",
		RefreshInterval:  time.Hour, FlushInterval: time.Hour,
	})
	if err != nil {
		t.Fatalf("New should soft-fail; got %v", err)
	}
	defer s.Close()

	if got := s.RowCount(); got != 0 {
		t.Errorf("RowCount = %d, want 0", got)
	}
}

func TestUpsertPDFAsset(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 0, 0, 0) // empty catalog

	ctx := context.Background()
	s, err := New(ctx, Config{
		LocalParquetPath: pq,
		RefreshInterval:  time.Hour,
		FlushInterval:    20 * time.Millisecond, // fast flush for test
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	uploadedAt := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertPDFAsset(ctx, "2401.00001v1", "2401", 12345, uploadedAt, "abc"); err != nil {
		t.Fatalf("UpsertPDFAsset: %v", err)
	}

	if got := s.RowCount(); got != 1 {
		t.Errorf("RowCount after upsert = %d, want 1", got)
	}
	st, _ := s.QueryStats(ctx)
	if st.HasPDF != 1 || st.NeedsMineru != 1 {
		t.Errorf("stats after pdf upsert: %+v", st)
	}

	// Re-upsert with new size shouldn't dupe the row.
	if err := s.UpsertPDFAsset(ctx, "2401.00001v1", "2401", 99999, uploadedAt, "xyz"); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got := s.RowCount(); got != 1 {
		t.Errorf("RowCount after re-upsert = %d, want 1 (ON CONFLICT)", got)
	}

	// Add MD → needs-mineru should go to 0.
	if err := s.UpsertMDAsset(ctx, "2401.00001v1", "2401", 555, uploadedAt, "def"); err != nil {
		t.Fatalf("md upsert: %v", err)
	}
	st, _ = s.QueryStats(ctx)
	if st.HasPDF != 1 || st.HasMD != 1 || st.NeedsMineru != 0 {
		t.Errorf("after md upsert: %+v", st)
	}
}

func TestUpsertJSONMetadata(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 0, 0, 0)
	ctx := context.Background()
	s, err := New(ctx, Config{LocalParquetPath: pq, RefreshInterval: time.Hour, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	uploadedAt := time.Now().UTC().Truncate(time.Second)
	if err := s.UpsertJSONMetadata(ctx, "0704.2988v1", "0704", 3908, uploadedAt, "etag1", JSONMetadata{
		Title:      "On solving systems of random linear disequations",
		Abstract:   "We solve...",
		Authors:    "Gabor Ivanyos",
		Categories: "quant-ph",
		Submitter:  "G. Ivanyos",
		UpdateDate: time.Date(2007, 5, 23, 0, 0, 0, 0, time.UTC),
	}); err != nil {
		t.Fatalf("upsert json md: %v", err)
	}

	// Round-trip: query the title back.
	var title sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT title FROM papers WHERE arxiv_id = ?`, "0704.2988v1").Scan(&title); err != nil {
		t.Fatalf("query title: %v", err)
	}
	if !title.Valid || !strings.Contains(title.String, "disequations") {
		t.Errorf("title roundtrip wrong: %+v", title)
	}

	// Re-upsert with blank title → existing one should be preserved.
	if err := s.UpsertJSONMetadata(ctx, "0704.2988v1", "0704", 3908, uploadedAt, "etag2", JSONMetadata{}); err != nil {
		t.Fatalf("blank re-upsert: %v", err)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT title FROM papers WHERE arxiv_id = ?`, "0704.2988v1").Scan(&title); err != nil {
		t.Fatalf("query title 2: %v", err)
	}
	if !title.Valid || !strings.Contains(title.String, "disequations") {
		t.Errorf("title got clobbered by blank upsert: %+v", title)
	}
}

func TestAdjustImageCount(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 0, 0, 0)
	ctx := context.Background()
	s, err := New(ctx, Config{LocalParquetPath: pq, RefreshInterval: time.Hour, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	for i := 0; i < 5; i++ {
		if err := s.AdjustImageCount(ctx, "2401.0001v1", "2401", 1); err != nil {
			t.Fatalf("inc %d: %v", i, err)
		}
	}
	st, _ := s.QueryStats(ctx)
	if st.TotalImages != 5 {
		t.Errorf("after 5 inc, images=%d, want 5", st.TotalImages)
	}
	if st.Total != 1 {
		t.Errorf("Total=%d, want 1 (stub row)", st.Total)
	}

	if err := s.AdjustImageCount(ctx, "2401.0001v1", "2401", -2); err != nil {
		t.Fatalf("dec: %v", err)
	}
	st, _ = s.QueryStats(ctx)
	if st.TotalImages != 3 {
		t.Errorf("after -2, images=%d, want 3", st.TotalImages)
	}
}

func TestRemoveAsset(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 0, 0, 0)
	ctx := context.Background()
	s, err := New(ctx, Config{LocalParquetPath: pq, RefreshInterval: time.Hour, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	uploadedAt := time.Now().UTC().Truncate(time.Second)
	_ = s.UpsertPDFAsset(ctx, "2401.0001v1", "2401", 100, uploadedAt, "e1")
	_ = s.UpsertMDAsset(ctx, "2401.0001v1", "2401", 50, uploadedAt, "e2")

	st, _ := s.QueryStats(ctx)
	if st.HasPDF != 1 || st.HasMD != 1 {
		t.Fatalf("pre-remove stats: %+v", st)
	}

	if err := s.RemoveAsset(ctx, "2401.0001v1", "pdf"); err != nil {
		t.Fatalf("RemoveAsset: %v", err)
	}
	st, _ = s.QueryStats(ctx)
	if st.HasPDF != 0 || st.HasMD != 1 {
		t.Errorf("after PDF remove: %+v (want has_pdf=0, has_md=1)", st)
	}
}

func TestFlushPersistence(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 0, 0, 0)
	ctx := context.Background()

	// First Store: write something + close (synchronous final flush).
	s, err := New(ctx, Config{LocalParquetPath: pq, RefreshInterval: time.Hour, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("New 1: %v", err)
	}
	uploadedAt := time.Now().UTC().Truncate(time.Second)
	_ = s.UpsertPDFAsset(ctx, "2401.0001v1", "2401", 100, uploadedAt, "e1")
	_ = s.UpsertJSONMetadata(ctx, "2401.0001v1", "2401", 50, uploadedAt, "e2", JSONMetadata{Title: "Hi"})
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Second Store: re-load from same parquet — should see the upserts.
	s2, err := New(ctx, Config{LocalParquetPath: pq, RefreshInterval: time.Hour, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("New 2: %v", err)
	}
	defer s2.Close()
	if got := s2.RowCount(); got != 1 {
		t.Fatalf("after reload RowCount=%d want 1", got)
	}
	st, _ := s2.QueryStats(ctx)
	if st.HasPDF != 1 || st.HasJSON != 1 {
		t.Errorf("after reload stats: %+v", st)
	}
	var title sql.NullString
	if err := s2.db.QueryRowContext(ctx, `SELECT title FROM papers WHERE arxiv_id = ?`, "2401.0001v1").Scan(&title); err != nil {
		t.Fatalf("query reload title: %v", err)
	}
	if !title.Valid || title.String != "Hi" {
		t.Errorf("reloaded title wrong: %+v", title)
	}
}

func TestFlushTickerPersistsAsync(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 0, 0, 0)
	ctx := context.Background()

	s, err := New(ctx, Config{
		LocalParquetPath: pq,
		RefreshInterval:  time.Hour,
		FlushInterval:    10 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	uploadedAt := time.Now().UTC().Truncate(time.Second)
	_ = s.UpsertPDFAsset(ctx, "2401.0001v1", "2401", 100, uploadedAt, "e1")

	// Wait for async flusher to land the parquet (poll up to 2s).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if !s.isDirty() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if s.isDirty() {
		t.Fatalf("flush ticker did not clear dirty within 2s")
	}

	// Re-open as a brand new Store on the same file — should see the row.
	s2, err := New(ctx, Config{LocalParquetPath: pq, RefreshInterval: time.Hour, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("reload: %v", err)
	}
	defer s2.Close()
	if got := s2.RowCount(); got != 1 {
		t.Errorf("after async flush + reload: RowCount=%d", got)
	}
}

func TestBuildInsertSelect(t *testing.T) {
	// Simulate an old parquet without the metadata columns.
	cols := []string{"arxiv_id", "yymm", "has_pdf", "has_md", "has_json",
		"image_count", "pdf_size_bytes", "md_size_bytes", "json_size_bytes",
		"pdf_uploaded_at", "md_processed_at", "json_uploaded_at",
		"pdf_etag", "md_etag", "json_etag"}
	got := buildInsertSelect(cols)
	if !strings.Contains(got, "NULL AS title") {
		t.Errorf("expected NULL AS title in projection; got: %s", got)
	}
	if !strings.Contains(got, "arxiv_id") {
		t.Errorf("expected arxiv_id in projection; got: %s", got)
	}
	// All cols must appear.
	parts := strings.Split(got, ", ")
	if len(parts) != len(allCols) {
		t.Errorf("got %d projection parts, want %d", len(parts), len(allCols))
	}
}
