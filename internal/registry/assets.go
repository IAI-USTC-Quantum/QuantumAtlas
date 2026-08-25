package registry

// assets.go: asset-side write/read operations ported from the legacy
// internal/papers catalog (store.go + doi_store.go). Every write that
// introduces a paper resolves it through ResolveOrMint — never a raw
// INSERT into papers — so the multi-identity merge machinery (including
// the twin-attach case: a DOI contribution that lands on an existing
// arXiv paper) applies uniformly.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// NeedsMineruRow projects one "PDF without markdown, not currently
// leased" arXiv asset, ordered by most-recent fetch. ArxivID is the
// full versioned id (bare papers.arxiv_id + the asset's version).
type NeedsMineruRow struct {
	PaperID       string
	ArxivID       string
	Version       int
	PDFPath       string
	PDFSizeBytes  int64
	PDFUploadedAt *time.Time
}

// NeedsMineru returns up to limit arXiv assets with a PDF but no
// markdown and no active lease, newest first.
func (s *Store) NeedsMineru(ctx context.Context, limit int) ([]NeedsMineruRow, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT a.paper_id, p.arxiv_id, a.arxiv_version, a.pdf_path, a.pdf_size, a.fetched_at
		FROM paper_assets a
		JOIN papers p ON p.paper_id = a.paper_id
		WHERE a.source = 'arxiv'
		  AND a.mineru_md_path IS NULL
		  AND (a.lease_expires_at IS NULL OR a.lease_expires_at < now())
		ORDER BY a.fetched_at DESC NULLS LAST, a.asset_id
		LIMIT $1`, limit)
	if err != nil {
		return nil, catalogUnavailable("registry: needs-mineru", err)
	}
	defer rows.Close()
	out := make([]NeedsMineruRow, 0, limit)
	for rows.Next() {
		var (
			bareID  *string
			pdfSize *int64
			row     NeedsMineruRow
		)
		if err := rows.Scan(&row.PaperID, &bareID, &row.Version, &row.PDFPath, &pdfSize, &row.PDFUploadedAt); err != nil {
			return nil, catalogUnavailable("registry: scan needs-mineru", err)
		}
		row.ArxivID = versionedArxivID(deref(bareID), row.Version)
		if pdfSize != nil {
			row.PDFSizeBytes = *pdfSize
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, catalogUnavailable("registry: iterate needs-mineru", err)
	}
	return out, nil
}

// UpsertPDF records an arXiv PDF for the given version. The paper is
// resolved (or minted) through ResolveOrMint from ref — ref.ArxivID may
// carry a version suffix, which only feeds the arxiv_version identity
// key; the asset row keys on the explicit version argument. On a
// successful asset upsert the paper's status flips to 'ready' (a
// 'pending'/'failed' paper with bytes on disk is hostable again;
// 'merged_into:*' rows are never touched because ResolveOrMint always
// returns the survivor). Idempotent. Returns the surrogate paper_id and
// the asset_id of the upserted row.
func (s *Store) UpsertPDF(ctx context.Context, ref PaperRef, version int, sha256 string, size int64, pdfPath string) (paperID string, assetID int64, err error) {
	if !s.ensure(ctx) {
		return "", 0, ErrCatalogUnavailable
	}
	if version <= 0 {
		return "", 0, fmt.Errorf("registry: upsert pdf: arxiv version must be positive, got %d", version)
	}
	paperID, _, err = s.ResolveOrMint(ctx, ref)
	if err != nil {
		return "", 0, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO paper_assets (paper_id, source, arxiv_version, pdf_path, pdf_size, pdf_sha256, fetched_at)
		VALUES ($1, 'arxiv', $2, $3, $4, nullif($5, ''), now())
		ON CONFLICT (paper_id, source, arxiv_version) DO UPDATE SET
			pdf_path = EXCLUDED.pdf_path,
			pdf_size = EXCLUDED.pdf_size,
			pdf_sha256 = EXCLUDED.pdf_sha256,
			fetched_at = now()
		RETURNING asset_id`,
		paperID, version, pdfPath, size, sha256).Scan(&assetID)
	if err != nil {
		return "", 0, catalogUnavailable("registry: upsert pdf "+paperID, err)
	}
	if err := s.markReady(ctx, paperID); err != nil {
		return "", 0, err
	}
	return paperID, assetID, nil
}

// UpsertMD records the MinerU markdown bundle for one arXiv asset of an
// already-resolved paper, and clears any lease on that asset (markdown
// done ⇒ lease no longer needed). image_count is recorded alongside the
// md/json pointers. When no asset row exists yet (markdown arrived
// before the PDF write-through) one is inserted with mdPath as the
// pdf_path placeholder — the same md-only-contribution fallback the
// legacy catalog used. Idempotent.
func (s *Store) UpsertMD(ctx context.Context, paperID string, version int, sha256 string, size int64, mdPath, jsonPath string, imageCount int) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	if version <= 0 {
		return fmt.Errorf("registry: upsert md: arxiv version must be positive, got %d", version)
	}
	// sha256 / size are part of the caller contract (upload verification
	// happens before the catalog write) but, as in the legacy catalog,
	// are not persisted on the md row.
	_, _ = sha256, size
	_, err := s.pool.Exec(ctx, `
		INSERT INTO paper_assets (paper_id, source, arxiv_version, pdf_path, mineru_md_path, mineru_json_path, image_count, fetched_at)
		VALUES ($1, 'arxiv', $2, $3, $3, $4, $5, now())
		ON CONFLICT (paper_id, source, arxiv_version) DO UPDATE SET
			mineru_md_path = EXCLUDED.mineru_md_path,
			mineru_json_path = EXCLUDED.mineru_json_path,
			image_count = EXCLUDED.image_count,
			fetched_at = now(),
			lease_id = NULL,
			lease_holder = NULL,
			lease_expires_at = NULL`,
		paperID, version, mdPath, jsonPath, imageCount)
	if err != nil {
		return catalogUnavailable("registry: upsert md "+paperID, err)
	}
	return nil
}

