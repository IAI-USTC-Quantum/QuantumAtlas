package papers

import (
	"context"
	"strings"
)

// IsHosted reports whether a bibliographic reference matches a paper_works row
// — i.e. a Paper QuantumAtlas hosts. scheme is the reference namespace
// (arxiv | doi | openalex) and id its bare, unversioned identifier. Used by
// GET /api/papers/lookup to flag hosted refs (ADR 0007).
//
// Returns ErrCatalogUnavailable when the catalog backend is unreachable;
// callers treat that as "not hosted" rather than failing the batch.
func (s *Store) IsHosted(ctx context.Context, scheme, id string) (bool, error) {
	if !s.ensure(ctx) {
		return false, ErrCatalogUnavailable
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return false, nil
	}
	var query string
	switch scheme {
	case "arxiv":
		// Match either the canonical (bare) form or the stored primary key.
		query = `SELECT EXISTS(SELECT 1 FROM paper_works WHERE arxiv_id_canonical = $1 OR arxiv_id = $1)`
	case "doi":
		// DOIs are stored lower-cased (normalizeDOI); compare case-insensitively.
		query = `SELECT EXISTS(SELECT 1 FROM paper_works WHERE identifier_scheme = 'doi' AND lower(doi) = lower($1))`
	case "openalex":
		query = `SELECT EXISTS(SELECT 1 FROM paper_works WHERE openalex_id = $1)`
	default:
		return false, nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, query, id).Scan(&exists); err != nil {
		return false, err
	}
	return exists, nil
}
