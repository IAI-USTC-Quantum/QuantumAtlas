package openalexcorpus

import (
	"context"
	"fmt"
	"time"
)

// Stats summarizes the corpus for dashboards / post-ingest verification.
type Stats struct {
	Works      int64
	WithArxiv  int64
	Citations  int64
	Embeddings int64
	MaxUpdated *time.Time
	LoadedAt   time.Time
}

// QueryStats returns corpus counters. Returns ErrCorpusUnavailable when
// PostgreSQL is unreachable so callers can degrade to {available:false}.
// The works count, with-arxiv count, citation-edge sum, and max
// updated_date come from a single seq-scan over openalex_works — fine for
// an operator-run sanity check, but not for a hot path at 10^8 rows.
func (s *Store) QueryStats(ctx context.Context) (Stats, error) {
	var st Stats
	if !s.ensure(ctx) {
		return st, ErrCorpusUnavailable
	}
	err := s.pool.QueryRow(ctx, `
		SELECT
			count(*)::bigint,
			count(*) FILTER (WHERE arxiv_id IS NOT NULL)::bigint,
			coalesce(sum(jsonb_array_length(openalex_referenced_work_ids)), 0)::bigint,
			max(updated_date)
		FROM openalex_works`,
	).Scan(&st.Works, &st.WithArxiv, &st.Citations, &st.MaxUpdated)
	if err != nil {
		return st, fmt.Errorf("openalexcorpus: query stats: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM work_embeddings`).Scan(&st.Embeddings); err != nil {
		return st, fmt.Errorf("openalexcorpus: query embeddings count: %w", err)
	}
	st.LoadedAt = time.Now().UTC()
	return st, nil
}

// CountWorks returns the number of rows in openalex_works. Separated from
// QueryStats so an ingest progress check doesn't pay for the child-table
// counts.
func (s *Store) CountWorks(ctx context.Context) (int64, error) {
	if !s.ensure(ctx) {
		return 0, ErrCorpusUnavailable
	}
	var n int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*)::bigint FROM openalex_works`).Scan(&n); err != nil {
		return 0, fmt.Errorf("openalexcorpus: count works: %w", err)
	}
	return n, nil
}
