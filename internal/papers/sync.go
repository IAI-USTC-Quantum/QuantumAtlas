package papers

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// SyncReport summarizes a reconcile pass.
type SyncReport struct {
	PDFObjects    int
	MDObjects     int
	ImageObjects  int
	PapersTouched int
	StartedAt     time.Time
	Duration      time.Duration
}

// SyncOptions tunes a reconcile pass.
type SyncOptions struct {
	// DryRun reports the diff without writing to PostgreSQL.
	DryRun bool
	// BatchSize is the unnest batch size for the upsert statements.
	BatchSize int
}

const defaultSyncBatch = 500

// SyncFromStore reconciles paper_assets against the actual objects in the
// per-kind buckets. It is the safety net: even if a write-through failed
// (PostgreSQL down during an upload), a later sync upserts the paper +
// asset from the bucket listing. It does NOT create OpenAlex metadata —
// that is the openalex ingest path; sync only attaches asset state.
//
// store is the per-kind Router (or a single LocalStore in dev). Keys are
// listed under the "pdf/" / "markdown/" / "images/" prefixes.
func (s *Store) SyncFromStore(ctx context.Context, store objstore.Store, opts SyncOptions) (SyncReport, error) {
	rep := SyncReport{StartedAt: time.Now().UTC()}
	if !s.ensure(ctx) {
		return rep, ErrCatalogUnavailable
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultSyncBatch
	}

	pdfPaths, err := listKindPaths(ctx, store, "pdf")
	if err != nil {
		return rep, fmt.Errorf("papers sync: list pdf: %w", err)
	}
	rep.PDFObjects = len(pdfPaths)
	mdPaths, err := listKindPaths(ctx, store, "markdown")
	if err != nil {
		return rep, fmt.Errorf("papers sync: list markdown: %w", err)
	}
	rep.MDObjects = len(mdPaths)
	imgCounts, imgObjs, err := listImageCounts(ctx, store)
	if err != nil {
		return rep, fmt.Errorf("papers sync: list images: %w", err)
	}
	rep.ImageObjects = imgObjs

	if opts.DryRun {
		rep.PapersTouched = len(pdfPaths) + len(mdPaths) + len(imgCounts)
		rep.Duration = time.Since(rep.StartedAt)
		return rep, nil
	}

	touched := 0
	// PDF first so markdown / image merges can attach to an existing asset
	// (paper_assets.pdf_path is NOT NULL — an asset can't exist md-only).
	n, err := s.mergeAssetBatch(ctx, "pdf", pdfPaths, opts.BatchSize)
	if err != nil {
		return rep, err
	}
	touched += n
	n, err = s.mergeAssetBatch(ctx, "markdown", mdPaths, opts.BatchSize)
	if err != nil {
		return rep, err
	}
	touched += n
	n, err = s.mergeImageBatch(ctx, imgCounts, opts.BatchSize)
	if err != nil {
		return rep, err
	}
	touched += n
	rep.PapersTouched = touched
	rep.Duration = time.Since(rep.StartedAt)
	return rep, nil
}

// DOINodeKey is the internal map-key convention sync uses to distinguish
// DOI-keyed assets from arxiv-keyed ones while grouping bucket listings.
// It is NOT a database key (the surrogate-key catalog resolves DOI
// contributions by paper_doi); it only tags map entries within sync.
func DOINodeKey(doi string) string { return "doi:" + doi }

// listKindPaths returns stem->bucket-relative-path for a pdf/markdown
// kind by listing the bucket prefix. DOI-keyed assets are tagged with the
// "doi:<doi>" map key (DOINodeKey) so mergeAssetBatch routes them to the
// published-asset path; arxiv assets use the bare stem.
func listKindPaths(ctx context.Context, store objstore.Store, kind string) (map[string]string, error) {
	infos, err := store.ListPrefix(ctx, kind+"/", 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(infos))
	for _, info := range infos {
		stem, isDOI, ok := stemFromKey(info.Key, kind)
		if !ok {
			continue
		}
		key := stem
		if isDOI {
			key = DOINodeKey(stem)
		}
		out[key] = bucketRelKey(info.Key)
	}
	return out, nil
}

