package registry

import (
	"context"
	"errors"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/jackc/pgx/v5"
)

// IsHosted reports whether a bibliographic reference matches a papers
// row — i.e. a paper QuantumAtlas hosts. scheme is the reference
// namespace (arxiv | doi | openalex) and id its bare, unversioned
// identifier (version suffixes and DOI URL prefixes are normalized away).
// Unknown schemes and empty ids report (false, nil).
//
// Returns ErrCatalogUnavailable when the registry backend is
// unreachable; callers treat that as "not hosted" rather than failing
// the batch.
func (s *Store) IsHosted(ctx context.Context, scheme, id string) (bool, error) {
	if !s.ensure(ctx) {
		return false, ErrCatalogUnavailable
	}
	var query string
	switch scheme {
	case "arxiv":
		// papers stores the bare, version-stripped arXiv id.
		id = NormalizeArxivID(id)
		query = `SELECT EXISTS(SELECT 1 FROM papers WHERE arxiv_id = $1)`
	case "doi":
		// DOIs are stored normalized (lowercased, prefix-stripped).
		id = NormalizeDOI(id)
		query = `SELECT EXISTS(SELECT 1 FROM papers WHERE doi = $1)`
	case "openalex":
		id = strings.TrimSpace(id)
		query = `SELECT EXISTS(SELECT 1 FROM papers WHERE openalex_id = $1)`
	default:
		return false, nil
	}
	if id == "" {
		return false, nil
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, query, id).Scan(&exists); err != nil {
		return false, catalogUnavailable("registry: is-hosted "+scheme+":"+id, err)
	}
	return exists, nil
}

// LookupDOI reports whether a paper with the given DOI exists. The three
// return modes are:
//
//   - (doi, true, nil)  — a paper with this DOI exists; caller dispatches
//     to the DOI handlers.
//   - ("", false, nil)  — genuine miss (no row, or registry unconfigured).
//   - ("", false, err)  — PostgreSQL query-time error; caller MUST 503.
//
// Used by the GET /api/papers/<id>/{pdf,markdown} read path: when the
// caller supplies a DOI this is consulted FIRST — before any OpenAlex
// resolution — because DOI is the canonical identity for any work that
// has both an arXiv preprint and a DOI-only published version.
func (s *Store) LookupDOI(ctx context.Context, doi string) (string, bool, error) {
	if !s.ensure(ctx) {
		return "", false, nil
	}
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return "", false, nil
	}
	var found string
	err := s.pool.QueryRow(ctx,
		`SELECT doi FROM papers WHERE doi = $1 LIMIT 1`, norm).Scan(&found)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, catalogUnavailable("registry: lookup doi "+norm, err)
	}
	return found, true, nil
}

// LookupArxivToDOI is the reverse of LookupDOI: given a bare arXiv id,
// returns the DOI of the same paper when it also carries one. Used by the
// GET dispatch to honour "DOI is canonical" — when a DOI twin exists for
// the requested arXiv id, default to serving the DOI bytes (caller can
// opt back with ?force_arxiv=1).
//
// Caller MUST pass the BARE arXiv id (no vN suffix).
//
//   - (doi, true, nil)  — a DOI twin exists.
//   - ("", false, nil)  — no twin, empty input, or registry unconfigured.
//   - ("", false, err)  — PostgreSQL query-time error.
func (s *Store) LookupArxivToDOI(ctx context.Context, bareArxiv string) (string, bool, error) {
	if !s.ensure(ctx) {
		return "", false, nil
	}
	bare := NormalizeArxivID(bareArxiv)
	if bare == "" {
		return "", false, nil
	}
	var doi string
	err := s.pool.QueryRow(ctx, `
		SELECT doi FROM papers
		WHERE arxiv_id = $1 AND doi IS NOT NULL
		LIMIT 1`, bare).Scan(&doi)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, catalogUnavailable("registry: lookup arxiv-to-doi "+bare, err)
	}
	return doi, true, nil
}

// HasPublishedAsset reports whether the paper carrying the given DOI has
// a `published` asset (a contributor-uploaded published-version PDF).
// The GET dispatch uses it to decide whether the DOI-canonical redirect
// actually has DOI bytes to serve: a DOI attached purely by metadata
// backfill (the common case) has no published asset, so the arxiv path
// must handle the request instead.
//
//   - (true, nil)   — published asset exists; DOI-canonical serving works.
//   - (false, nil)  — no published asset, or registry unconfigured.
//   - (false, err)  — PostgreSQL query-time error (callers should treat
//     as "fall through to arxiv", same as a clean false).
func (s *Store) HasPublishedAsset(ctx context.Context, doi string) (bool, error) {
	if !s.ensure(ctx) {
		return false, nil
	}
	norm := NormalizeDOI(doi)
	if norm == "" {
		return false, nil
	}
	var exists bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM paper_assets a
			JOIN papers p ON p.paper_id = a.paper_id
			WHERE p.doi = $1 AND a.source = 'published'
		)`, norm).Scan(&exists)
	if err != nil {
		return false, catalogUnavailable("registry: has published asset "+norm, err)
	}
	return exists, nil
}
