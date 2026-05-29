package routes

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperindex"

	"github.com/pocketbase/pocketbase/core"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// makeStatsParquet seeds a parquet with `total` papers where the first
// `withMD` have markdown. All have a PDF. Mirrors the fixture style in
// internal/paperindex/store_test.go but kept local so the routes test
// has no cross-package test dependency.
func makeStatsParquet(t *testing.T, path string, total, withMD int) {
	t.Helper()
	db, err := sql.Open("duckdb", "")
	if err != nil {
		t.Fatalf("open duckdb: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE papers (
		arxiv_id VARCHAR PRIMARY KEY,
		yymm VARCHAR,
		has_pdf BOOLEAN,
		has_md BOOLEAN,
		has_json BOOLEAN,
		image_count INTEGER,
		pdf_size_bytes BIGINT,
		md_size_bytes BIGINT,
		json_size_bytes BIGINT,
		pdf_uploaded_at TIMESTAMP
	)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	for i := 0; i < total; i++ {
		if _, err := db.Exec(`INSERT INTO papers
			(arxiv_id, yymm, has_pdf, has_md, has_json, image_count,
			 pdf_size_bytes, md_size_bytes, json_size_bytes, pdf_uploaded_at)
			VALUES (?, '0704', true, ?, false, 0, 100, 50, 0, TIMESTAMP '2026-05-28 12:00:00')`,
			filepathSafeID(i), i < withMD); err != nil {
			t.Fatalf("seed row %d: %v", i, err)
		}
	}
	if _, err := db.Exec(`COPY (SELECT * FROM papers) TO ? (FORMAT 'parquet')`, path); err != nil {
		t.Fatalf("write parquet: %v", err)
	}
}

func filepathSafeID(i int) string {
	return "0704." + padID(i+1) + "v1"
}

func padID(n int) string {
	s := ""
	for _, d := range []int{1000, 100, 10, 1} {
		s += string(rune('0' + (n/d)%10))
	}
	return s
}

// callStats invokes paperStatsHandler with a synthetic request and
// decodes the JSON body.
func callStats(t *testing.T, idx *paperindex.Store) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/papers/stats", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := paperStatsHandler(re, idx); err != nil {
		t.Fatalf("paperStatsHandler: %v", err)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body %q: %v", rec.Body.String(), err)
	}
	return body
}

func TestPaperStatsHandlerNilIndex(t *testing.T) {
	body := callStats(t, nil)
	if avail, _ := body["available"].(bool); avail {
		t.Fatalf("expected available=false when paperIndex is nil, got %v", body)
	}
}

func TestPaperStatsHandlerCounts(t *testing.T) {
	pq := filepath.Join(t.TempDir(), "papers.parquet")
	makeStatsParquet(t, pq, 7, 3)

	ctx := context.Background()
	s, err := paperindex.New(ctx, paperindex.Config{
		LocalParquetPath: pq,
		RefreshInterval:  time.Hour,
		FlushInterval:    time.Hour,
	})
	if err != nil {
		t.Fatalf("paperindex.New: %v", err)
	}
	defer s.Close()

	body := callStats(t, s)
	if avail, _ := body["available"].(bool); !avail {
		t.Fatalf("expected available=true, got %v", body)
	}
	if got := body["total"].(float64); got != 7 {
		t.Errorf("total = %v, want 7", got)
	}
	if got := body["has_pdf"].(float64); got != 7 {
		t.Errorf("has_pdf = %v, want 7", got)
	}
	if got := body["has_md"].(float64); got != 3 {
		t.Errorf("has_md = %v, want 3", got)
	}
}
