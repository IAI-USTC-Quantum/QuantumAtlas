package registry

import (
	"context"
	"strings"
)

// SearchPapersWithAssets finds papers whose title, DOI, or arXiv ID
// matches the query (case-insensitive substring) AND that have at least
// one asset row. Used by the admin asset browser.
func (s *Store) SearchPapersWithAssets(ctx context.Context, query string, limit int) ([]Paper, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	q := strings.TrimSpace(query)
	if q == "" {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.paper_id, p.arxiv_id, p.doi, p.title, p.status
		FROM papers p
		WHERE EXISTS (SELECT 1 FROM paper_assets a WHERE a.paper_id = p.paper_id)
		  AND (
			p.title ILIKE '%' || $1 || '%'
			OR p.doi ILIKE '%' || $1 || '%'
			OR p.arxiv_id ILIKE '%' || $1 || '%'
		  )
		ORDER BY p.updated_at DESC
		LIMIT $2`, q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Paper
	for rows.Next() {
		var p Paper
		var arxivID, doi *string
		if err := rows.Scan(&p.PaperID, &arxivID, &doi, &p.Title, &p.Status); err != nil {
			return nil, err
		}
		if arxivID != nil {
			p.ArxivID = *arxivID
		}
		if doi != nil {
			p.DOI = *doi
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
