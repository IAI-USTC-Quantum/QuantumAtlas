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
const (
	// VerifyVerified: caller supplied a title/authors that matched the
	// DOI's OpenAlex metadata.
	VerifyVerified = "verified"
	// VerifyMismatch: caller-supplied title/authors did NOT match.
	VerifyMismatch = "mismatch"
	// VerifyRecorded: OpenAlex metadata was fetched and stored, but the
	// caller supplied nothing to cross-check against.
	VerifyRecorded = "recorded"
	// VerifyDOINotFound: OpenAlex has no record for this DOI.
	VerifyDOINotFound = "doi-not-found"
	// VerifyUnavailable: OpenAlex was unreachable / errored.
	VerifyUnavailable = "metadata-unavailable"
	// VerifyUnconfigured: the server has no OpenAlex mailto configured,
	// so DOI metadata verification is disabled.
	VerifyUnconfigured = "unconfigured"
)

// DOIVerification is the outcome of upload-time title/author cross-check
// against a DOI's OpenAlex metadata, persisted on the catalog node.
type DOIVerification struct {
	Status  string   // one of the Verify* constants
	Title   string   // OpenAlex canonical title (may be empty)
	Authors []string // OpenAlex author display names
	ArxivID string   // linked arxiv id when OpenAlex knows one, else ""
}

// DOINodeKey returns the synthetic :PaperWork primary key for a DOI
// identity. Exported so handlers/tests can assert on the stored key.
func DOINodeKey(doi string) string { return "doi:" + doi }

// UpsertPDFByDOI is the DOI-indexed analogue of UpsertPDF: records a PDF
// contributed against a DOI (a published version that may have no arXiv
// preprint). Creates the node if missing, is idempotent, and stores the
// verification outcome. Returns ErrCatalogUnavailable when Neo4j is down
// (handler treats as deferred, object is already durably written).
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
		    p.doi_arxiv_id = $arxiv_id,
		    p.has_pdf = true,
		    p.pdf_path = $pdf_path,
		    p.pdf_size = $size,
		    p.pdf_sha256 = $sha,
		    p.pdf_etag = $etag,
		    p.pdf_uploaded_at = datetime(),
		    p.last_assets_change_at = datetime(),
		    p.doi_title = $title,
		    p.doi_authors = $authors,
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
		    p.doi_arxiv_id = $arxiv_id,
		    p.has_md = true,
		    p.md_path = $md_path,
		    p.md_size = $size,
		    p.md_etag = $etag,
		    p.image_count = $image_count,
		    p.md_uploaded_at = datetime(),
		    p.last_assets_change_at = datetime(),
		    p.doi_title = $title,
		    p.doi_authors = $authors,
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
