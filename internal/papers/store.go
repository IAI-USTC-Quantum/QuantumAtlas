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
// degrade to {available:false}. Counts are over papers that have an arXiv
// asset (the arXiv-paper dashboard); published-only DOI contributions do
// not pollute the counters.
func (s *Store) QueryStats(ctx context.Context) (Stats, error) {
	var st Stats
	if !s.ensure(ctx) {
		return st, ErrCatalogUnavailable
	}
	var total, hasPDF, hasMD, needsMineru, totalImages int64
	err := s.pool.QueryRow(ctx, `
		WITH arxiv_assets AS (
			SELECT paper_id, pdf_path, mineru_md_path, image_count, lease_expires_at
			FROM paper_assets
			WHERE source = 'arxiv'
		)
		SELECT
		  count(DISTINCT paper_id)::bigint AS total,
		  count(DISTINCT paper_id) FILTER (WHERE pdf_path IS NOT NULL)::bigint AS has_pdf,
		  count(DISTINCT paper_id) FILTER (WHERE mineru_md_path IS NOT NULL)::bigint AS has_md,
		  count(DISTINCT paper_id) FILTER (
		      WHERE pdf_path IS NOT NULL AND mineru_md_path IS NULL
		        AND (lease_expires_at IS NULL OR lease_expires_at < now())
		  )::bigint AS needs_mineru,
		  coalesce(sum(image_count), 0)::bigint AS total_images
		FROM arxiv_assets`,
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
// leased" asset, ordered by most-recent fetch.
type NeedsMineruRow struct {
	ArxivID       string
	YYMM          string
	PDFKey        string // "pdf/<yymm>/<stem>.pdf" (AssetKey form)
	PDFSizeBytes  int64
	PDFUploadedAt *time.Time
}

// NeedsMineru returns up to limit arXiv assets with a PDF but no markdown
// and no active lease, newest first. The reconstructed ArxivID pairs the
// paper's bare arxiv id with the asset's version.
func (s *Store) NeedsMineru(ctx context.Context, limit int) ([]NeedsMineruRow, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.paper_arxiv_id, a.arxiv_version, a.pdf_path, a.pdf_size, a.fetched_at
		FROM paper_assets a
		JOIN papers p ON p.paper_id = a.paper_id
		WHERE a.source = 'arxiv'
		  AND a.pdf_path IS NOT NULL
		  AND a.mineru_md_path IS NULL
		  AND (a.lease_expires_at IS NULL OR a.lease_expires_at < now())
		ORDER BY a.fetched_at DESC NULLS LAST, a.asset_id
		LIMIT $1`, limit)
	if err != nil {
		return nil, catalogUnavailable("papers: needs-mineru", err)
	}
	defer rows.Close()
	out := make([]NeedsMineruRow, 0, limit)
	for rows.Next() {
		var (
			bareID  string
			version int
			pdfPath *string
			pdfSize *int64
			row     NeedsMineruRow
		)
		if err := rows.Scan(&bareID, &version, &pdfPath, &pdfSize, &row.PDFUploadedAt); err != nil {
			return nil, catalogUnavailable("papers: scan needs-mineru", err)
		}
		row.ArxivID = versionedArxivID(bareID, version)
		row.YYMM = paperassets.Shard(paperassets.StorageKey(bareID))
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

// UpsertPDF write-through: ensures the papers row (keyed on the bare arxiv
// id) and the arxiv paper_assets row for this version, recording the PDF
// pointers. Idempotent. Returns ErrCatalogUnavailable when PostgreSQL is
// down (handler treats as deferred).
func (s *Store) UpsertPDF(ctx context.Context, arxivID, sha string, size int64) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	bare := bareArxivID(arxivID)
	version := arxivVersion(arxivID)
	pdfPath := bucketRelKey(paperassets.AssetKey("pdf", deriveIDs(arxivID).ArxivID))
	_, err := s.pool.Exec(ctx, `
		WITH p AS (
			INSERT INTO papers (paper_arxiv_id) VALUES ($1)
			ON CONFLICT (paper_arxiv_id) DO UPDATE SET paper_arxiv_id = EXCLUDED.paper_arxiv_id
			RETURNING paper_id
		)
		INSERT INTO paper_assets (paper_id, source, arxiv_version, pdf_path, pdf_size, pdf_sha256, fetched_at)
		SELECT paper_id, 'arxiv', $2, $3, $4, nullif($5,''), now() FROM p
		ON CONFLICT (paper_id, source, arxiv_version) DO UPDATE SET
			pdf_path = EXCLUDED.pdf_path,
			pdf_size = EXCLUDED.pdf_size,
			pdf_sha256 = EXCLUDED.pdf_sha256,
			fetched_at = now()`,
		bare, version, pdfPath, size, sha)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert pdf %s", bare), err)
	}
	return nil
}

// UpsertMD write-through for markdown. Ensures the papers row + the arxiv
// asset for this version, records the MinerU md/json pointers, and clears
// any active lease on that asset (markdown done ⇒ lease no longer needed).
// Idempotent.
func (s *Store) UpsertMD(ctx context.Context, arxivID, sha string, size int64) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	bare := bareArxivID(arxivID)
	version := arxivVersion(arxivID)
	full := deriveIDs(arxivID).ArxivID
	mdPath := bucketRelKey(paperassets.AssetKey("markdown", full))
	jsonPath := bucketRelKey(paperassets.AssetKey("json", full))
	_, err := s.pool.Exec(ctx, `
		WITH p AS (
			INSERT INTO papers (paper_arxiv_id) VALUES ($1)
			ON CONFLICT (paper_arxiv_id) DO UPDATE SET paper_arxiv_id = EXCLUDED.paper_arxiv_id
			RETURNING paper_id
		)
		INSERT INTO paper_assets (paper_id, source, arxiv_version, pdf_path, mineru_md_path, mineru_json_path, fetched_at)
		SELECT paper_id, 'arxiv', $2, $3, $4, $5, now() FROM p
		ON CONFLICT (paper_id, source, arxiv_version) DO UPDATE SET
			mineru_md_path = EXCLUDED.mineru_md_path,
			mineru_json_path = EXCLUDED.mineru_json_path,
			fetched_at = now(),
			lease_id = NULL,
			lease_holder = NULL,
			lease_expires_at = NULL`,
		bare, version, mdPath, mdPath, jsonPath)
	if err != nil {
		return catalogUnavailable(fmt.Sprintf("papers: upsert md %s", bare), err)
	}
	return nil
}
