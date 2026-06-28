package papers

// doi_store.go: catalog write-through for DOI-indexed contributions.
//
// A DOI contribution records a PDF / markdown for a *published* version
// of a paper, which may have no arXiv preprint at all. These cannot live
// under the arxiv_id-keyed asset layout, so they get their own identity:
//
//   - storage:  paperassets.DOIAssetKey → "<kind>/doi/<reg>/<suffix>.<ext>"
//   - catalog:  a paper_works row whose primary key is the reserved
//               "doi:<doi>" namespace. Reusing the arxiv_id UNIQUE
//               primary key keeps the upsert atomic (same race-safety as
//               arxiv upserts) while the "doi:" prefix guarantees the
//               synthetic key can never collide with a real arxiv id.
//
// Besides the asset pointers we persist the DOI-metadata verification
// outcome (title/authors checked against OpenAlex) so the contribution
// is auditable — "was this PDF confirmed to be the paper it claims?".

import (
	"context"
	"errors"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/jackc/pgx/v5"
)

// Verification statuses recorded on DOI nodes (p.verification_status).
//
// Title / authors are NEVER taken from the contributor — they are always
// resolved from OpenAlex. The status records whether that resolution
// succeeded.
const (
	// VerifyVerified: OpenAlex returned a record for the DOI; Title /
	// Authors / ArxivID populated from the canonical metadata.
	VerifyVerified = "verified"
	// VerifyDOINotFound: OpenAlex confirmed the DOI does not exist.
	VerifyDOINotFound = "doi-not-found"
	// VerifyUnavailable: OpenAlex was unreachable / errored.
	VerifyUnavailable = "metadata-unavailable"
	// VerifyUnconfigured: the server has no OpenAlex mailto configured,
	// so DOI metadata enrichment is disabled.
	VerifyUnconfigured = "unconfigured"
)

// DOIVerification is the outcome of upload-time DOI metadata enrichment
// against OpenAlex, persisted on the catalog node. Title / Authors /
// ArxivID are populated only when Status == VerifyVerified; on every
// other status the catalog write must NOT clobber any previously-stored
// values (the DOI may have been verified by an earlier upload that
// caught a transient OpenAlex outage on the next).
type DOIVerification struct {
	Status  string   // one of the Verify* constants
	Title   string   // OpenAlex canonical title (only set on verified)
	Authors []string // OpenAlex author display names (only set on verified)
	ArxivID string   // linked arxiv id when OpenAlex knows one, else ""
}

// DOINodeKey returns the synthetic paper_works primary key for a DOI
// identity. Exported so handlers/tests can assert on the stored key.
func DOINodeKey(doi string) string { return "doi:" + doi }

// LookupDOI returns the catalog node's primary key (the synthetic
// "doi:<doi>" string) when a DOI contribution has been recorded
// against the given DOI. The three return modes are:
//
//   - (key, true, nil)  — DOI node found locally; caller dispatches
//     to the DOI handlers using `doi`.
//   - ("", false, nil)  — genuine miss (no row, or the catalog has
//     never been configured so ensure(ctx) short-circuits); caller
//     may fall through to OpenAlex resolution.
//   - ("", false, err) — PostgreSQL query-time error (connection drop,
//     failover mid-read, etc.). Caller MUST
//     return 503 — folding this into "not found" would have the
//     dispatcher serve a stale 404 (or worse, an arxiv twin) when
//     the local DOI bytes are in fact present, breaking the
//     DOI-canonical invariant.
//
// Used by the GET /api/papers/<id>/{pdf,markdown} read path: when
// the caller supplies a DOI, this is consulted FIRST — before any
// OpenAlex resolution — because DOI is the canonical identity for
// any work that has both an arxiv preprint and a DOI-only published
// version (see docs/reference/upload-api.md §Canonical resolution).
// `?force_arxiv=1` bypasses this lookup.
//
// The synthetic key matches the "<kind>/doi/<reg>/<suffix>" bucket
// layout used by UpsertPDFByDOI, so callers can hand it straight to
// the DOI handlers.
func (s *Store) LookupDOI(ctx context.Context, doi string) (string, bool, error) {
	if !s.ensure(ctx) {
		return "", false, nil
	}
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return "", false, nil
	}
	var nodeKey string
	err := s.pool.QueryRow(ctx, `
		SELECT arxiv_id
		FROM paper_works
		WHERE identifier_scheme = 'doi' AND doi = $1
		LIMIT 1`, norm).Scan(&nodeKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, catalogUnavailable(fmt.Sprintf("papers: lookup doi %s", norm), err)
	}
	return nodeKey, true, nil
}

