package papers

// doi_store.go: catalog write-through for DOI-indexed contributions.
//
// A DOI contribution records a PDF / markdown for a *published* version
// of a paper, which may have no arXiv preprint at all. These cannot live
// under the arxiv_id-keyed asset layout, so they get their own identity:
//
//   - storage:  paperassets.DOIAssetKey → "<kind>/doi/<reg>/<suffix>.<ext>"
//   - catalog:  a :PaperWork node whose primary key is the reserved
//               "doi:<doi>" namespace. Reusing the arxiv_id UNIQUE
//               constraint keeps the MERGE atomic (same race-safety as
//               arxiv upserts) while the "doi:" prefix guarantees the
//               synthetic key can never collide with a real arxiv id.
//
// Besides the asset pointers we persist the DOI-metadata verification
// outcome (title/authors checked against OpenAlex) so the contribution
// is auditable — "was this PDF confirmed to be the paper it claims?".

import (
	"context"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
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

// DOINodeKey returns the synthetic :PaperWork primary key for a DOI
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
//   - ("", false, err) — Neo4j query-time error (driver dropped a
//     connection, cluster failover mid-read, etc.). Caller MUST
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
	rows, err := s.nc.ExecuteReadParams(ctx, `
		MATCH (p:PaperWork {doi: $doi})
		WHERE p.identifier_scheme = 'doi'
		RETURN p.arxiv_id AS arxiv_id
		LIMIT 1`, map[string]any{"doi": norm})
	if err != nil {
		return "", false, fmt.Errorf("papers: lookup doi %s: %w", norm, err)
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return asString(rows[0]["arxiv_id"]), true, nil
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
//   - ("", false, err) — Neo4j query-time error. The arxiv path is
//     designed to be independent of the catalog (Neo4j outage MUST
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
	rows, err := s.nc.ExecuteReadParams(ctx, `
		MATCH (p:PaperWork)
		WHERE p.identifier_scheme = 'doi'
		  AND p.doi_arxiv_id = $arxiv_id
		RETURN p.doi AS doi
		LIMIT 1`, map[string]any{"arxiv_id": bareArxivID})
	if err != nil {
		return "", false, fmt.Errorf("papers: lookup arxiv-to-doi %s: %w", bareArxivID, err)
	}
	if len(rows) == 0 {
		return "", false, nil
	}
	return asString(rows[0]["doi"]), true, nil
}

// UpsertPDFByDOI is the DOI-indexed analogue of UpsertPDF: records a PDF
// contributed against a DOI (a published version that may have no arXiv
// preprint). Creates the node if missing, is idempotent, and stores the
// verification outcome. Returns ErrCatalogUnavailable when Neo4j is down
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
	_, err := s.nc.ExecuteWrite(ctx, `
		MERGE (p:PaperWork {arxiv_id: $node_key})
		ON CREATE SET p.source = 'doi-upload',
		              p.identifier_scheme = 'doi',
		              p.has_md = false,
		              p.has_json = false
		SET p.doi = $doi,
		    p.identifier_scheme = 'doi',
		    p.doi_arxiv_id = CASE WHEN $arxiv_id <> '' THEN $arxiv_id ELSE p.doi_arxiv_id END,
		    p.has_pdf = true,
		    p.pdf_path = $pdf_path,
		    p.pdf_size = $size,
		    p.pdf_sha256 = $sha,
		    p.pdf_etag = $etag,
		    p.pdf_uploaded_at = datetime(),
		    p.last_assets_change_at = datetime(),
		    p.doi_title = CASE WHEN $title <> '' THEN $title ELSE p.doi_title END,
		    p.doi_authors = CASE WHEN size($authors) > 0 THEN $authors ELSE p.doi_authors END,
		    p.verification_status = $vstatus,
		    p.verified_at = datetime()
		RETURN p.arxiv_id`,
		map[string]any{
			"node_key": DOINodeKey(norm),
			"doi":      norm,
			"arxiv_id": v.ArxivID,
			"pdf_path": pdfPath,
			"size":     size,
			"sha":      sha,
			"etag":     etag,
			"title":    v.Title,
			"authors":  v.Authors,
			"vstatus":  v.Status,
		})
	if err != nil {
		return fmt.Errorf("papers: upsert pdf by doi %s: %w", norm, err)
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
	_, err := s.nc.ExecuteWrite(ctx, `
		MERGE (p:PaperWork {arxiv_id: $node_key})
		ON CREATE SET p.source = 'doi-upload',
		              p.identifier_scheme = 'doi',
		              p.has_pdf = false,
		              p.has_json = false
		SET p.doi = $doi,
		    p.identifier_scheme = 'doi',
		    p.doi_arxiv_id = CASE WHEN $arxiv_id <> '' THEN $arxiv_id ELSE p.doi_arxiv_id END,
		    p.has_md = true,
		    p.md_path = $md_path,
		    p.md_size = $size,
		    p.md_etag = $etag,
		    p.image_count = $image_count,
		    p.md_uploaded_at = datetime(),
		    p.last_assets_change_at = datetime(),
		    p.doi_title = CASE WHEN $title <> '' THEN $title ELSE p.doi_title END,
		    p.doi_authors = CASE WHEN size($authors) > 0 THEN $authors ELSE p.doi_authors END,
		    p.verification_status = $vstatus,
		    p.verified_at = datetime()
		RETURN p.arxiv_id`,
		map[string]any{
			"node_key":    DOINodeKey(norm),
			"doi":         norm,
			"arxiv_id":    v.ArxivID,
			"md_path":     mdPath,
			"size":        size,
			"sha":         sha,
			"etag":        etag,
			"image_count": int64(imageCount),
			"title":       v.Title,
			"authors":     v.Authors,
			"vstatus":     v.Status,
		})
	if err != nil {
		return fmt.Errorf("papers: upsert md by doi %s: %w", norm, err)
	}
	return nil
}