// UpsertPDFByDOI records a PDF contributed against a DOI (the published
// version). ref must carry DOI; when ref also carries the OpenAlex-linked
// ArxivID of a paper we already host, ResolveOrMint resolves (and, if a
// separate DOI twin exists, merges) so the published asset attaches to
// the one surviving paper — the legacy resolveDOIPaper three-way
// resolution is entirely covered by the merge machinery. Idempotent.
func (s *Store) UpsertPDFByDOI(ctx context.Context, ref PaperRef, sha256 string, size int64, pdfPath string) (paperID string, assetID int64, err error) {
	if !s.ensure(ctx) {
		return "", 0, ErrCatalogUnavailable
	}
	norm, ok := paperassets.ValidateDOI(ref.DOI)
	if !ok {
		return "", 0, fmt.Errorf("registry: upsert pdf by doi: invalid doi %q", ref.DOI)
	}
	ref.DOI = norm
	paperID, _, err = s.ResolveOrMint(ctx, ref)
	if err != nil {
		return "", 0, err
	}
	err = s.pool.QueryRow(ctx, `
		INSERT INTO paper_assets (paper_id, source, pdf_path, pdf_size, pdf_sha256, fetched_at)
		VALUES ($1, 'published', $2, $3, nullif($4, ''), now())
		ON CONFLICT (paper_id) WHERE source = 'published' DO UPDATE SET
			pdf_path = EXCLUDED.pdf_path,
			pdf_size = EXCLUDED.pdf_size,
			pdf_sha256 = EXCLUDED.pdf_sha256,
			fetched_at = now()
		RETURNING asset_id`,
		paperID, pdfPath, size, sha256).Scan(&assetID)
	if err != nil {
		return "", 0, catalogUnavailable("registry: upsert pdf by doi "+norm, err)
	}
	if err := s.markReady(ctx, paperID); err != nil {
		return "", 0, err
	}
	return paperID, assetID, nil
}

// UpsertMDByDOI records a converted-PDF markdown bundle contributed
// against a DOI. Resolution is identical to UpsertPDFByDOI; the md/json
// pointers + image count upsert onto the paper's single published asset.
// A markdown-only contribution still needs a published asset to hang off
// (pdf_path is NOT NULL), so the DOI-keyed PDF storage path is recorded
// as the asset's PDF pointer, as in the legacy catalog. Idempotent.
func (s *Store) UpsertMDByDOI(ctx context.Context, ref PaperRef, sha256 string, size int64, mdPath, jsonPath string, imageCount int) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	norm, ok := paperassets.ValidateDOI(ref.DOI)
	if !ok {
		return fmt.Errorf("registry: upsert md by doi: invalid doi %q", ref.DOI)
	}
	ref.DOI = norm
	_, _ = sha256, size
	paperID, _, err := s.ResolveOrMint(ctx, ref)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO paper_assets (paper_id, source, pdf_path, mineru_md_path, mineru_json_path, image_count, fetched_at)
		VALUES ($1, 'published', $2, $3, $4, $5, now())
		ON CONFLICT (paper_id) WHERE source = 'published' DO UPDATE SET
			mineru_md_path = EXCLUDED.mineru_md_path,
			mineru_json_path = EXCLUDED.mineru_json_path,
			image_count = EXCLUDED.image_count,
			fetched_at = now()`,
		paperID, bucketRelKey(paperassets.DOIAssetKey("pdf", norm)), mdPath, jsonPath, imageCount)
	if err != nil {
		return catalogUnavailable("registry: upsert md by doi "+norm, err)
	}
	return nil
}

// markReady flips a 'pending'/'failed' paper to 'ready' after a
// successful asset upsert. Merged losers (status 'merged_into:*') are
// excluded explicitly so a stray write can never resurrect one.
func (s *Store) markReady(ctx context.Context, paperID string) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE papers SET status = 'ready', updated_at = now()
		WHERE paper_id = $1 AND status IN ('pending', 'failed')`, paperID); err != nil {
		return catalogUnavailable("registry: mark ready "+paperID, err)
	}
	return nil
}

// versionedArxivID reconstructs the full versioned id from a bare arXiv
// id + integer version, e.g. ("2208.06941", 2) → "2208.06941v2". A zero
// version (or empty bare id) yields the bare id unchanged.
func versionedArxivID(bare string, version int) string {
	if bare == "" || version <= 0 {
		return bare
	}
	return bare + "v" + strconv.Itoa(version)
}

// bucketRelKey strips the leading "<kind>/" segment from an AssetKey,
// yielding the object key relative to a per-kind bucket, e.g.
// "pdf/9508/9508027.pdf" -> "9508/9508027.pdf".
func bucketRelKey(assetKey string) string {
	if i := strings.IndexByte(assetKey, '/'); i >= 0 {
		return assetKey[i+1:]
	}
	return assetKey
}