// LookupArxivToDOI is the reverse direction of LookupDOI: given a bare
// (version-stripped) arxiv id, returns the DOI of any DOI-indexed node
// whose `doi_arxiv_id` matches. Used by the GET dispatch to honour the
// "DOI is canonical" rule even when the caller passed an arxiv id —
// when a DOI contribution exists for the same paper, default to
// serving the DOI bytes (caller can opt back with `?force_arxiv=1`).
//
// Caller MUST pass the BARE arxiv id (no `vN` suffix). DOI nodes store
// `doi_arxiv_id` as the version-stripped form returned by
// openalex.ExtractArxivID, so a versioned input would never match.
//
// Three return modes, mirroring LookupDOI:
//
//   - (doi, true, nil)  — a DOI twin exists; caller dispatches to
//     the DOI handlers.
//   - ("", false, nil)  — no twin, empty input, or catalog never
//     configured; caller falls through to the arxiv handlers.
//   - ("", false, err) — PostgreSQL query-time error. The arxiv path is
//     designed to be independent of the catalog (PostgreSQL outage MUST
//     NOT gate arxiv access), so the dispatcher logs-and-falls-
//     through here; the error is returned only so callers can
//     observe / log it instead of silently dropping the signal.
func (s *Store) LookupArxivToDOI(ctx context.Context, bareArxivID string) (string, bool, error) {
	if !s.ensure(ctx) {
		return "", false, nil
	}
	bareArxivID = paperassets.StripVersion(bareArxivID)
	if bareArxivID == "" {
		return "", false, nil
	}
	var doi string
	err := s.pool.QueryRow(ctx, `
		SELECT doi
		FROM paper_works
		WHERE identifier_scheme = 'doi'
		  AND doi_arxiv_id = $1
		LIMIT 1`, bareArxivID).Scan(&doi)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, catalogUnavailable(fmt.Sprintf("papers: lookup arxiv-to-doi %s", bareArxivID), err)
	}
	return doi, true, nil
}

