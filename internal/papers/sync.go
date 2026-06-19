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
// kind by listing the bucket prefix. DOI-keyed assets are keyed by the
// synthetic "doi:<doi>" string (matching the :PaperWork primary key from
// UpsertPDFByDOI/UpsertMDByDOI) so the MERGE in mergeAssetBatch lands
// on the existing node instead of creating an arxiv-fallback phantom.
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

// listImageCounts returns stem→count of image objects under images/.
// arXiv images: keyed by the bare arxiv stem (e.g. "2401.12345v1").
// DOI images:   keyed by "doi:<doi>" so they MERGE into the
//   existing :PaperWork node from UpsertMDByDOI. We previously keyed
//   DOI images by the registrant ("10.1103") alone, which collided
//   every DOI under the same publisher into one counter — the
//   counts for sibling DOIs summed instead of staying per-paper.
//
// DOI image storage is a SINGLE zip per DOI (paperassets.DOIAssetKey
// emits "images/doi/<reg>/<suffix>.zip") — not a per-file directory.
// We therefore (a) strip the .zip / .<ext> suffix when reassembling the
// node key so the synthetic id matches DOINodeKey(<reg>/<suffix>)
// instead of leaking a phantom "doi:<reg>/<suffix>.zip" node, and
// (b) report a count of 1 zip per DOI. The "true" image_count (number
// of images inside the zip) is set authoritatively by UpsertMDByDOI
// from the parsed bundle; mergeImageBatch's DOI cypher uses
// coalesce(p.image_count, r.image_count) so this 1-per-zip presence
// signal never clobbers a real count.
//
// Returns (counts, total, err). A non-nil err means the listing was
// incomplete (S3 paginate failed, ctx canceled, etc.) — callers MUST
// propagate it instead of silently treating an empty result as "zero
// images". Earlier version swallowed the error and reported 0, which
// in production made `papers sync` claim success while leaving every
// :PaperWork.image_count unchanged at 0/null even when qatlas-images
// had thousands of objects (bug surfaced 2026-06-01 during T3 reconcile
// — sync reported 0 images while the bucket actually held 34 prefixes
// of mirrored arxiv assets).
func listImageCounts(ctx context.Context, store objstore.Store) (map[string]int, int, error) {
	infos, err := store.ListPrefix(ctx, "images/", 0)
	if err != nil {
		return nil, 0, err
	}
	counts := map[string]int{}
	total := 0
	for _, info := range infos {
		parts := strings.Split(info.Key, "/")
		// DOI keys are 4 segments for the single-zip layout
		// (images/doi/<registrant>/<suffix>.zip) or 5+ for the
		// legacy per-file directory layout (images/doi/<reg>/<suffix>/<file>).
		// In both cases the synthetic node key is built from
		// segments [2] (registrant) and [3] (suffix), with any
		// trailing extension stripped so the key round-trips back
		// to DOINodeKey(<reg>/<suffix>) — never "doi:<reg>/<suffix>.zip".
		if len(parts) >= 4 && parts[0] == "images" && parts[1] == "doi" {
			suffix := strings.TrimSuffix(parts[3], path.Ext(parts[3]))
			if suffix == "" {
				continue
			}
			doiKey := parts[2] + "/" + suffix
			counts[DOINodeKey(doiKey)]++
			total++
			continue
		}
		// arXiv keys: images/<yymm>/<stem>/<file...>
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
// isDOI is true for DOI-keyed assets so the caller can route them to a
// DOI-aware MERGE (DOINodeKey) instead of the arxiv fallback. Returns
// ok=false for keys that don't match either shape.
func stemFromKey(key, kind string) (stem string, isDOI bool, ok bool) {
	parts := strings.Split(key, "/")
	// DOI keys: <kind>/doi/<registrant>/<suffix>.<ext>
	if len(parts) == 4 && parts[1] == "doi" && parts[0] == kind {
		ext := path.Ext(parts[3])
		s := parts[2] + "/" + strings.TrimSuffix(parts[3], ext)
		if s == "" {
			return "", false, false
		}
		return s, true, true
	}
	// arXiv keys: <kind>/<yymm>/<stem>.<ext>
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

// mergeAssetBatch MERGEs has_pdf/has_md=true for a set of stems in
// UNWIND batches. DOI-keyed assets (key starts with "doi:") go through
// a separate DOI-aware MERGE that lands on the existing :PaperWork node
// from UpsertPDFByDOI/UpsertMDByDOI; the arxiv-fallback path would
// create a phantom arxiv_id='<doi>' node with the wrong source.
// Returns the number of rows touched.
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

	// Split DOI vs arxiv-keyed assets. DOI keys arrive with the
	// "doi:<doi>" prefix from listKindPaths; arxiv keys are bare stems.
	var doiItems, arxivItems []map[string]any
	for key, p := range paths {
		if strings.HasPrefix(key, "doi:") {
			doiItems = append(doiItems, map[string]any{
				"node_key": key,
				"path":     p,
			})
		} else {
			id := deriveIDs(key)
			arxivItems = append(arxivItems, map[string]any{
				"arxiv_id":  id.ArxivID,
				"canonical": id.Canonical,
				"yymm":      id.YYMM,
				"path":      p,
			})
		}
	}

	doiCypher := fmt.Sprintf(`
		UNWIND $rows AS r
		MERGE (p:PaperWork {arxiv_id: r.node_key})
		ON CREATE SET p.source = 'doi-upload',
		              p.identifier_scheme = 'doi',
		              p.has_json = false
		SET p.%s = true,
		    p.%s = r.path,
		    p.last_assets_change_at = datetime()`, flag, pathField)

	arxivCypher := fmt.Sprintf(`
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
	for _, group := range []struct {
		cypher string
		rows   []map[string]any
		label  string
	}{
		{doiCypher, doiItems, "doi " + kind},
		{arxivCypher, arxivItems, kind},
	} {
		if len(group.rows) == 0 {
			continue
		}
		for i := 0; i < len(group.rows); i += batch {
			end := i + batch
			if end > len(group.rows) {
				end = len(group.rows)
			}
			chunk := group.rows[i:end]
			if _, err := s.nc.ExecuteWrite(ctx, group.cypher, map[string]any{"rows": chunk}); err != nil {
				return touched, fmt.Errorf("papers sync: merge %s batch: %w", group.label, err)
			}
			touched += len(chunk)
		}
	}
	return touched, nil
}

// mergeImageBatch MERGEs image_count for a set of stems. DOI-keyed
// counts (key starts with "doi:") go through a separate DOI-aware
// MERGE that lands on the existing :PaperWork node from
// UpsertMDByDOI; the arxiv-fallback path would create a phantom
// arxiv_id='<doi>' node with the wrong source.
func (s *Store) mergeImageBatch(ctx context.Context, counts map[string]int, batch int) (int, error) {
	if len(counts) == 0 {
		return 0, nil
	}
	var doiItems, arxivItems []map[string]any
	for key, c := range counts {
		if strings.HasPrefix(key, "doi:") {
			doiItems = append(doiItems, map[string]any{
				"node_key":    key,
				"image_count": int64(c),
			})
		} else {
			id := deriveIDs(key)
			arxivItems = append(arxivItems, map[string]any{
				"arxiv_id":    id.ArxivID,
				"canonical":   id.Canonical,
				"yymm":        id.YYMM,
				"image_count": int64(c),
				"images_path": "images/" + id.YYMM + "/" + id.StorageKey + "/",
			})
		}
	}

	doiCypher := `
		UNWIND $rows AS r
		MERGE (p:PaperWork {arxiv_id: r.node_key})
		ON CREATE SET p.source = 'doi-upload',
		              p.identifier_scheme = 'doi',
		              p.has_json = false
		SET p.image_count = coalesce(p.image_count, r.image_count),
		    p.last_assets_change_at = datetime()`

	arxivCypher := `
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
	for _, group := range []struct {
		cypher string
		rows   []map[string]any
		label  string
	}{
		{doiCypher, doiItems, "doi images"},
		{arxivCypher, arxivItems, "images"},
	} {
		if len(group.rows) == 0 {
			continue
		}
		for i := 0; i < len(group.rows); i += batch {
			end := i + batch
			if end > len(group.rows) {
				end = len(group.rows)
			}
			chunk := group.rows[i:end]
			if _, err := s.nc.ExecuteWrite(ctx, group.cypher, map[string]any{"rows": chunk}); err != nil {
				return touched, fmt.Errorf("papers sync: merge %s batch: %w", group.label, err)
			}
			touched += len(chunk)
		}
	}
	return touched, nil
}
