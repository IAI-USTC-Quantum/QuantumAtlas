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
	// BatchSize is the unnest batch size for INSERT ... ON CONFLICT statements.
	BatchSize int
}

const defaultSyncBatch = 500

// SyncFromStore reconciles the catalog's asset flags (has_pdf / has_md /
// image_count) against the actual objects in the per-kind buckets. It is
// the §4.2 safety net: even if a write-through SQL update failed (PostgreSQL was
// down during an upload), a later sync upserts the row from the
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
// synthetic "doi:<doi>" string (matching the paper_works primary key from
// UpsertPDFByDOI/UpsertMDByDOI) so mergeAssetBatch lands on the existing
// row instead of creating an arxiv-fallback phantom.
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
//
//	  existing paper_works row from UpsertMDByDOI. We previously keyed
//		DOI images by the registrant ("10.1103") alone, which collided
//		every DOI under the same publisher into one counter — the
//		counts for sibling DOIs summed instead of staying per-paper.
//
// DOI image storage is a SINGLE zip per DOI (paperassets.DOIAssetKey
// emits "images/doi/<reg>/<suffix>.zip") — not a per-file directory.
// We therefore (a) strip the .zip / .<ext> suffix when reassembling the
// node key so the synthetic id matches DOINodeKey(<reg>/<suffix>)
// instead of leaking a phantom "doi:<reg>/<suffix>.zip" node, AND
// (b) decode the "__" back to "/" via paperassets.DOIDecodeStem so a
// nested-slash DOI ("10.1234/foo/bar" → stored as "foo__bar.zip")
// round-trips back to the original node key — without the decode
// the synthetic key was "doi:10.1234/foo__bar" and never matched the
// real "doi:10.1234/foo/bar" written by UpsertMDByDOI, regenerating
// the same phantom-node bug at a different layer. Finally
// (c) report a count of 1 zip per DOI. The "true" image_count
// (number of images inside the zip) is set authoritatively by
// UpsertMDByDOI from the parsed bundle; mergeImageBatch's DOI cypher
// uses coalesce(p.image_count, r.image_count) so this 1-per-zip
// presence signal never clobbers a real count.
//
// Returns (counts, total, err). A non-nil err means the listing was
// incomplete (S3 paginate failed, ctx canceled, etc.) — callers MUST
// propagate it instead of silently treating an empty result as "zero
// images". Earlier version swallowed the error and reported 0, which
// in production made `papers sync` claim success while leaving every
// paper_works.image_count unchanged at 0/null even when qatlas-images
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
		// trailing extension stripped AND the "__" placeholder
		// decoded back to "/" so the key round-trips to
		// DOINodeKey(<reg>/<suffix>) — never "doi:<reg>/<suffix>.zip"
		// or "doi:<reg>/foo__bar".
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
// DOI-aware upsert (DOINodeKey) instead of the arxiv fallback. Returns
// ok=false for keys that don't match either shape.
//
// For DOI keys, the suffix segment is post-processed by
// paperassets.DOIDecodeStem so a nested-slash DOI's "__" placeholder
// is restored to "/" — required for the synthetic node key to match
// what UpsertPDFByDOI / UpsertMDByDOI write (which use the original
// pre-DOISafeStem suffix). See listImageCounts for the same fix.
func stemFromKey(key, kind string) (stem string, isDOI bool, ok bool) {
	parts := strings.Split(key, "/")
	// DOI keys: <kind>/doi/<registrant>/<suffix>.<ext>
	if len(parts) == 4 && parts[1] == "doi" && parts[0] == kind {
		ext := path.Ext(parts[3])
		s := parts[2] + "/" + paperassets.DOIDecodeStem(strings.TrimSuffix(parts[3], ext))
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

// mergeAssetBatch upserts has_pdf/has_md=true for a set of stems in
// UNWIND batches. DOI-keyed assets (key starts with "doi:") go through
// a separate DOI-aware upsert that lands on the existing paper_works row
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
	// We also carry the bare DOI (node_key minus "doi:" prefix) so the
	// upsert can set doi — without it, a sync that recreates the row
	// from the bucket (catalog DB lost / restored from old backup) would
	// leave p.doi unset, and LookupDOI (which matches on p.doi, not on
	// the synthetic arxiv_id key) would never find the recovered node.
	var doiItems, arxivItems []map[string]any
	for key, p := range paths {
		if strings.HasPrefix(key, "doi:") {
			doiItems = append(doiItems, map[string]any{
				"node_key": key,
				"doi":      strings.TrimPrefix(key, "doi:"),
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

	touched := 0
	for i := 0; i < len(doiItems); i += batch {
		end := min(i+batch, len(doiItems))
		nodeKeys, dois, relPaths := syncAssetColumns(doiItems[i:end])
		stmt := fmt.Sprintf(`
			INSERT INTO paper_works (arxiv_id, source, identifier_scheme, doi, has_json, %s, %s, last_assets_change_at)
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::boolean[], $6::text[], $7::timestamptz[])
			ON CONFLICT (arxiv_id) DO UPDATE SET
				doi = EXCLUDED.doi,
				identifier_scheme = 'doi',
				%s = true,
				%s = EXCLUDED.%s,
				last_assets_change_at = now()`,
			flag, pathField, flag, pathField, pathField)
		flags := boolSlice(len(nodeKeys), true)
		times := nowSlice(len(nodeKeys))
		if _, err := s.pool.Exec(ctx, stmt, nodeKeys, constStringSlice(len(nodeKeys), "doi-upload"), constStringSlice(len(nodeKeys), "doi"), dois, flags, relPaths, times); err != nil {
			return touched, catalogUnavailable("papers sync: merge doi "+kind+" batch", err)
		}
		touched += len(nodeKeys)
	}
	for i := 0; i < len(arxivItems); i += batch {
		end := min(i+batch, len(arxivItems))
		arxivIDs, canonicals, yymms, relPaths := syncArxivAssetColumns(arxivItems[i:end])
		stmt := fmt.Sprintf(`
			INSERT INTO paper_works (arxiv_id, source, identifier_scheme, arxiv_id_canonical, yymm, has_json, %s, %s, last_assets_change_at)
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::boolean[], $7::boolean[], $8::text[], $9::timestamptz[])
			ON CONFLICT (arxiv_id) DO UPDATE SET
				arxiv_id_canonical = coalesce(paper_works.arxiv_id_canonical, EXCLUDED.arxiv_id_canonical),
				yymm = coalesce(paper_works.yymm, EXCLUDED.yymm),
				%s = true,
				%s = EXCLUDED.%s,
				last_assets_change_at = now()`,
			flag, pathField, flag, pathField, pathField)
		flags := boolSlice(len(arxivIDs), true)
		times := nowSlice(len(arxivIDs))
		if _, err := s.pool.Exec(ctx, stmt, arxivIDs, constStringSlice(len(arxivIDs), "arxiv-fallback"), constStringSlice(len(arxivIDs), "arxiv"), canonicals, yymms, boolSlice(len(arxivIDs), false), flags, relPaths, times); err != nil {
			return touched, catalogUnavailable("papers sync: merge "+kind+" batch", err)
		}
		touched += len(arxivIDs)
	}
	return touched, nil
}

// mergeImageBatch upserts image_count for a set of stems. DOI-keyed
// counts (key starts with "doi:") go through a separate DOI-aware
// upsert that lands on the existing paper_works row from
// UpsertMDByDOI; the arxiv-fallback path would create a phantom
// arxiv_id='<doi>' node with the wrong source.
func (s *Store) mergeImageBatch(ctx context.Context, counts map[string]int, batch int) (int, error) {
	if len(counts) == 0 {
		return 0, nil
	}
	// See mergeAssetBatch for why doiItems carry the bare DOI alongside
	// the synthetic node_key: without SET p.doi here, an image-only sync
	// against a fresh node would leave LookupDOI unable to find it.
	var doiItems, arxivItems []map[string]any
	for key, c := range counts {
		if strings.HasPrefix(key, "doi:") {
			doiItems = append(doiItems, map[string]any{
				"node_key":    key,
				"doi":         strings.TrimPrefix(key, "doi:"),
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

	touched := 0
	for i := 0; i < len(doiItems); i += batch {
		end := min(i+batch, len(doiItems))
		nodeKeys, dois, imageCounts := syncDOIImageColumns(doiItems[i:end])
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO paper_works (arxiv_id, source, identifier_scheme, doi, has_json, image_count, last_assets_change_at)
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::boolean[], $6::integer[], $7::timestamptz[])
			ON CONFLICT (arxiv_id) DO UPDATE SET
				doi = EXCLUDED.doi,
				identifier_scheme = 'doi',
				image_count = coalesce(paper_works.image_count, EXCLUDED.image_count),
				last_assets_change_at = now()`,
			nodeKeys, constStringSlice(len(nodeKeys), "doi-upload"), constStringSlice(len(nodeKeys), "doi"), dois, boolSlice(len(nodeKeys), false), imageCounts, nowSlice(len(nodeKeys))); err != nil {
			return touched, catalogUnavailable("papers sync: merge doi images batch", err)
		}
		touched += len(nodeKeys)
	}
	for i := 0; i < len(arxivItems); i += batch {
		end := min(i+batch, len(arxivItems))
		arxivIDs, canonicals, yymms, imageCounts, imagePaths := syncArxivImageColumns(arxivItems[i:end])
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO paper_works (arxiv_id, source, identifier_scheme, arxiv_id_canonical, yymm, has_json, image_count, images_path_prefix, last_assets_change_at)
			SELECT * FROM unnest($1::text[], $2::text[], $3::text[], $4::text[], $5::text[], $6::boolean[], $7::integer[], $8::text[], $9::timestamptz[])
			ON CONFLICT (arxiv_id) DO UPDATE SET
				arxiv_id_canonical = coalesce(paper_works.arxiv_id_canonical, EXCLUDED.arxiv_id_canonical),
				yymm = coalesce(paper_works.yymm, EXCLUDED.yymm),
				image_count = EXCLUDED.image_count,
				images_path_prefix = EXCLUDED.images_path_prefix,
				last_assets_change_at = now()`,
			arxivIDs, constStringSlice(len(arxivIDs), "arxiv-fallback"), constStringSlice(len(arxivIDs), "arxiv"), canonicals, yymms, boolSlice(len(arxivIDs), false), imageCounts, imagePaths, nowSlice(len(arxivIDs))); err != nil {
			return touched, catalogUnavailable("papers sync: merge images batch", err)
		}
		touched += len(arxivIDs)
	}
	return touched, nil
}

func syncAssetColumns(rows []map[string]any) (nodeKeys, dois, paths []string) {
	for _, r := range rows {
		nodeKeys = append(nodeKeys, r["node_key"].(string))
		dois = append(dois, r["doi"].(string))
		paths = append(paths, r["path"].(string))
	}
	return nodeKeys, dois, paths
}

func syncArxivAssetColumns(rows []map[string]any) (arxivIDs, canonicals, yymms, paths []string) {
	for _, r := range rows {
		arxivIDs = append(arxivIDs, r["arxiv_id"].(string))
		canonicals = append(canonicals, r["canonical"].(string))
		yymms = append(yymms, r["yymm"].(string))
		paths = append(paths, r["path"].(string))
	}
	return arxivIDs, canonicals, yymms, paths
}

func syncDOIImageColumns(rows []map[string]any) (nodeKeys, dois []string, counts []int) {
	for _, r := range rows {
		nodeKeys = append(nodeKeys, r["node_key"].(string))
		dois = append(dois, r["doi"].(string))
		counts = append(counts, int(r["image_count"].(int64)))
	}
	return nodeKeys, dois, counts
}

func syncArxivImageColumns(rows []map[string]any) (arxivIDs, canonicals, yymms []string, counts []int, paths []string) {
	for _, r := range rows {
		arxivIDs = append(arxivIDs, r["arxiv_id"].(string))
		canonicals = append(canonicals, r["canonical"].(string))
		yymms = append(yymms, r["yymm"].(string))
		counts = append(counts, int(r["image_count"].(int64)))
		paths = append(paths, r["images_path"].(string))
	}
	return arxivIDs, canonicals, yymms, counts, paths
}

func constStringSlice(n int, value string) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = value
	}
	return out
}

func boolSlice(n int, value bool) []bool {
	out := make([]bool, n)
	for i := range out {
		out[i] = value
	}
	return out
}

func nowSlice(n int) []time.Time {
	now := time.Now().UTC()
	out := make([]time.Time, n)
	for i := range out {
		out[i] = now
	}
	return out
}
