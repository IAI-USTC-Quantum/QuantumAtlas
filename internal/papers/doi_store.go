package papers

// doi_store.go: catalog write-through for DOI-indexed contributions.
//
// A DOI contribution records a PDF / markdown for the *published* version
// of a paper. In the surrogate-key model a DOI upload resolves to a single
// papers row and a paper_assets row with source='published':
//
//   - when OpenAlex links the DOI to an arXiv id we already host, the
//     published asset attaches to that existing paper (one work, an arXiv
//     asset + a published asset — ADR 0009);
//   - otherwise a paper keyed on paper_doi is created / reused.
//
// Storage keys are DOI-derived (paperassets.DOIAssetKey -> "<kind>/doi/
// <reg>/<suffix>.<ext>"). The OpenAlex-verified title lands in
// paper_title and the verification outcome in paper_verification_status;
// authors are not snapshotted (they resolve from the OpenAlex corpus).

import (
	"context"
	"errors"
	"fmt"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/jackc/pgx/v5"
)

// Verification statuses recorded on a DOI contribution
// (papers.paper_verification_status).
//
// Title is taken from OpenAlex, never the contributor; the status records
// whether that resolution succeeded.
const (
	// VerifyVerified: OpenAlex returned a record for the DOI; Title /
	// ArxivID populated from the canonical metadata.
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
// against OpenAlex. Title / Authors / ArxivID are populated only when
// Status == VerifyVerified. Authors is used only for the HTTP response
// body (it is not persisted -- authors resolve from the OpenAlex corpus).
type DOIVerification struct {
	Status  string   // one of the Verify* constants
	Title   string   // OpenAlex canonical title (only set on verified)
	Authors []string // OpenAlex author display names (response body only)
	ArxivID string   // linked arxiv id when OpenAlex knows one, else ""
}

// LookupDOI reports whether a DOI contribution has been recorded for the
// given DOI. The three return modes are:
//
//   - (doi, true, nil)  -- a paper with this DOI exists; caller dispatches
//     to the DOI handlers.
//   - ("", false, nil)  -- genuine miss (no row, or catalog unconfigured).
//   - ("", false, err)  -- PostgreSQL query-time error; caller MUST 503.
//
// Used by the GET /api/papers/<id>/{pdf,markdown} read path: when the
// caller supplies a DOI this is consulted FIRST -- before any OpenAlex
// resolution -- because DOI is the canonical identity for any work that
// has both an arxiv preprint and a DOI-only published version.
func (s *Store) LookupDOI(ctx context.Context, doi string) (string, bool, error) {
	if !s.ensure(ctx) {
		return "", false, nil
	}
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return "", false, nil
	}
	var found string
	err := s.pool.QueryRow(ctx, `
		SELECT paper_doi FROM papers WHERE lower(paper_doi) = lower($1) LIMIT 1`, norm,
	).Scan(&found)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, catalogUnavailable(fmt.Sprintf("papers: lookup doi %s", norm), err)
	}
	return found, true, nil
}

// LookupArxivToDOI is the reverse of LookupDOI: given a bare arxiv id,
// returns the DOI of the same paper when it also carries a published DOI
// contribution. Used by the GET dispatch to honour "DOI is canonical" --
// when a DOI twin exists for the requested arxiv id, default to serving
// the DOI bytes (caller can opt back with ?force_arxiv=1).
//
// Caller MUST pass the BARE arxiv id (no vN suffix).
//
//   - (doi, true, nil)  -- a DOI twin exists.
//   - ("", false, nil)  -- no twin, empty input, or catalog unconfigured.
//   - ("", false, err)  -- PostgreSQL query-time error.
func (s *Store) LookupArxivToDOI(ctx context.Context, bareArxiv string) (string, bool, error) {
	if !s.ensure(ctx) {
		return "", false, nil
	}
	bare := bareArxivID(bareArxiv)
	if bare == "" {
		return "", false, nil
	}
	var doi string
	err := s.pool.QueryRow(ctx, `
		SELECT paper_doi FROM papers
		WHERE paper_arxiv_id = $1 AND paper_doi IS NOT NULL
		LIMIT 1`, bare,
	).Scan(&doi)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", false, nil
		}
		return "", false, catalogUnavailable(fmt.Sprintf("papers: lookup arxiv-to-doi %s", bare), err)
	}
	return doi, true, nil
}

