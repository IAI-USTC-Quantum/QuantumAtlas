package registry

// detail.go: read-side projections for the paper-detail surface
// (GET /api/papers/{paper_id}) — one paper plus its asset rows.

import (
	"context"
	"time"
)

// Asset projects one paper_assets row. Paths are bucket-relative (the
// "<yymm>/<stem>.<ext>" form the objstore Router expects under each
// per-kind bucket).
type Asset struct {
	AssetID        int64
	Source         string // 'arxiv' | 'published'
	ArxivVersion   int    // 0 for published assets
	PDFPath        string
	PDFSize        int64
	PDFSha256      string
	MinerUMDPath   string
	MinerUJSONPath string
	ImageCount     int
	LeaseID        string
	LeaseHolder    string
	LeaseExpiresAt *time.Time
	FetchedAt      time.Time
}

// PaperDetail is a paper plus every asset row it owns, for the
// paper-detail endpoint.
type PaperDetail struct {
	Paper  *Paper
	Assets []Asset
}

// GetWithAssets returns one paper and its assets by surrogate id.
// found=false when no such paper.
func (s *Store) GetWithAssets(ctx context.Context, paperID string) (d *PaperDetail, found bool, err error) {
	if !s.ensure(ctx) {
		return nil, false, ErrCatalogUnavailable
	}
	p, found, err := s.Get(ctx, paperID)
	if err != nil || !found {
		return nil, found, err
	}
	assets, err := s.Assets(ctx, paperID)
	if err != nil {
		return nil, false, err
	}
	return &PaperDetail{Paper: p, Assets: assets}, true, nil
}

// Assets lists every asset row of one paper, published first then by
// descending arXiv version (mirrors the default-asset ordering).
func (s *Store) Assets(ctx context.Context, paperID string) ([]Asset, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	rows, err := s.pool.Query(ctx, `
		SELECT asset_id, source, arxiv_version, pdf_path, pdf_size, pdf_sha256,
		       mineru_md_path, mineru_json_path, image_count,
		       lease_id, lease_holder, lease_expires_at, fetched_at
		FROM paper_assets
		WHERE paper_id = $1
		ORDER BY (source = 'published') DESC, arxiv_version DESC NULLS LAST, asset_id`, paperID)
	if err != nil {
		return nil, catalogUnavailable("registry: list assets "+paperID, err)
	}
	defer rows.Close()
	var out []Asset
	for rows.Next() {
		var (
			a       Asset
			version *int
			size    *int64
			sha     *string
			mdPath  *string
			jsPath  *string
			images  *int
			leaseID *string
			holder  *string
		)
		if err := rows.Scan(&a.AssetID, &a.Source, &version, &a.PDFPath, &size, &sha,
			&mdPath, &jsPath, &images, &leaseID, &holder, &a.LeaseExpiresAt, &a.FetchedAt); err != nil {
			return nil, catalogUnavailable("registry: scan asset "+paperID, err)
		}
		a.ArxivVersion = derefInt(version)
		if size != nil {
			a.PDFSize = *size
		}
		a.PDFSha256 = deref(sha)
		a.MinerUMDPath = deref(mdPath)
		a.MinerUJSONPath = deref(jsPath)
		if images != nil {
			a.ImageCount = *images
		}
		a.LeaseID = deref(leaseID)
		a.LeaseHolder = deref(holder)
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, catalogUnavailable("registry: iterate assets "+paperID, err)
	}
	return out, nil
}