// UpsertPDFByDOI is the DOI-indexed analogue of UpsertPDF: records a PDF
// contributed against a DOI (a published version that may have no arXiv
// preprint). Creates the node if missing, is idempotent, and stores the
// verification outcome. Returns ErrCatalogUnavailable when PostgreSQL is down
// (handler treats as deferred, object is already durably written).
//
// Metadata preservation: when the verification was non-verified (e.g.
// OpenAlex was transiently unavailable, or the DOI was not found), the
// CASE WHEN clauses below preserve any previously-stored title / authors
// / linked arxiv id — a transient outage during a re-upload must not
// silently overwrite a prior verified record. verification_status itself
// is always overwritten so the latest attempt is visible to operators.
func (s *Store) UpsertPDFByDOI(ctx context.Context, doi, sha string, size int64, etag string, v DOIVerification) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return fmt.Errorf("papers: upsert pdf by doi: invalid doi %q", doi)
	}
	pdfPath := bucketRelKey(paperassets.DOIAssetKey("pdf", norm))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO paper_works (
			arxiv_id, source, identifier_scheme, doi, doi_arxiv_id,
			has_pdf, has_md, has_json, pdf_path, pdf_size, pdf_sha256,
			pdf_etag, pdf_uploaded_at, last_assets_change_at,
			doi_title, doi_authors, verification_status, verified_at
		)
		VALUES ($1, 'doi-upload', 'doi', $2, nullif($3, ''),
		        true, false, false, $4, $5, $6, $7, now(), now(),
		        nullif($8, ''), $9, $10, now())
		ON CONFLICT (arxiv_id) DO UPDATE SET
			doi = EXCLUDED.doi,
			identifier_scheme = 'doi',
			doi_arxiv_id = coalesce(EXCLUDED.doi_arxiv_id, paper_works.doi_arxiv_id),
			has_pdf = true,
			pdf_path = EXCLUDED.pdf_path,
			pdf_size = EXCLUDED.pdf_size,
			pdf_sha256 = EXCLUDED.pdf_sha256,
			pdf_etag = EXCLUDED.pdf_etag,
			pdf_uploaded_at = now(),
			last_assets_change_at = now(),
			doi_title = coalesce(EXCLUDED.doi_title, paper_works.doi_title),
			doi_authors = CASE WHEN cardinality(EXCLUDED.doi_authors) > 0 THEN EXCLUDED.doi_authors ELSE paper_works.doi_authors END,
			verification_status = EXCLUDED.verification_status,
			verified_at = now()`,
		DOINodeKey(norm), norm, v.ArxivID, pdfPath, size, sha, etag, v.Title, v.Authors, v.Status)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert pdf by doi %s", norm), err)
	}
	return nil
}

// UpsertMDByDOI is the DOI-indexed analogue of UpsertMD: records a
// converted-PDF markdown bundle contributed against a DOI. Creates the
// node if missing, flips has_md=true, and stores the verification
// outcome. Idempotent.
//
// Metadata preservation: same CASE WHEN guard as UpsertPDFByDOI — a
// non-verified status (transient OpenAlex outage / doi-not-found) does
// not overwrite previously-stored title / authors / linked arxiv id.
func (s *Store) UpsertMDByDOI(ctx context.Context, doi, sha string, size int64, etag string, imageCount int, v DOIVerification) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return fmt.Errorf("papers: upsert md by doi: invalid doi %q", doi)
	}
	mdPath := bucketRelKey(paperassets.DOIAssetKey("markdown", norm))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO paper_works (
			arxiv_id, source, identifier_scheme, doi, doi_arxiv_id,
			has_pdf, has_md, has_json, md_path, md_size, md_sha256,
			md_etag, image_count, md_uploaded_at, last_assets_change_at,
			doi_title, doi_authors, verification_status, verified_at
		)
		VALUES ($1, 'doi-upload', 'doi', $2, nullif($3, ''),
		        false, true, false, $4, $5, $6, $7, $8, now(), now(),
		        nullif($9, ''), $10, $11, now())
		ON CONFLICT (arxiv_id) DO UPDATE SET
			doi = EXCLUDED.doi,
			identifier_scheme = 'doi',
			doi_arxiv_id = coalesce(EXCLUDED.doi_arxiv_id, paper_works.doi_arxiv_id),
			has_md = true,
			md_path = EXCLUDED.md_path,
			md_size = EXCLUDED.md_size,
			md_sha256 = EXCLUDED.md_sha256,
			md_etag = EXCLUDED.md_etag,
			image_count = EXCLUDED.image_count,
			md_uploaded_at = now(),
			last_assets_change_at = now(),
			doi_title = coalesce(EXCLUDED.doi_title, paper_works.doi_title),
			doi_authors = CASE WHEN cardinality(EXCLUDED.doi_authors) > 0 THEN EXCLUDED.doi_authors ELSE paper_works.doi_authors END,
			verification_status = EXCLUDED.verification_status,
			verified_at = now()`,
		DOINodeKey(norm), norm, v.ArxivID, mdPath, size, sha, etag, imageCount, v.Title, v.Authors, v.Status)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert md by doi %s", norm), err)
	}
	return nil
}
