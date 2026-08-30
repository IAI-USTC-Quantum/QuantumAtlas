package registry

import (
	"context"
	"fmt"
)

// PendingPaper is one persisted paper that still needs PDF ingestion.
// Ref carries every identity/metadata field required to safely redrive
// the normal Ingester.OnMint path after a process restart.
type PendingPaper struct {
	PaperID string
	Ref     PaperRef
}

// PendingPapers returns one keyset-paginated batch of pending papers.
// after is the last paper_id from the previous page; empty starts from
// the beginning. A stable paper_id cursor avoids offset skips while
// workers concurrently flip earlier rows to ready/failed.
func (s *Store) PendingPapers(ctx context.Context, after string, limit int) ([]PendingPaper, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	if limit < 1 {
		limit = 200
	}
	rows, err := s.pool.Query(ctx, `
		SELECT paper_id, arxiv_id, doi, openalex_id, title, coalesce(authors, '{}')
		FROM papers
		WHERE status = 'pending' AND paper_id > $1
		ORDER BY paper_id
		LIMIT $2`, after, limit)
	if err != nil {
		return nil, catalogUnavailable("registry: list pending papers", err)
	}
	defer rows.Close()

	out := make([]PendingPaper, 0, limit)
	for rows.Next() {
		var row PendingPaper
		var arxivID, doi, openalexID, title *string
		if err := rows.Scan(&row.PaperID, &arxivID, &doi, &openalexID, &title, &row.Ref.Authors); err != nil {
			return nil, catalogUnavailable("registry: scan pending paper", err)
		}
		row.Ref.ArxivID = deref(arxivID)
		row.Ref.DOI = deref(doi)
		row.Ref.OpenAlexID = deref(openalexID)
		row.Ref.Title = deref(title)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("registry: iterate pending papers: %w", err)
	}
	return out, nil
}
