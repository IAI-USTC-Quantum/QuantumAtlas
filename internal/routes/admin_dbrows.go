// Admin DB row browser: GET /api/admin/db/tables/{name}/rows.
//
// Read-only paginated peek into the raw rows of a Postgres registry
// table (same pool as /api/admin/db/schema). The table name is NOT
// interpolated into SQL: it is validated with to_regclass first (nil →
// 404) and the identifier is quoted via pgx.Identifier.Sanitize; the
// pagination parameters are ordinary $n placeholders. Values are
// normalized to JSON-friendly shapes (bytea → string, timestamps →
// RFC3339); everything else goes through as pgx decoded it (int64,
// float64, bool, string) so numbers stay numbers in the response.
package routes

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pocketbase/pocketbase/core"
)

const (
	adminDBRowsDefaultPerPage = 20
	adminDBRowsMaxPerPage     = 100
)

// errAdminTableNotFound is returned by fetchDBRows when to_regclass
// resolves to NULL — the table does not exist in the public schema.
var errAdminTableNotFound = errors.New("table not found")

// Response contract for GET /api/admin/db/tables/{name}/rows. rows is
// a row-major matrix aligned with columns; per_page follows the
// papers-list convention (default 20, max 100).
type adminDBRowsResponse struct {
	Table   string   `json:"table"`
	Columns []string `json:"columns"`
	Rows    [][]any  `json:"rows"`
	Total   int64    `json:"total"`
	Page    int      `json:"page"`
	PerPage int      `json:"per_page"`
}

func adminDBRowsHandler(pool *pgxpool.Pool) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if pool == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "postgres registry unavailable (QATLAS_POSTGRES_DSN unset)",
			})
		}
		name := re.Request.PathValue("name")
		q := re.Request.URL.Query()
		page, _ := strconv.Atoi(q.Get("page"))
		if page < 1 {
			page = 1
		}
		perPage, _ := strconv.Atoi(q.Get("per_page"))
		if perPage < 1 {
			perPage = adminDBRowsDefaultPerPage
		} else if perPage > adminDBRowsMaxPerPage {
			perPage = adminDBRowsMaxPerPage
		}

		ctx, cancel := context.WithTimeout(re.Request.Context(), 15*time.Second)
		defer cancel()
		resp, err := fetchDBRows(ctx, pool, name, page, perPage)
		if err != nil {
			if errors.Is(err, errAdminTableNotFound) {
				return re.JSON(http.StatusNotFound, map[string]string{
					"detail": "table not found",
				})
			}
			slog.Error("admin: db rows query failed", "table", name, "error", err)
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "postgres registry unavailable (row query failed); retry shortly",
			})
		}
		return re.JSON(http.StatusOK, resp)
	}
}

// fetchDBRows validates the table, counts it, then reads one page. All
// three statements parameterize what they can; the table identifier is
// quoted (never interpolated raw) after to_regclass has confirmed the
// table exists in the public schema.
func fetchDBRows(ctx context.Context, pool *pgxpool.Pool, name string, page, perPage int) (*adminDBRowsResponse, error) {
	var regclass *string
	if err := pool.QueryRow(ctx, `SELECT to_regclass('public.' || $1)`, name).Scan(&regclass); err != nil {
		return nil, err
	}
	if regclass == nil {
		return nil, errAdminTableNotFound
	}

	ident := pgx.Identifier{name}.Sanitize()
	resp := &adminDBRowsResponse{
		Table:   name,
		Columns: []string{},
		Rows:    [][]any{},
		Page:    page,
		PerPage: perPage,
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM public.`+ident).Scan(&resp.Total); err != nil {
		return nil, err
	}

	rows, err := pool.Query(ctx,
		`SELECT * FROM public.`+ident+` LIMIT $1 OFFSET $2`,
		perPage, (page-1)*perPage)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for _, fd := range rows.FieldDescriptions() {
		resp.Columns = append(resp.Columns, fd.Name)
	}
	for rows.Next() {
		values, err := rows.Values()
		if err != nil {
			return nil, err
		}
		row := make([]any, len(values))
		for i, v := range values {
			row[i] = normalizeDBRowValue(v)
		}
		resp.Rows = append(resp.Rows, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return resp, nil
}

// normalizeDBRowValue maps pgx-decoded values onto JSON-friendly
// shapes. pgx already returns int64/float64/bool/string for the common
// types; only the awkward ones (bytea, timestamps) need converting.
// nil passes through as JSON null. Unknown driver types are left as-is
// rather than fmt.Sprint'd, so numeric types never degrade to strings.
func normalizeDBRowValue(v any) any {
	switch t := v.(type) {
	case nil:
		return nil
	case []byte:
		return string(t)
	case time.Time:
		return t.Format(time.RFC3339)
	default:
		return v
	}
}