// listImageCounts returns stem->count of image objects under images/.
// arXiv images key on the bare arxiv stem; DOI images key on
// "doi:<doi>" (DOINodeKey). DOI image storage is a single zip per DOI
// (paperassets.DOIAssetKey -> "images/doi/<reg>/<suffix>.zip"), so we
// strip the extension and DOIDecodeStem the "__" placeholder back to "/"
// so the map key round-trips to DOINodeKey(<reg>/<suffix>).
//
// Returns (counts, total, err). A non-nil err means the listing was
// incomplete; callers MUST propagate it rather than treat an empty result
// as "zero images".
func listImageCounts(ctx context.Context, store objstore.Store) (map[string]int, int, error) {
	infos, err := store.ListPrefix(ctx, "images/", 0)
	if err != nil {
		return nil, 0, err
	}
	counts := map[string]int{}
	total := 0
	for _, info := range infos {
		parts := strings.Split(info.Key, "/")
		if len(parts) >= 4 && parts[0] == "images" && parts[1] == "doi" {
			suffix := paperassets.DOIDecodeStem(strings.TrimSuffix(parts[3], path.Ext(parts[3])))
			if suffix == "" {
				continue
			}
			doiKey := parts[2] + "/" + suffix
			counts[DOINodeKey(doiKey)]++
			total++
			continue
		}
		if len(parts) < 4 || parts[0] != "images" {
			continue
		}
		counts[parts[2]]++
		total++
	}
	return counts, total, nil
}

// stemFromKey extracts the arxiv stem from a "<kind>/<yymm>/<stem>.<ext>"
// key, or the DOI stem from a "<kind>/doi/<registrant>/<suffix>.<ext>" key.
// isDOI is true for DOI-keyed assets. For DOI keys the suffix is
// DOIDecodeStem'd so a nested-slash DOI's "__" placeholder is restored to
// "/". Returns ok=false for keys that match neither shape.
func stemFromKey(key, kind string) (stem string, isDOI bool, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) == 4 && parts[1] == "doi" && parts[0] == kind {
		ext := path.Ext(parts[3])
		s := parts[2] + "/" + paperassets.DOIDecodeStem(strings.TrimSuffix(parts[3], ext))
		if s == "" {
			return "", false, false
		}
		return s, true, true
	}
	if len(parts) != 3 || parts[0] != kind {
		return "", false, false
	}
	base := parts[2]
	s := strings.TrimSuffix(base, path.Ext(base))
	if s == "" {
		return "", false, false
	}
	return s, false, true
}

// mergeAssetBatch reconciles a set of pdf/markdown assets. arxiv-keyed
// items upsert papers + paper_assets(source='arxiv'); DOI-keyed items
// (map key "doi:<doi>") resolve the published asset. Returns rows touched.
//
//   - kind="pdf":      insert-or-update the asset's pdf_path.
//   - kind="markdown": update the existing asset's mineru_md_path +
//     mineru_json_path (an asset can't be created md-only — pdf_path is
//     NOT NULL — so markdown attaches to a pdf synced earlier in the pass).
func (s *Store) mergeAssetBatch(ctx context.Context, kind string, paths map[string]string, batch int) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	touched := 0
	for key, rel := range paths {
		if strings.HasPrefix(key, "doi:") {
			n, err := s.mergeDOIAsset(ctx, kind, strings.TrimPrefix(key, "doi:"), rel)
			if err != nil {
				return touched, err
			}
			touched += n
			continue
		}
		n, err := s.mergeArxivAsset(ctx, kind, key, rel)
		if err != nil {
			return touched, err
		}
		touched += n
	}
	return touched, nil
}