// resolveDOIPaper resolves (inside tx) the papers row a DOI contribution
// belongs to, creating or updating it, and returns its paper_id. The
// three-way resolution keeps one work as one paper (ADR 0009):
//
//  1. OpenAlex linked an arXiv id we host -> attach to that paper (set its
//     paper_doi + verification), so the arXiv preprint and the published
//     version share one paper.
//  2. else a paper with this DOI exists -> reuse it.
//  3. else create a new paper keyed on the DOI (+ the linked arXiv id when
//     it is free).
func resolveDOIPaper(ctx context.Context, tx pgx.Tx, doi string, v DOIVerification) (int64, error) {
	title := nilIfEmpty(v.Title)
	status := nilIfEmpty(v.Status)
	linkedArxiv := ""
	if v.ArxivID != "" {
		linkedArxiv = bareArxivID(v.ArxivID)
	}

	if linkedArxiv != "" {
		var pid int64
		err := tx.QueryRow(ctx, `
			UPDATE papers SET
				paper_doi = $2,
				paper_title = coalesce($3, paper_title),
				paper_verification_status = coalesce($4, paper_verification_status)
			WHERE paper_arxiv_id = $1
			RETURNING paper_id`, linkedArxiv, doi, title, status,
		).Scan(&pid)
		if err == nil {
			return pid, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return 0, err
		}
	}

	var pid int64
	err := tx.QueryRow(ctx, `
		INSERT INTO papers (paper_doi, paper_arxiv_id, paper_title, paper_verification_status)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (paper_doi) DO UPDATE SET
			paper_arxiv_id = coalesce(papers.paper_arxiv_id, EXCLUDED.paper_arxiv_id),
			paper_title = coalesce(EXCLUDED.paper_title, papers.paper_title),
			paper_verification_status = coalesce(EXCLUDED.paper_verification_status, papers.paper_verification_status)
		RETURNING paper_id`,
		doi, nilIfEmpty(linkedArxiv), title, status,
	).Scan(&pid)
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// UpsertPDFByDOI records a PDF contributed against a DOI (the published
// version). Resolves/creates the paper, then upserts its single published
// asset. Idempotent. Returns ErrCatalogUnavailable when PostgreSQL is down.
func (s *Store) UpsertPDFByDOI(ctx context.Context, doi, sha string, size int64, v DOIVerification) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return fmt.Errorf("papers: upsert pdf by doi: invalid doi %q", doi)
	}
	pdfPath := bucketRelKey(paperassets.DOIAssetKey("pdf", norm))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert pdf by doi begin %s", norm), err)
	}
	defer tx.Rollback(ctx)
	pid, err := resolveDOIPaper(ctx, tx, norm, v)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: resolve doi paper %s", norm), err)
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO paper_assets (paper_id, source, pdf_path, pdf_size, pdf_sha256, fetched_at)
		VALUES ($1, 'published', $2, $3, nullif($4,''), now())
		ON CONFLICT (paper_id) WHERE source = 'published' DO UPDATE SET
			pdf_path = EXCLUDED.pdf_path,
			pdf_size = EXCLUDED.pdf_size,
			pdf_sha256 = EXCLUDED.pdf_sha256,
			fetched_at = now()`,
		pid, pdfPath, size, sha)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert pdf asset by doi %s", norm), err)
	}
	if err := tx.Commit(ctx); err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert pdf by doi commit %s", norm), err)
	}
	return nil
}

// UpsertMDByDOI records a converted-PDF markdown bundle contributed
// against a DOI. Resolves/creates the paper, then upserts the MinerU
// pointers + image count on its published asset. Idempotent.
func (s *Store) UpsertMDByDOI(ctx context.Context, doi, sha string, size int64, imageCount int, v DOIVerification) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	norm, ok := paperassets.ValidateDOI(doi)
	if !ok {
		return fmt.Errorf("papers: upsert md by doi: invalid doi %q", doi)
	}
	mdPath := bucketRelKey(paperassets.DOIAssetKey("markdown", norm))
	jsonPath := bucketRelKey(paperassets.DOIAssetKey("json", norm))
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert md by doi begin %s", norm), err)
	}
	defer tx.Rollback(ctx)
	pid, err := resolveDOIPaper(ctx, tx, norm, v)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: resolve doi paper %s", norm), err)
	}
	// A markdown-only contribution still needs a published asset to hang
	// off; pdf_path is NOT NULL, so we record the DOI PDF key as the
	// asset's PDF pointer (the PDF lives in the same DOI-keyed layout).
	_, err = tx.Exec(ctx, `
		INSERT INTO paper_assets (paper_id, source, pdf_path, mineru_md_path, mineru_json_path, image_count, fetched_at)
		VALUES ($1, 'published', $2, $3, $4, $5, now())
		ON CONFLICT (paper_id) WHERE source = 'published' DO UPDATE SET
			mineru_md_path = EXCLUDED.mineru_md_path,
			mineru_json_path = EXCLUDED.mineru_json_path,
			image_count = EXCLUDED.image_count,
			fetched_at = now()`,
		pid, bucketRelKey(paperassets.DOIAssetKey("pdf", norm)), mdPath, jsonPath, imageCount)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert md asset by doi %s", norm), err)
	}
	if err := tx.Commit(ctx); err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert md by doi commit %s", norm), err)
	}
	return nil
}

// nilIfEmpty maps "" to a nil *string so it encodes as SQL NULL.
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
