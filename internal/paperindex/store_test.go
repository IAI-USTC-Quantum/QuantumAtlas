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

// makeTestParquet creates a small parquet at filepath via DuckDB so
// tests don't need to ship a binary fixture. Returns the path so the
// test can pass it to Config.LocalParquetPath.
func makeTestParquet(t *testing.T, path string, rows int, withMD int, withJSON int) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open duckdb: %v", err)
	}
	defer db.Close()

	// Build a deterministic test set: rows total, first `withMD` of
	// them have markdown, first `withJSON` have json. Sizes are
	// arbitrary nonzero integers so we can assert on them.
	if _, err := db.Exec(`CREATE TABLE papers (
		arxiv_id VARCHAR, yymm VARCHAR,
		has_pdf BOOLEAN, has_md BOOLEAN, has_json BOOLEAN,
		image_count INTEGER,
		pdf_size_bytes BIGINT, md_size_bytes BIGINT, json_size_bytes BIGINT,
		pdf_uploaded_at TIMESTAMP, md_processed_at TIMESTAMP, json_uploaded_at TIMESTAMP,
		pdf_etag VARCHAR, md_etag VARCHAR, json_etag VARCHAR
	)`); err != nil {
		t.Fatalf("create test table: %v", err)
	}

	for i := 0; i < rows; i++ {
		hasMD := i < withMD
		hasJSON := i < withJSON
		arxivID := fmt.Sprintf("0704.%04dv1", i+1)
		_, err := db.Exec(`INSERT INTO papers VALUES (
			?, '0704', true, ?, ?, 0,
			?, ?, ?,
			TIMESTAMP '2026-05-28 12:00:00',
			NULL, NULL, 'etagpdf', NULL, NULL
		)`, arxivID, hasMD, hasJSON, int64(100000+i), int64(10000), int64(3000))
		if err != nil {
			t.Fatalf("insert row %d: %v", i, err)
		}
	}

	if _, err := db.Exec(`COPY papers TO ? (FORMAT 'parquet', COMPRESSION 'zstd')`, path); err != nil {
		t.Fatalf("write test parquet: %v", err)
	}
}

func TestStoreLoadFromLocalParquet(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	// 10 papers, 6 with MD, 4 with JSON → 4 need MinerU, 6 need JSON
	makeTestParquet(t, pq, 10, 6, 4)

	ctx := context.Background()
	s, err := New(ctx, Config{LocalParquetPath: pq})
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
		t.Errorf("Stats counts wrong: %+v", st)
	}
	if st.NeedsMineru != 4 {
		t.Errorf("NeedsMineru = %d, want 4", st.NeedsMineru)
	}
	if st.NeedsJSON != 6 {
		t.Errorf("NeedsJSON = %d, want 6", st.NeedsJSON)
	}
}

func TestStoreNeedsMineru(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 20, 12, 5) // 8 need mineru

	ctx := context.Background()
	s, err := New(ctx, Config{LocalParquetPath: pq})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	rows, err := s.NeedsMineru(ctx, 5) // limit smaller than total
	if err != nil {
		t.Fatalf("NeedsMineru: %v", err)
	}
	if len(rows) != 5 {
		t.Errorf("len(rows) = %d, want 5", len(rows))
	}
	for _, r := range rows {
		if r.PDFKey == "" || !strings.HasPrefix(r.PDFKey, "pdf/0704/") {
			t.Errorf("malformed PDFKey: %q", r.PDFKey)
		}
	}

	// limit > total returns just the total (8 mineru rows)
	all, err := s.NeedsMineru(ctx, 100)
	if err != nil {
		t.Fatalf("NeedsMineru: %v", err)
	}
	if len(all) != 8 {
		t.Errorf("len(all) = %d, want 8", len(all))
	}
}

func TestStoreLoadMissingParquetGracefulEmpty(t *testing.T) {
	// Point at a path that doesn't exist — Store should fall back to
	// the empty-table schema and serve 0s rather than fail.
	ctx := context.Background()
	s, err := New(ctx, Config{LocalParquetPath: "/nonexistent/path/papers.parquet"})
	if err != nil {
		t.Fatalf("New should soft-fail on missing parquet, got: %v", err)
	}
	defer s.Close()

	if got := s.RowCount(); got != 0 {
		t.Errorf("RowCount = %d, want 0 for missing parquet", got)
	}

	st, err := s.QueryStats(ctx)
	if err != nil {
		t.Fatalf("QueryStats on empty: %v", err)
	}
	if st.Total != 0 {
		t.Errorf("Stats.Total = %d, want 0", st.Total)
	}
}

func TestStoreRefreshPicksUpNewParquet(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeTestParquet(t, pq, 5, 5, 5)

	ctx := context.Background()
	s, err := New(ctx, Config{
		LocalParquetPath: pq,
		RefreshInterval:  10 * time.Millisecond, // fast tick for test
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if got := s.RowCount(); got != 5 {
		t.Fatalf("initial RowCount = %d, want 5", got)
	}

	// Rewrite parquet with more rows; refreshLoop should pick it up.
	makeTestParquet(t, pq, 12, 6, 3)

	// Wait for refreshLoop to run at least once (10ms tick).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s.RowCount() == 12 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if got := s.RowCount(); got != 12 {
		t.Errorf("after refresh: RowCount = %d, want 12", got)
	}
}

func TestSQLQuoteLiteral(t *testing.T) {
	cases := []struct{ in, want string }{
		{"foo", "'foo'"},
		{"", "''"},
		{"don't", "'don''t'"},
		{"a'b'c", "'a''b''c'"},
	}
	for _, c := range cases {
		if got := sqlQuoteLiteral(c.in); got != c.want {
			t.Errorf("sqlQuoteLiteral(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
