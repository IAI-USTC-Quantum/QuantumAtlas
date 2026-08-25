// HTTP-layer tests for GET /api/admin/db/tables/{name}/rows
// (admin_dbrows.go).
//
//   - adminGuard contract: anonymous 401, non-admin session 403,
//     admin session reaches the handler (503 on the harness's nil pool,
//     same convention as /api/admin/db/schema)
//   - fetchDBRows against a live PostgreSQL, gated on
//     QATLAS_TEST_PG_DSN (same skip convention as admin_test.go):
//     happy-path page over a probe table with bytea/timestamptz/NULL
//     value normalization, plus the unknown-table 404 sentinel
package routes

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAPI_Admin_DBRowsGate(t *testing.T) {
	h := newAdminHarness(t)
	url := "/api/admin/db/tables/papers/rows"

	status, _, _ := h.do(http.MethodGet, url, "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("anonymous: status = %d, want 401", status)
	}

	status, _, body := h.do(http.MethodGet, url, "", rawHeader(h.sessionToken()))
	if status != http.StatusForbidden {
		t.Fatalf("non-admin: status = %d, want 403; body=%v", status, body)
	}
	if asString(body["detail"]) != "admin only" {
		t.Errorf("detail = %q, want %q", body["detail"], "admin only")
	}
}

func TestAPI_Admin_DBRowsNilPool(t *testing.T) {
	h := newAdminHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/admin/db/tables/papers/rows", "", rawHeader(h.adminSessionToken()))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nil pool); body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "QATLAS_POSTGRES_DSN") {
		t.Errorf("detail should mention QATLAS_POSTGRES_DSN; got %q", body["detail"])
	}
}

// TestFetchDBRows_LivePostgres exercises the happy path and the
// unknown-table sentinel against a real Postgres.
func TestFetchDBRows_LivePostgres(t *testing.T) {
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

	if _, err := pool.Exec(ctx, `
		CREATE TABLE IF NOT EXISTS admin_rows_probe (
			id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
			title text,
			blob bytea,
			stamp timestamptz,
			missing text
		);
		TRUNCATE admin_rows_probe;
		INSERT INTO admin_rows_probe (title, blob, stamp, missing) VALUES
			('one', '\x68656c6c6f'::bytea, '2026-01-02T03:04:05Z', NULL),
			('two', NULL, NULL, NULL),
			('three', '\x00'::bytea, NULL, NULL)`); err != nil {
		t.Fatalf("seed probe table: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if _, err := pool.Exec(ctx, "DROP TABLE IF EXISTS admin_rows_probe"); err != nil {
			t.Errorf("drop probe table: %v", err)
		}
	})

	// Unknown table → not-found sentinel (the handler maps it to 404).
	if _, err := fetchDBRows(ctx, pool, "no_such_table_xyz", 1, 20); !errors.Is(err, errAdminTableNotFound) {
		t.Fatalf("unknown table: err = %v, want errAdminTableNotFound", err)
	}

	page1, err := fetchDBRows(ctx, pool, "admin_rows_probe", 1, 2)
	if err != nil {
		t.Fatalf("fetchDBRows page 1: %v", err)
	}
	if page1.Table != "admin_rows_probe" {
		t.Errorf("table = %q", page1.Table)
	}
	if page1.Total != 3 {
		t.Errorf("total = %d, want 3", page1.Total)
	}
	if page1.Page != 1 || page1.PerPage != 2 {
		t.Errorf("page/per_page = %d/%d, want 1/2", page1.Page, page1.PerPage)
	}
	wantCols := []string{"id", "title", "blob", "stamp", "missing"}
	if strings.Join(page1.Columns, ",") != strings.Join(wantCols, ",") {
		t.Errorf("columns = %v, want %v", page1.Columns, wantCols)
	}
	if len(page1.Rows) != 2 {
		t.Fatalf("page 1 rows = %d, want 2", len(page1.Rows))
	}

	row1 := page1.Rows[0]
	if _, ok := row1[0].(int64); !ok {
		t.Errorf("id should stay numeric (int64), got %T (%v)", row1[0], row1[0])
	}
	if row1[1] != "one" {
		t.Errorf("title = %v, want one", row1[1])
	}
	if row1[2] != "hello" {
		t.Errorf("bytea should normalize to string \"hello\", got %T (%v)", row1[2], row1[2])
	}
	if row1[3] != "2026-01-02T03:04:05Z" {
		t.Errorf("timestamptz should normalize to RFC3339, got %T (%v)", row1[3], row1[3])
	}
	if row1[4] != nil {
		t.Errorf("NULL should stay nil, got %T (%v)", row1[4], row1[4])
	}

	// Page 2 proves OFFSET pagination sees the third row.
	page2, err := fetchDBRows(ctx, pool, "admin_rows_probe", 2, 2)
	if err != nil {
		t.Fatalf("fetchDBRows page 2: %v", err)
	}
	if len(page2.Rows) != 1 || page2.Rows[0][1] != "three" {
		t.Errorf("page 2 rows = %v, want the single 'three' row", page2.Rows)
	}
}
