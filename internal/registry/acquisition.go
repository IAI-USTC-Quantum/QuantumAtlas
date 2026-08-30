package registry

import (
	"context"
	"strings"
	"time"
)

const maxAcquisitionDetail = 4000

// RecordAcquisitionEvent appends one durable PDF acquisition transition.
// detail is bounded so an upstream HTML error page cannot bloat the log.
func (s *Store) RecordAcquisitionEvent(ctx context.Context, paperID, phase, state, detail string) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	detail = strings.TrimSpace(detail)
	runes := []rune(detail)
	if len(runes) > maxAcquisitionDetail {
		detail = string(runes[:maxAcquisitionDetail])
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO paper_acquisition_events (paper_id, phase, state, detail)
		VALUES ($1, $2, $3, nullif($4, ''))`, paperID, phase, state, detail); err != nil {
		return catalogUnavailable("registry: record acquisition event "+paperID, err)
	}
	return nil
}

// AcquisitionFailure is one failed paper plus its latest durable failure.
// Rows created before migration 00002 have blank Stage/Reason and zero
// Attempts; UpdatedAt still gives admins a useful historical timestamp.
type AcquisitionFailure struct {
	PaperID  string    `json:"paper_id"`
	ArxivID  string    `json:"arxiv_id,omitempty"`
	DOI      string    `json:"doi,omitempty"`
	Title    string    `json:"title,omitempty"`
	Stage    string    `json:"stage,omitempty"`
	Reason   string    `json:"reason,omitempty"`
	FailedAt time.Time `json:"failed_at"`
	Attempts int       `json:"attempts"`
}

// ListAcquisitionFailures returns the most recently failed papers first.
func (s *Store) ListAcquisitionFailures(ctx context.Context, limit int) ([]AcquisitionFailure, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	if limit < 1 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.paper_id, p.arxiv_id, p.doi, p.title,
		       coalesce(latest.phase, ''), coalesce(latest.detail, ''),
		       coalesce(latest.created_at, p.updated_at), coalesce(attempts.n, 0)
		FROM papers p
		LEFT JOIN LATERAL (
			SELECT phase, detail, created_at
			FROM paper_acquisition_events
			WHERE paper_id = p.paper_id AND state = 'failed'
			ORDER BY created_at DESC, event_id DESC
			LIMIT 1
		) latest ON true
		LEFT JOIN LATERAL (
			SELECT count(*)::int AS n
			FROM paper_acquisition_events
			WHERE paper_id = p.paper_id AND phase = 'queued'
		) attempts ON true
		WHERE p.status = 'failed'
		ORDER BY coalesce(latest.created_at, p.updated_at) DESC, p.paper_id
		LIMIT $1`, limit)
	if err != nil {
		return nil, catalogUnavailable("registry: list acquisition failures", err)
	}
	defer rows.Close()
	out := make([]AcquisitionFailure, 0, limit)
	for rows.Next() {
		var row AcquisitionFailure
		var arxivID, doi, title *string
		if err := rows.Scan(&row.PaperID, &arxivID, &doi, &title,
			&row.Stage, &row.Reason, &row.FailedAt, &row.Attempts); err != nil {
			return nil, catalogUnavailable("registry: scan acquisition failure", err)
		}
		row.ArxivID = deref(arxivID)
		row.DOI = deref(doi)
		row.Title = deref(title)
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, catalogUnavailable("registry: iterate acquisition failures", err)
	}
	return out, nil
}
