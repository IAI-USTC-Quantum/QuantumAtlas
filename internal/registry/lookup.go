package registry

import (
	"context"
	"errors"
	"fmt"
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

// GetPaperIDByIdentity is the detail-returning twin of IsHosted: it
// resolves a bibliographic identity to the owning papers.paper_id
// instead of a bare existence flag. scheme / id semantics mirror
// IsHosted exactly (arxiv | doi | openalex; bare unversioned ids,
// normalized DOIs). Used by GET /api/papers/<identifier> — the paper
// detail dispatched by arXiv id / DOI rather than surrogate id.
//
//   - (id, true, nil)  — the identity is hosted; id is the paper_id.
//   - ("", false, nil) — genuine miss, unknown scheme, or empty id.
//   - ("", false, err) — PostgreSQL query-time error (incl. unconfigured
//     pool); callers answer 503, matching the qa_ detail handler's
//     convention — serving 404 here would misreport a hosted paper.
func (s *Store) GetPaperIDByIdentity(ctx context.Context, scheme, id string) (string, bool, error) {
	if !s.ensure(ctx) {
		return "", false, ErrCatalogUnavailable
	}
	var query string
	switch scheme {
	case "arxiv":
		id = NormalizeArxivID(id)
		query = `SELECT paper_id FROM papers WHERE arxiv_id = $1 LIMIT 1`
	case "doi":
		id = NormalizeDOI(id)
		query = `SELECT paper_id FROM papers WHERE doi = $1 LIMIT 1`
	case "openalex":
		id = strings.TrimSpace(id)
		query = `SELECT paper_id FROM papers WHERE openalex_id = $1 LIMIT 1`
	default:
		return "", false, nil
	}
	if id == "" {
		return "", false, nil
	}
	var paperID string
	err := s.pool.QueryRow(ctx, query, id).Scan(&paperID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, catalogUnavailable("registry: get paper by identity "+scheme+":"+id, err)
	}
	return paperID, true, nil
}

// HostedWithMD extends IsHosted with the caller-facing "is the markdown
// ready" bit: hosted reports whether the identity matches a papers row,
// hasMD whether that paper's default asset carries converted markdown.
// Used by GET /api/papers/lookup so batch consumers learn both facts in
// one call. Error semantics mirror IsHosted (ErrCatalogUnavailable on
// query-time failure; callers degrade to (false, false)).
func (s *Store) HostedWithMD(ctx context.Context, scheme, id string) (hosted, hasMD bool, err error) {
	if !s.ensure(ctx) {
		return false, false, ErrCatalogUnavailable
	}
	var col string
	switch scheme {
	case "arxiv":
		col, id = "arxiv_id", NormalizeArxivID(id)
	case "doi":
		col, id = "doi", NormalizeDOI(id)
	case "openalex":
		col, id = "openalex_id", strings.TrimSpace(id)
	default:
		return false, false, nil
	}
	if id == "" {
		return false, false, nil
	}
	query := fmt.Sprintf(`
		SELECT EXISTS(SELECT 1 FROM papers WHERE %[1]s = $1),
		       EXISTS(
		           SELECT 1 FROM papers p
		           LEFT JOIN paper_assets a ON a.asset_id = p.default_asset_id
		           WHERE p.%[1]s = $1 AND a.mineru_md_path IS NOT NULL
		       )`, col)
	if err := s.pool.QueryRow(ctx, query, id).Scan(&hosted, &hasMD); err != nil {
		return false, false, catalogUnavailable("registry: hosted-with-md "+scheme+":"+id, err)
	}
	return hosted, hasMD, nil
}

// LatestArxivAssetVersion returns the highest arXiv version among the
// paper's arxiv-source assets — the version the server already holds
// bytes for. bareArxiv is normalized (version-stripped) before lookup.
//
//   - (v>0, true, nil)  — found; v is the highest ingested version.
//   - (0, false, nil)   — paper unknown, no arxiv asset, or no version.
//   - (0, false, err)   — PostgreSQL query-time error; callers fall back
//     to their pre-catalog path (e.g. the arxiv.org latest-version
//     scrape) rather than failing the request.
func (s *Store) LatestArxivAssetVersion(ctx context.Context, bareArxiv string) (int, bool, error) {
	if !s.ensure(ctx) {
		return 0, false, ErrCatalogUnavailable
	}
	bare := NormalizeArxivID(bareArxiv)
	if bare == "" {
		return 0, false, nil
	}
	var version *int
	err := s.pool.QueryRow(ctx, `
		SELECT max(a.arxiv_version)
		FROM paper_assets a
		JOIN papers p ON p.paper_id = a.paper_id
		WHERE p.arxiv_id = $1 AND a.source = 'arxiv'`, bare).Scan(&version)
	if err != nil {
		return 0, false, catalogUnavailable("registry: latest arxiv version "+bare, err)
	}
	if version == nil || *version <= 0 {
		return 0, false, nil
	}
	return *version, true, nil
}

// PaperSummary is the hosting projection attached to search results:
// the paper's lifecycle status plus its default-asset availability.
type PaperSummary struct {
	Status string
	HasMD  bool
	HasPDF bool
}

// PaperSummaries returns the default-asset summary for every requested
// paper_id in one query (= ANY + LEFT JOIN, mirroring ListPapers).
// paper_ids that don't resolve are simply absent from the map. An empty
// input returns an empty map without touching the database.
func (s *Store) PaperSummaries(ctx context.Context, paperIDs []string) (map[string]PaperSummary, error) {
	if len(paperIDs) == 0 {
		return map[string]PaperSummary{}, nil
	}
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.paper_id, p.status,
		       a.asset_id IS NOT NULL,
		       a.mineru_md_path IS NOT NULL
		FROM papers p
		LEFT JOIN paper_assets a ON a.asset_id = p.default_asset_id
		WHERE p.paper_id = ANY($1)`, paperIDs)
	if err != nil {
		return nil, catalogUnavailable("registry: paper summaries", err)
	}
	defer rows.Close()
	out := make(map[string]PaperSummary, len(paperIDs))
	for rows.Next() {
		var (
			id string
			ps PaperSummary
		)
		if err := rows.Scan(&id, &ps.Status, &ps.HasPDF, &ps.HasMD); err != nil {
			return nil, catalogUnavailable("registry: scan paper summary", err)
		}
		out[id] = ps
	}
	if err := rows.Err(); err != nil {
		return nil, catalogUnavailable("registry: iterate paper summaries", err)
	}
	return out, nil
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