// mergeArxivAsset upserts one arxiv pdf/markdown asset by bare id + version.
func (s *Store) mergeArxivAsset(ctx context.Context, kind, stem, rel string) (int, error) {
	bare := bareArxivID(stem)
	version := arxivVersion(stem)
	if kind == "pdf" {
		if _, err := s.pool.Exec(ctx, `
			WITH p AS (
				INSERT INTO papers (paper_arxiv_id) VALUES ($1)
				ON CONFLICT (paper_arxiv_id) DO UPDATE SET paper_arxiv_id = EXCLUDED.paper_arxiv_id
				RETURNING paper_id
			)
			INSERT INTO paper_assets (paper_id, source, arxiv_version, pdf_path, fetched_at)
			SELECT paper_id, 'arxiv', $2, $3, now() FROM p
			ON CONFLICT (paper_id, source, arxiv_version) DO UPDATE SET
				pdf_path = EXCLUDED.pdf_path,
				fetched_at = now()`,
			bare, version, rel); err != nil {
			return 0, catalogUnavailable("papers sync: merge arxiv pdf", err)
		}
		return 1, nil
	}
	// markdown: update the existing asset (mineru_md + derived json path).
	jsonRel := bucketRelKey(paperassets.AssetKey("json", versionedArxivID(bare, version)))
	tag, err := s.pool.Exec(ctx, `
		UPDATE paper_assets a
		SET mineru_md_path = $3, mineru_json_path = $4
		FROM papers p
		WHERE p.paper_arxiv_id = $1
		  AND a.paper_id = p.paper_id
		  AND a.source = 'arxiv'
		  AND a.arxiv_version = $2`,
		bare, version, rel, jsonRel)
	if err != nil {
		return 0, catalogUnavailable("papers sync: merge arxiv markdown", err)
	}
	return int(tag.RowsAffected()), nil
}

// mergeDOIAsset upserts one DOI pdf/markdown asset onto the published
// asset of the paper resolved by doi (no OpenAlex verification available
// during a reconcile — resolveDOIPaper keys purely on the DOI).
func (s *Store) mergeDOIAsset(ctx context.Context, kind, doi, rel string) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, catalogUnavailable("papers sync: merge doi begin", err)
	}
	defer tx.Rollback(ctx)
	pid, err := resolveDOIPaper(ctx, tx, doi, DOIVerification{})
	if err != nil {
		return 0, catalogUnavailable("papers sync: resolve doi", err)
	}
	if kind == "pdf" {
		_, err = tx.Exec(ctx, `
			INSERT INTO paper_assets (paper_id, source, pdf_path, fetched_at)
			VALUES ($1, 'published', $2, now())
			ON CONFLICT (paper_id) WHERE source = 'published' DO UPDATE SET
				pdf_path = EXCLUDED.pdf_path, fetched_at = now()`,
			pid, rel)
	} else {
		jsonRel := bucketRelKey(paperassets.DOIAssetKey("json", doi))
		_, err = tx.Exec(ctx, `
			UPDATE paper_assets
			SET mineru_md_path = $2, mineru_json_path = $3
			WHERE paper_id = $1 AND source = 'published'`,
			pid, rel, jsonRel)
	}
	if err != nil {
		return 0, catalogUnavailable("papers sync: merge doi asset", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, catalogUnavailable("papers sync: merge doi commit", err)
	}
	return 1, nil
}

// mergeImageBatch reconciles image_count onto assets. arxiv counts update
// the arxiv asset (any version — images are shared across versions in the
// bucket layout, keyed by bare stem); DOI counts update the published
// asset.
func (s *Store) mergeImageBatch(ctx context.Context, counts map[string]int, batch int) (int, error) {
	if len(counts) == 0 {
		return 0, nil
	}
	touched := 0
	for key, c := range counts {
		if strings.HasPrefix(key, "doi:") {
			doi := strings.TrimPrefix(key, "doi:")
			tx, err := s.pool.Begin(ctx)
			if err != nil {
				return touched, catalogUnavailable("papers sync: image doi begin", err)
			}
			pid, err := resolveDOIPaper(ctx, tx, doi, DOIVerification{})
			if err != nil {
				_ = tx.Rollback(ctx)
				return touched, catalogUnavailable("papers sync: image resolve doi", err)
			}
			_, err = tx.Exec(ctx, `
				UPDATE paper_assets SET image_count = $2
				WHERE paper_id = $1 AND source = 'published'`, pid, c)
			if err != nil {
				_ = tx.Rollback(ctx)
				return touched, catalogUnavailable("papers sync: image doi update", err)
			}
			if err := tx.Commit(ctx); err != nil {
				return touched, catalogUnavailable("papers sync: image doi commit", err)
			}
			touched++
			continue
		}
		bare := bareArxivID(key)
		tag, err := s.pool.Exec(ctx, `
			UPDATE paper_assets a
			SET image_count = $2
			FROM papers p
			WHERE p.paper_arxiv_id = $1 AND a.paper_id = p.paper_id AND a.source = 'arxiv'`,
			bare, c)
		if err != nil {
			return touched, catalogUnavailable("papers sync: image arxiv update", err)
		}
		touched += int(tag.RowsAffected())
	}
	return touched, nil
}
