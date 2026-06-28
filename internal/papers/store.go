package papers

import (
	"context"
	"fmt"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// Stats mirrors the legacy paperindex.Stats shape so /api/papers/stats
// keeps the same JSON contract. HasJSON / NeedsJSON are always zero in
// v0.7.0 (the json bucket was cut) but retained for wire compatibility.
type Stats struct {
	Total       int
	HasPDF      int
	HasMD       int
	HasJSON     int
	NeedsMineru int
	NeedsJSON   int
	TotalImages int
	LoadedAt    time.Time
}

// QueryStats returns aggregate catalog counters. Returns
// ErrCatalogUnavailable when PostgreSQL is unreachable so the handler can
// degrade to {available:false}. Excludes DOI-indexed nodes (those with
// identifier_scheme='doi') so published-version contributions don't
// pollute the arxiv-paper dashboard counts. (PR #19 follow-up.)
func (s *Store) QueryStats(ctx context.Context) (Stats, error) {
	var st Stats
	if !s.ensure(ctx) {
		return st, ErrCatalogUnavailable
	}
	var total, hasPDF, hasMD, needsMineru, totalImages int64
	err := s.pool.QueryRow(ctx, `
		SELECT
		  count(*)::bigint AS total,
		  count(*) FILTER (WHERE has_pdf)::bigint AS has_pdf,
		  count(*) FILTER (WHERE has_md)::bigint AS has_md,
		  count(*) FILTER (WHERE has_pdf AND NOT has_md)::bigint AS needs_mineru,
		  coalesce(sum(image_count), 0)::bigint AS total_images
		FROM paper_works
		WHERE identifier_scheme <> 'doi'`,
	).Scan(&total, &hasPDF, &hasMD, &needsMineru, &totalImages)
	if err != nil {
		return st, catalogUnavailable("papers: query stats", err)
	}
	st.Total = int(total)
	st.HasPDF = int(hasPDF)
	st.HasMD = int(hasMD)
	st.NeedsMineru = int(needsMineru)
	st.TotalImages = int(totalImages)
	st.LoadedAt = time.Now().UTC()
	return st, nil
}

// NeedsMineruRow projects one "PDF without markdown, not currently
// claimed" paper, ordered by most-recent upload.
type NeedsMineruRow struct {
	ArxivID       string
	YYMM          string
	PDFKey        string // "pdf/<yymm>/<stem>.pdf" (AssetKey form)
	PDFSizeBytes  int64
	PDFUploadedAt *time.Time
}

// NeedsMineru returns up to limit papers with a PDF but no markdown and
// no active claim. The claim filter lives in the same query (claims are
// inlined on the node), so callers don't need a separate claim store.
func (s *Store) NeedsMineru(ctx context.Context, limit int) ([]NeedsMineruRow, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT arxiv_id, yymm, pdf_path, pdf_size, pdf_uploaded_at
		FROM paper_works
		WHERE has_pdf
		  AND NOT has_md
		  AND (claim_expires_at IS NULL OR claim_expires_at < now())
		  AND identifier_scheme <> 'doi'
		ORDER BY pdf_uploaded_at DESC NULLS LAST, arxiv_id
		LIMIT $1`, limit)
	if err != nil {
		return nil, catalogUnavailable("papers: needs-mineru", err)
	}
	defer rows.Close()
	out := make([]NeedsMineruRow, 0, limit)
	for rows.Next() {
		var row NeedsMineruRow
		var pdfPath *string
		var pdfSize *int64
		if err := rows.Scan(&row.ArxivID, &row.YYMM, &pdfPath, &pdfSize, &row.PDFUploadedAt); err != nil {
			return nil, catalogUnavailable("papers: scan needs-mineru", err)
		}
		if pdfPath != nil && *pdfPath != "" {
			row.PDFKey = "pdf/" + *pdfPath
		}
		if pdfSize != nil {
			row.PDFSizeBytes = *pdfSize
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, catalogUnavailable("papers: iterate needs-mineru", err)
	}
	return out, nil
}

// UpsertPDF write-through: creates the paper_works row if missing
// (source='arxiv-fallback') and flips has_pdf=true with the asset
// pointers. Idempotent. Returns ErrCatalogUnavailable when PostgreSQL is
// down (handler treats as deferred).
func (s *Store) UpsertPDF(ctx context.Context, arxivID, sha string, size int64, etag string) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	id := deriveIDs(arxivID)
	pdfPath := bucketRelKey(paperassets.AssetKey("pdf", id.ArxivID))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO paper_works (
			arxiv_id, source, identifier_scheme, arxiv_id_canonical, yymm,
			has_pdf, has_md, has_json, pdf_path, pdf_size, pdf_sha256,
			pdf_etag, pdf_uploaded_at, last_assets_change_at
		)
		VALUES ($1, 'arxiv-fallback', 'arxiv', $2, $3, true, false, false,
		        $4, $5, $6, $7, now(), now())
		ON CONFLICT (arxiv_id) DO UPDATE SET
			arxiv_id_canonical = coalesce(paper_works.arxiv_id_canonical, EXCLUDED.arxiv_id_canonical),
			yymm = coalesce(paper_works.yymm, EXCLUDED.yymm),
			has_pdf = true,
			pdf_path = EXCLUDED.pdf_path,
			pdf_size = EXCLUDED.pdf_size,
			pdf_sha256 = EXCLUDED.pdf_sha256,
			pdf_etag = EXCLUDED.pdf_etag,
			pdf_uploaded_at = now(),
			last_assets_change_at = now()`,
		id.ArxivID, id.Canonical, id.YYMM, pdfPath, size, sha, etag)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert pdf %s", id.ArxivID), err)
	}
	return nil
}

// UpsertMD write-through for markdown. Creates the node if missing,
// flips has_md=true, and clears any active claim (markdown done ⇒ lease
// no longer needed). Idempotent.
func (s *Store) UpsertMD(ctx context.Context, arxivID, sha string, size int64, etag string) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	id := deriveIDs(arxivID)
	mdPath := bucketRelKey(paperassets.AssetKey("markdown", id.ArxivID))
	_, err := s.pool.Exec(ctx, `
		INSERT INTO paper_works (
			arxiv_id, source, identifier_scheme, arxiv_id_canonical, yymm,
			has_pdf, has_md, has_json, md_path, md_size, md_sha256,
			md_etag, md_uploaded_at, last_assets_change_at
		)
		VALUES ($1, 'arxiv-fallback', 'arxiv', $2, $3, false, true, false,
		        $4, $5, $6, $7, now(), now())
		ON CONFLICT (arxiv_id) DO UPDATE SET
			arxiv_id_canonical = coalesce(paper_works.arxiv_id_canonical, EXCLUDED.arxiv_id_canonical),
			yymm = coalesce(paper_works.yymm, EXCLUDED.yymm),
			has_md = true,
			md_path = EXCLUDED.md_path,
			md_size = EXCLUDED.md_size,
			md_sha256 = EXCLUDED.md_sha256,
			md_etag = EXCLUDED.md_etag,
			md_uploaded_at = now(),
			last_assets_change_at = now(),
			claimed_by_login = NULL,
			claim_expires_at = NULL,
			claim_id = NULL`,
		id.ArxivID, id.Canonical, id.YYMM, mdPath, size, sha, etag)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert md %s", id.ArxivID), err)
	}
	return nil
}
