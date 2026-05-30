package papers

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
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
	// DryRun reports the diff without writing to Neo4j.
	DryRun bool
	// BatchSize is the UNWIND batch size for MERGE statements.
	BatchSize int
}

const defaultSyncBatch = 500

// SyncFromStore reconciles the catalog's asset flags (has_pdf / has_md /
// image_count) against the actual objects in the per-kind buckets. It is
// the §4.2 safety net: even if a write-through Cypher failed (Neo4j was
// down during an upload), a later sync re-MERGEs the node from the
// bucket listing. It does NOT create OpenAlex metadata — that's the
// `openalex` ingest path; sync only attaches asset载体 state.
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

	// PDF + MD: stem → bucket-relative path.
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
	imgCounts, imgObjs := listImageCounts(ctx, store)
	rep.ImageObjects = imgObjs

	if opts.DryRun {
		rep.PapersTouched = len(pdfPaths) + len(mdPaths) + len(imgCounts)
		rep.Duration = time.Since(rep.StartedAt)
		return rep, nil
	}

	touched := 0
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

// listKindPaths returns stem→bucket-relative-path for a pdf/markdown
// kind by listing the bucket prefix.
func listKindPaths(ctx context.Context, store objstore.Store, kind string) (map[string]string, error) {
	infos, err := store.ListPrefix(ctx, kind+"/", 0)
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(infos))
	for _, info := range infos {
		stem, ok := stemFromKey(info.Key, kind)
		if !ok {
			continue
		}
		out[stem] = bucketRelKey(info.Key)
	}
	return out, nil
}

// listImageCounts returns stem→count of image objects under images/.
func listImageCounts(ctx context.Context, store objstore.Store) (map[string]int, int) {
	infos, err := store.ListPrefix(ctx, "images/", 0)
	if err != nil {
		return map[string]int{}, 0
	}
	counts := map[string]int{}
	total := 0
	for _, info := range infos {
		parts := strings.Split(info.Key, "/")
		// images/<yymm>/<stem>/<file...>
		if len(parts) < 4 || parts[0] != "images" {
			continue
		}
		counts[parts[2]]++
		total++
	}
	return counts, total
}

// stemFromKey extracts the arxiv stem from a "<kind>/<yymm>/<stem>.<ext>"
// key. Returns ok=false for keys that don't match the shape.
func stemFromKey(key, kind string) (string, bool) {
	parts := strings.Split(key, "/")
	if len(parts) != 3 || parts[0] != kind {
		return "", false
	}
	base := parts[2]
	stem := strings.TrimSuffix(base, path.Ext(base))
	if stem == "" {
		return "", false
	}
	return stem, true
}

// mergeAssetBatch MERGEs has_pdf/has_md=true for a set of stems in
// UNWIND batches. Returns the number of rows touched.
func (s *Store) mergeAssetBatch(ctx context.Context, kind string, paths map[string]string, batch int) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	flag := "has_pdf"
	pathField := "pdf_path"
	if kind == "markdown" {
		flag = "has_md"
		pathField = "md_path"
	}
	items := make([]map[string]any, 0, len(paths))
	for stem, p := range paths {
		id := deriveIDs(stem)
		items = append(items, map[string]any{
			"arxiv_id":  id.ArxivID,
			"canonical": id.Canonical,
			"yymm":      id.YYMM,
			"path":      p,
		})
	}
	cypher := fmt.Sprintf(`
		UNWIND $rows AS r
		MERGE (p:PaperWork {arxiv_id: r.arxiv_id})
		ON CREATE SET p.source = 'arxiv-fallback',
		              p.arxiv_id_canonical = r.canonical,
		              p.yymm = r.yymm,
		              p.has_json = false
		SET p.arxiv_id_canonical = coalesce(p.arxiv_id_canonical, r.canonical),
		    p.yymm = coalesce(p.yymm, r.yymm),
		    p.%s = true,
		    p.%s = r.path,
		    p.last_assets_change_at = datetime()`, flag, pathField)

	touched := 0
	for i := 0; i < len(items); i += batch {
		end := i + batch
		if end > len(items) {
			end = len(items)
		}
		chunk := items[i:end]
		if _, err := s.nc.ExecuteWrite(ctx, cypher, map[string]any{"rows": chunk}); err != nil {
			return touched, fmt.Errorf("papers sync: merge %s batch: %w", kind, err)
		}
		touched += len(chunk)
	}
	return touched, nil
}

// mergeImageBatch MERGEs image_count for a set of stems.
func (s *Store) mergeImageBatch(ctx context.Context, counts map[string]int, batch int) (int, error) {
	if len(counts) == 0 {
		return 0, nil
	}
	items := make([]map[string]any, 0, len(counts))
	for stem, c := range counts {
		id := deriveIDs(stem)
		items = append(items, map[string]any{
			"arxiv_id":    id.ArxivID,
			"canonical":   id.Canonical,
			"yymm":        id.YYMM,
			"image_count": int64(c),
			"images_path": "images/" + id.YYMM + "/" + id.StorageKey + "/",
		})
	}
	cypher := `
		UNWIND $rows AS r
		MERGE (p:PaperWork {arxiv_id: r.arxiv_id})
		ON CREATE SET p.source = 'arxiv-fallback',
		              p.arxiv_id_canonical = r.canonical,
		              p.yymm = r.yymm,
		              p.has_json = false
		SET p.arxiv_id_canonical = coalesce(p.arxiv_id_canonical, r.canonical),
		    p.yymm = coalesce(p.yymm, r.yymm),
		    p.image_count = r.image_count,
		    p.images_path_prefix = r.images_path,
		    p.last_assets_change_at = datetime()`
	touched := 0
	for i := 0; i < len(items); i += batch {
		end := i + batch
		if end > len(items) {
			end = len(items)
		}
		chunk := items[i:end]
		if _, err := s.nc.ExecuteWrite(ctx, cypher, map[string]any{"rows": chunk}); err != nil {
			return touched, fmt.Errorf("papers sync: merge images batch: %w", err)
		}
		touched += len(chunk)
	}
	return touched, nil
}
