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
// The counts are exact (cheap on the indexed columns); the openalex_works
// total is a seq-scan count which is fine for an operator-run sanity check
// but should not be polled on a hot path at 10^8 rows.
func (s *Store) QueryStats(ctx context.Context) (Stats, error) {
	var st Stats
	if !s.ensure(ctx) {
		return st, ErrCorpusUnavailable
	}
	err := s.pool.QueryRow(ctx, `
		SELECT
			(SELECT count(*) FROM openalex_works)::bigint,
			(SELECT count(*) FROM openalex_works WHERE arxiv_id IS NOT NULL)::bigint,
			(SELECT count(*) FROM work_referenced)::bigint,
			(SELECT count(*) FROM work_embeddings)::bigint,
			(SELECT max(updated_date) FROM openalex_works)`,
	).Scan(&st.Works, &st.WithArxiv, &st.Citations, &st.Embeddings, &st.MaxUpdated)
	if err != nil {
		return st, fmt.Errorf("openalexcorpus: query stats: %w", err)
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
