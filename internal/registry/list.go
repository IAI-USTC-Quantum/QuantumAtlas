package registry

// list.go: paginated papers listing for the GET /api/papers list
// endpoint (the "converted papers" frontend page). Asset projections
// (has_pdf / has_md / image_count) come from the paper's default asset
// (papers.default_asset_id — published first, else highest arXiv
// version, maintained by the paper_assets trigger).

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ListFilter narrows ListPapers. The zero value lists every non-merged
// paper, newest-created first.
type ListFilter struct {
	// HasMD, when non-nil, restricts to papers whose default asset has
	// converted markdown (true) or lacks it (false).
	HasMD *bool
	// Status restricts to one lifecycle status (pending | ready |
	// failed). Empty = no status filter.
	Status string
	// Query is a case-insensitive title substring match (ILIKE).
	Query string
	// Page is 1-based; PerPage is the page size. Callers clamp both.
	Page, PerPage int
	// Sort is "created_at" (default) or "updated_at", always descending.
	Sort string
}

// ListItem projects one papers row plus its default-asset state.
type ListItem struct {
	PaperID    string
	ArxivID    string
	DOI        string
	Title      string
	Status     string
	HasPDF     bool // a default asset exists (pdf_path is NOT NULL per schema)
	HasMD      bool // the default asset's mineru_md_path IS NOT NULL
	ImageCount int  // the default asset's image_count (0 when unset)
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// ListPapers returns one page of papers matching f plus the total match
// count across all pages. Papers merged into another (status
// 'merged_into:...') are tombstones and never listed.
func (s *Store) ListPapers(ctx context.Context, f ListFilter) (items []ListItem, total int, err error) {
	if !s.ensure(ctx) {
		return nil, 0, ErrCatalogUnavailable
	}

	where := []string{"p.status NOT LIKE 'merged_into:%'"}
	args := []any{}
	add := func(clause string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(clause, len(args)))
	}
	if f.HasMD != nil {
		if *f.HasMD {
			where = append(where, "a.mineru_md_path IS NOT NULL")
		} else {
			where = append(where, "a.mineru_md_path IS NULL")
		}
	}
	if f.Status != "" {
		add("p.status = $%d", f.Status)
	}
	if q := strings.TrimSpace(f.Query); q != "" {
		add("p.title ILIKE '%%' || $%d || '%%'", q)
	}
	whereSQL := strings.Join(where, " AND ")

	from := `FROM papers p
		LEFT JOIN paper_assets a ON a.asset_id = p.default_asset_id
		WHERE ` + whereSQL

	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) `+from, args...).Scan(&total); err != nil {
		return nil, 0, catalogUnavailable("registry: list papers count", err)
	}

	sortCol := "p.created_at"
	if f.Sort == "updated_at" {
		sortCol = "p.updated_at"
	}
	offset := (f.Page - 1) * f.PerPage
	if offset < 0 {
		offset = 0
	}
	args = append(args, f.PerPage, offset)
	query := fmt.Sprintf(`
		SELECT p.paper_id, p.arxiv_id, p.doi, p.title, p.status,
		       a.asset_id IS NOT NULL, a.mineru_md_path IS NOT NULL,
		       coalesce(a.image_count, 0), p.created_at, p.updated_at
		%s
		ORDER BY %s DESC, p.paper_id
		LIMIT $%d OFFSET $%d`, from, sortCol, len(args)-1, len(args))
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, 0, catalogUnavailable("registry: list papers", err)
	}
	defer rows.Close()
	items = []ListItem{}
	for rows.Next() {
		var (
			it         ListItem
			arxiv, doi *string
			title      *string
		)
		if err := rows.Scan(&it.PaperID, &arxiv, &doi, &title, &it.Status,
			&it.HasPDF, &it.HasMD, &it.ImageCount, &it.CreatedAt, &it.UpdatedAt); err != nil {
			return nil, 0, catalogUnavailable("registry: scan paper list row", err)
		}
		it.ArxivID = deref(arxiv)
		it.DOI = deref(doi)
		it.Title = deref(title)
		items = append(items, it)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, catalogUnavailable("registry: iterate paper list", err)
	}
	return items, total, nil
}
