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
// ErrCatalogUnavailable when Neo4j is unreachable so the handler can
// degrade to {available:false}.
func (s *Store) QueryStats(ctx context.Context) (Stats, error) {
	var st Stats
	if !s.ensure(ctx) {
		return st, ErrCatalogUnavailable
	}
	rows, err := s.nc.ExecuteReadParams(ctx, `
		MATCH (p:PaperWork)
		RETURN
		  count(p) AS total,
		  count(CASE WHEN p.has_pdf = true THEN 1 END) AS has_pdf,
		  count(CASE WHEN p.has_md = true THEN 1 END) AS has_md,
		  count(CASE WHEN p.has_pdf = true AND coalesce(p.has_md, false) = false THEN 1 END) AS needs_mineru,
		  coalesce(sum(p.image_count), 0) AS total_images`, nil)
	if err != nil {
		return st, fmt.Errorf("papers: query stats: %w", err)
	}
	if len(rows) == 0 {
		st.LoadedAt = time.Now().UTC()
		return st, nil
	}
	r := rows[0]
	st.Total = asInt(r["total"])
	st.HasPDF = asInt(r["has_pdf"])
	st.HasMD = asInt(r["has_md"])
	st.NeedsMineru = asInt(r["needs_mineru"])
	st.TotalImages = asInt(r["total_images"])
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
	rows, err := s.nc.ExecuteReadParams(ctx, `
		MATCH (p:PaperWork)
		WHERE p.has_pdf = true AND coalesce(p.has_md, false) = false
		  AND (p.claim_expires_at IS NULL OR p.claim_expires_at < datetime())
		  AND (p.identifier_scheme IS NULL OR p.identifier_scheme <> 'doi')
		RETURN p.arxiv_id AS arxiv_id, p.yymm AS yymm,
		       p.pdf_path AS pdf_path, p.pdf_size AS pdf_size,
		       p.pdf_uploaded_at AS pdf_uploaded_at
		ORDER BY p.pdf_uploaded_at DESC
		LIMIT $limit`, map[string]any{"limit": int64(limit)})
	if err != nil {
		return nil, fmt.Errorf("papers: needs-mineru: %w", err)
	}
	out := make([]NeedsMineruRow, 0, len(rows))
	for _, r := range rows {
		row := NeedsMineruRow{
			ArxivID:      asString(r["arxiv_id"]),
			YYMM:         asString(r["yymm"]),
			PDFSizeBytes: int64(asInt(r["pdf_size"])),
		}
		if pp := asString(r["pdf_path"]); pp != "" {
			row.PDFKey = "pdf/" + pp
		}
		if t := asTime(r["pdf_uploaded_at"]); t != nil {
			row.PDFUploadedAt = t
		}
		out = append(out, row)
	}
	return out, nil
}

// UpsertPDF write-through: creates the :PaperWork node if missing
// (source='arxiv-fallback') and flips has_pdf=true with the asset
// pointers. Idempotent. Returns ErrCatalogUnavailable when Neo4j is
// down (handler treats as deferred).
func (s *Store) UpsertPDF(ctx context.Context, arxivID, sha string, size int64, etag string) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	id := deriveIDs(arxivID)
	pdfPath := bucketRelKey(paperassets.AssetKey("pdf", id.ArxivID))
	_, err := s.nc.ExecuteWrite(ctx, `
		MERGE (p:PaperWork {arxiv_id: $arxiv_id})
		ON CREATE SET p.source = 'arxiv-fallback',
		              p.arxiv_id_canonical = $canonical,
		              p.yymm = $yymm,
		              p.has_md = false,
		              p.has_json = false
		SET p.arxiv_id_canonical = coalesce(p.arxiv_id_canonical, $canonical),
		    p.yymm = coalesce(p.yymm, $yymm),
		    p.has_pdf = true,
		    p.pdf_path = $pdf_path,
		    p.pdf_size = $size,
		    p.pdf_sha256 = $sha,
		    p.pdf_etag = $etag,
		    p.pdf_uploaded_at = datetime(),
		    p.last_assets_change_at = datetime()
		RETURN p.arxiv_id`,
		map[string]any{
			"arxiv_id":  id.ArxivID,
			"canonical": id.Canonical,
			"yymm":      id.YYMM,
			"pdf_path":  pdfPath,
			"size":      size,
			"sha":       sha,
			"etag":      etag,
		})
	if err != nil {
		return fmt.Errorf("papers: upsert pdf %s: %w", id.ArxivID, err)
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
	_, err := s.nc.ExecuteWrite(ctx, `
		MERGE (p:PaperWork {arxiv_id: $arxiv_id})
		ON CREATE SET p.source = 'arxiv-fallback',
		              p.arxiv_id_canonical = $canonical,
		              p.yymm = $yymm,
		              p.has_pdf = false,
		              p.has_json = false
		SET p.arxiv_id_canonical = coalesce(p.arxiv_id_canonical, $canonical),
		    p.yymm = coalesce(p.yymm, $yymm),
		    p.has_md = true,
		    p.md_path = $md_path,
		    p.md_size = $size,
		    p.md_etag = $etag,
		    p.md_uploaded_at = datetime(),
		    p.last_assets_change_at = datetime()
		REMOVE p.claimed_by_login, p.claim_expires_at, p.claim_id
		RETURN p.arxiv_id`,
		map[string]any{
			"arxiv_id":  id.ArxivID,
			"canonical": id.Canonical,
			"yymm":      id.YYMM,
			"md_path":   mdPath,
			"size":      size,
			"sha":       sha,
			"etag":      etag,
		})
	if err != nil {
		return fmt.Errorf("papers: upsert md %s: %w", id.ArxivID, err)
	}
	return nil
}
