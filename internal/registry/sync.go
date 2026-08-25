package registry

import (
	"context"
	"fmt"
	"log/slog"
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
	PapersTouched int

	// ShardsTotal is the number of shard prefixes selected for listing
	// (after Kinds / ShardFrom / ShardTo filtering); ShardsDone is how
	// many listed AND merged successfully. Failed shards are skipped,
	// not counted in ShardsDone.
	ShardsTotal int
	ShardsDone  int

	// FailedShards records every shard whose listing failed after
	// retries + drill-down; the sync skipped them and continued with
	// the remaining shards. Non-empty FailedShards makes SyncFromStore
	// return a non-nil error AFTER all other shards were processed.
	FailedShards []ShardFailure

	StartedAt time.Time
	Duration  time.Duration
}

// ShardFailure records one shard that could not be listed during a
// reconcile pass (after retries and drill-down). The error is rendered
// as a string because the report crosses the CLI boundary.
type ShardFailure struct {
	Shard string // e.g. "pdf/0906/"
	Err   string
}

// SyncOptions tunes a reconcile pass.
type SyncOptions struct {
	// DryRun reports the diff without writing to PostgreSQL.
	DryRun bool
	// BatchSize is retained for shape parity with the legacy catalog;
	// the registry port upserts per item (each goes through
	// ResolveOrMint's own transaction) and does not batch unnest.
	BatchSize int
	// Kinds restricts the reconcile to a subset of "pdf", "markdown".
	// Empty = both, always run in canonical merge order (pdf first so
	// markdown merges attach to minted assets). Image counts are NOT
	// synced: image_count is populated from MinerU upload metadata and
	// the image file list is served on demand from the images bucket.
	Kinds []string
	// ShardFrom / ShardTo bound the shard range, inclusive, compared
	// lexicographically on shard directory names ("0001".."2608").
	// Either side may be empty (open-ended). The "doi" pseudo-shard is
	// always included when its kind runs.
	ShardFrom, ShardTo string
}

const defaultSyncBatch = 500

// Syncer reconciles the registry against object-store listings.
// Construct with NewSyncer.
type Syncer struct {
	store *Store
}

// NewSyncer returns a Syncer writing through store.
func NewSyncer(store *Store) *Syncer {
	return &Syncer{store: store}
}

// SyncFromStore reconciles paper_assets against the actual objects in
// the per-kind buckets. It is the safety net: even if a write-through
// failed (PostgreSQL down during an upload), a later sync upserts the
// paper + asset from the bucket listing. Every paper a pdf listing
// introduces is minted through ResolveOrMint; markdown listings only
// update assets of papers the registry already knows (an asset can't
// exist md-only — pdf_path is NOT NULL — so markdown attaches to a pdf
// synced earlier in the pass, and sync never mints an asset-less paper
// off a markdown object alone). The images bucket is NOT listed: image
// counts come from MinerU upload metadata and per-paper image listings
// are served on demand (GET /api/papers/{paper_id}/images).
//
// The pass is a staged pipeline per shard: enumerate the kind's shard
// prefixes (ListDirs), then for each shard list it (listPrefixResilient:
// retries + drill-down) and merge THAT shard's items immediately, so a
// multi-hour run commits progress continuously instead of holding
// everything until a final merge phase. A shard that fails to list is
// recorded in SyncReport.FailedShards and skipped — partial progress is
// strictly better than none and a re-run reconciles the failed shards.
// Merge (PostgreSQL) errors still abort the pass immediately: they are
// systemic, not shard-local. After all shards are processed, a non-nil
// error is returned iff FailedShards is non-empty.
//
// store is the per-kind Router (or a single LocalStore in dev). Keys are
// listed under the "pdf/" / "markdown/" prefixes.
func (y *Syncer) SyncFromStore(ctx context.Context, store objstore.Store, opts SyncOptions) (SyncReport, error) {
	rep := SyncReport{StartedAt: time.Now().UTC()}
	if !y.store.ensure(ctx) {
		return rep, ErrCatalogUnavailable
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = defaultSyncBatch
	}
	kinds, err := orderedKinds(opts.Kinds)
	if err != nil {
		return rep, err
	}
	for _, kind := range kinds {
		if err := y.syncKind(ctx, store, kind, opts, &rep); err != nil {
			rep.Duration = time.Since(rep.StartedAt)
			return rep, err
		}
	}
	rep.Duration = time.Since(rep.StartedAt)
	if len(rep.FailedShards) > 0 {
		first := rep.FailedShards[0]
		return rep, fmt.Errorf("registry: sync: %d of %d shards failed, skipped (first: %s: %s)",
			len(rep.FailedShards), rep.ShardsTotal, first.Shard, first.Err)
	}
	return rep, nil
}

// syncKind runs the staged list→merge pipeline for one kind, updating
// rep in place. Shard listing failures are recorded and skipped; a
// merge (PostgreSQL) failure aborts the whole pass with a non-nil
// error.
func (y *Syncer) syncKind(ctx context.Context, store objstore.Store, kind string, opts SyncOptions, rep *SyncReport) error {
	shards, err := store.ListDirs(ctx, kind+"/")
	if err != nil {
		rep.FailedShards = append(rep.FailedShards, ShardFailure{Shard: kind + "/", Err: err.Error()})
		return nil
	}
	shards = filterShards(kind, shards, opts.ShardFrom, opts.ShardTo)
	rep.ShardsTotal += len(shards)
	for i, shard := range shards {
		start := time.Now()
		infos, err := listPrefixResilient(ctx, store, shard, 3)
		if err != nil {
			slog.Warn("registry: sync shard failed, skipping", "kind", kind, "shard", shard, "error", err)
			rep.FailedShards = append(rep.FailedShards, ShardFailure{Shard: shard, Err: err.Error()})
			continue
		}
		touched := 0
		paths := parseKindInfos(infos, kind)
		if kind == "pdf" {
			rep.PDFObjects += len(paths)
		} else {
			rep.MDObjects += len(paths)
		}
		if opts.DryRun {
			touched = len(paths)
		} else {
			n, err := y.mergeAssetBatch(ctx, kind, paths)
			if err != nil {
				return err
			}
			touched = n
		}
		rep.PapersTouched += touched
		rep.ShardsDone++
		slog.Info("registry: sync shard done",
			"kind", kind, "shard", shard,
			"shardIndex", i+1, "shardCount", len(shards),
			"keys", len(infos), "papers", touched,
			"shardElapsed", time.Since(start).Round(time.Millisecond),
			"totalElapsed", time.Since(rep.StartedAt).Round(time.Millisecond))
	}
	return nil
}

// orderedKinds returns the kinds to reconcile in canonical merge order
// (pdf before markdown — markdown merges attach to assets a pdf sync
// minted earlier in the pass). Empty requested = both. Unknown kind
// names are an error.
func orderedKinds(requested []string) ([]string, error) {
	want := map[string]bool{}
	for _, k := range requested {
		switch k {
		case "pdf", "markdown":
			want[k] = true
		default:
			return nil, fmt.Errorf("registry: sync unknown kind %q (want pdf or markdown)", k)
		}
	}
	var out []string
	for _, k := range []string{"pdf", "markdown"} {
		if len(requested) == 0 || want[k] {
			out = append(out, k)
		}
	}
	return out, nil
}

// filterShards keeps the shard prefixes whose directory name falls
// inside the inclusive [from, to] range (lexicographic on names like
// "0001"; either side may be empty = open-ended). The "doi"
// pseudo-shard always passes when its kind runs.
func filterShards(kind string, shards []string, from, to string) []string {
	if from == "" && to == "" {
		return shards
	}
	var out []string
	for _, shard := range shards {
		name := strings.TrimSuffix(strings.TrimPrefix(shard, kind+"/"), "/")
		if name == "doi" || shardInRange(name, from, to) {
			out = append(out, shard)
		}
	}
	return out
}

// shardInRange reports whether a shard directory name falls inside the
// inclusive [from, to] range.
func shardInRange(name, from, to string) bool {
	if from != "" && name < from {
		return false
	}
	if to != "" && name > to {
		return false
	}
	return true
}

// DOINodeKey is the internal map-key convention sync uses to distinguish
// DOI-keyed assets from arXiv-keyed ones while grouping bucket listings.
// It is NOT a database key (the registry resolves DOI contributions by
// the doi identity); it only tags map entries within sync.
func DOINodeKey(doi string) string { return "doi:" + doi }

// listRetryBackoff is the wait between ListPrefix attempts inside
// listPrefixResilient (attempts = len(listRetryBackoff)+1). A var so
// tests can shrink it; production keeps the 2s/5s rhythm that rode out
// transient RustFS "Io error: timeout" responses.
var listRetryBackoff = []time.Duration{2 * time.Second, 5 * time.Second}

// listPrefixResilient lists prefix with bounded retries and adaptive
// drill-down. RustFS can wedge server-side on a single big recursive
// ListObjectsV2 (observed: InternalError "Io error: timeout" after
// ~120s for one slow shard) while delimiter listings and narrower
// prefixes stay fast — so after the retries are exhausted and maxDepth
// allows, it enumerates child "directories" via ListDirs and lists each
// child recursively, concatenating the results.
//
// A listing that still fails at maxDepth 0, a ListDirs failure during
// drill-down, or a wedged prefix with no child directories to retry
// through, propagates the error: callers must never treat a partial
// listing as complete.
//
// Caveat: drill-down recovers objects under child directories only.
// Objects homed directly at the wedged level (siblings of the child
// dirs) stay unreachable until the backend recovers — sync never
// deletes assets, so they simply reconcile on a later pass.
func listPrefixResilient(ctx context.Context, store objstore.Store, prefix string, maxDepth int) ([]objstore.ObjectInfo, error) {
	var infos []objstore.ObjectInfo
	var err error
	for attempt := 0; attempt <= len(listRetryBackoff); attempt++ {
		if attempt > 0 {
			timer := time.NewTimer(listRetryBackoff[attempt-1])
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		infos, err = store.ListPrefix(ctx, prefix, 0)
		if err == nil {
			return infos, nil
		}
	}
	if maxDepth <= 0 {
		return nil, err
	}
	slog.Warn("registry: sync listing wedged, drilling down", "prefix", prefix, "error", err)
	dirs, derr := store.ListDirs(ctx, prefix)
	if derr != nil {
		return nil, fmt.Errorf("%w (drill-down list dirs: %v)", err, derr)
	}
	if len(dirs) == 0 {
		// No children to retry through — the wedged listing cannot be
		// decomposed. Propagate rather than report an empty prefix.
		return nil, err
	}
	var out []objstore.ObjectInfo
	for _, dir := range dirs {
		sub, err := listPrefixResilient(ctx, store, dir, maxDepth-1)
		if err != nil {
			return nil, err
		}
		out = append(out, sub...)
	}
	return out, nil
}

// listKindPaths returns stem->bucket-relative-path for a pdf/markdown
// kind by listing the bucket per shard. DOI-keyed assets are tagged with
// the "doi:<doi>" map key (DOINodeKey) so mergeAssetBatch routes them to
// the published-asset path; arXiv assets use the stem (old-style
// category-layout keys yield the canonical "<category>/<stem>" form).
//
// The listing enumerates top-level shards first (ListDirs) and then
// lists each shard separately: a single whole-bucket ListPrefix against
// a large RustFS bucket can hang server-side until the HTTP client
// times out, while shard-prefixed listings stay small. Per-shard
// listing goes through listPrefixResilient (retry + drill-down). A
// failure on ANY shard aborts the whole listing — callers must not
// treat partial results as complete. (SyncFromStore itself uses the
// per-shard staged pipeline in syncKind, which relaxes this to
// skip-and-record; this function keeps the strict contract for
// list-only callers.)
func listKindPaths(ctx context.Context, store objstore.Store, kind string) (map[string]string, error) {
	shards, err := store.ListDirs(ctx, kind+"/")
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for _, shard := range shards {
		infos, err := listPrefixResilient(ctx, store, shard, 3)
		if err != nil {
			return nil, fmt.Errorf("shard %s: %w", shard, err)
		}
		slog.Debug("registry: sync listed shard", "kind", kind, "shard", shard, "objects", len(infos))
		for key, rel := range parseKindInfos(infos, kind) {
			out[key] = rel
		}
	}
	return out, nil
}

// parseKindInfos maps one shard's object listing to
// stem->bucket-relative-path for a pdf/markdown kind (same rules as
// listKindPaths applies per key).
func parseKindInfos(infos []objstore.ObjectInfo, kind string) map[string]string {
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
	return out
}

// stemFromKey extracts the arXiv stem from a "<kind>/<yymm>/<stem>.<ext>"
// key (new-style), a "<kind>/<yymm>/<category>/<stem>.<ext>" key
// (old-style canonical — paperassets.AssetKeyFor renders pre-2007 ids
// with the category as a subdirectory), or the DOI stem from a
// "<kind>/doi/<registrant>/<suffix>.<ext>" key. isDOI is true for
// DOI-keyed assets. For DOI keys the suffix is DOIDecodeStem'd so a
// nested-slash DOI's "__" placeholder is restored to "/"; for old-style
// arXiv keys the returned stem is the canonical "<category>/<stem>"
// form so NormalizeArxivID / ResolveOrMint resolve the same identity
// the ingester minted. Returns ok=false for keys that match none of
// these shapes.
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
	if len(parts) == 4 && parts[0] == kind {
		// Old-style canonical: <kind>/<yymm>/<category>/<stem>.<ext>
		s := strings.TrimSuffix(parts[3], path.Ext(parts[3]))
		if s == "" {
			return "", false, false
		}
		return parts[2] + "/" + s, false, true
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

// mergeAssetBatch reconciles a set of pdf/markdown assets. arXiv-keyed
// items resolve through the arXiv identity; DOI-keyed items (map key
// "doi:<doi>") resolve through the DOI identity onto the published
// asset. Returns rows touched.
func (y *Syncer) mergeAssetBatch(ctx context.Context, kind string, paths map[string]string) (int, error) {
	if len(paths) == 0 {
		return 0, nil
	}
	touched := 0
	for key, rel := range paths {
		if strings.HasPrefix(key, "doi:") {
			n, err := y.mergeDOIAsset(ctx, kind, strings.TrimPrefix(key, "doi:"), rel)
			if err != nil {
				return touched, err
			}
			touched += n
			continue
		}
		n, err := y.mergeArxivAsset(ctx, kind, key, rel)
		if err != nil {
			return touched, err
		}
		touched += n
	}
	return touched, nil
}

// mergeArxivAsset reconciles one arXiv pdf/markdown asset by stem. A
// pdf resolves/mints the paper through ResolveOrMint and upserts the
// asset; markdown only updates an existing asset (it can never
// introduce a paper on its own).
func (y *Syncer) mergeArxivAsset(ctx context.Context, kind, stem, rel string) (int, error) {
	version := ArxivVersionOf(stem)
	if version <= 0 {
		// Bucket stems always carry a vN suffix (paperassets.AssetKey);
		// a versionless stem is a malformed key, not a paper to mint.
		return 0, nil
	}
	bare := NormalizeArxivID(stem)
	if kind == "pdf" {
		paperID, _, err := y.store.ResolveOrMint(ctx, PaperRef{ArxivID: stem})
		if err != nil {
			return 0, fmt.Errorf("registry: sync resolve arxiv %s: %w", bare, err)
		}
		if _, err := y.store.pool.Exec(ctx, `
			INSERT INTO paper_assets (paper_id, source, arxiv_version, pdf_path, fetched_at)
			VALUES ($1, 'arxiv', $2, $3, now())
			ON CONFLICT (paper_id, source, arxiv_version) DO UPDATE SET
				pdf_path = EXCLUDED.pdf_path,
				fetched_at = now()`,
			paperID, version, rel); err != nil {
			return 0, catalogUnavailable("registry: sync merge arxiv pdf", err)
		}
		return 1, nil
	}
	// markdown: update the existing asset (mineru_md + derived json
	// path) without minting — the paper must already be known.
	paperID, found, err := y.store.LookupByIdentity(ctx, ArxivKey(bare))
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	jsonRel := bucketRelKey(paperassets.AssetKey("json", versionedArxivID(bare, version)))
	tag, err := y.store.pool.Exec(ctx, `
		UPDATE paper_assets
		SET mineru_md_path = $3, mineru_json_path = $4
		WHERE paper_id = $1 AND source = 'arxiv' AND arxiv_version = $2`,
		paperID, version, rel, jsonRel)
	if err != nil {
		return 0, catalogUnavailable("registry: sync merge arxiv markdown", err)
	}
	return int(tag.RowsAffected()), nil
}

// mergeDOIAsset reconciles one DOI pdf/markdown asset onto the published
// asset of the paper the DOI resolves to (no OpenAlex verification is
// available during a reconcile — the ref keys purely on the DOI). Like
// the arXiv path, only a pdf mints; markdown attaches to a known paper.
func (y *Syncer) mergeDOIAsset(ctx context.Context, kind, doi, rel string) (int, error) {
	if kind == "pdf" {
		paperID, _, err := y.store.ResolveOrMint(ctx, PaperRef{DOI: doi})
		if err != nil {
			return 0, fmt.Errorf("registry: sync resolve doi %s: %w", doi, err)
		}
		if _, err := y.store.pool.Exec(ctx, `
			INSERT INTO paper_assets (paper_id, source, pdf_path, fetched_at)
			VALUES ($1, 'published', $2, now())
			ON CONFLICT (paper_id) WHERE source = 'published' DO UPDATE SET
				pdf_path = EXCLUDED.pdf_path, fetched_at = now()`,
			paperID, rel); err != nil {
			return 0, catalogUnavailable("registry: sync merge doi pdf", err)
		}
		return 1, nil
	}
	paperID, found, err := y.store.LookupByIdentity(ctx, DOIKey(NormalizeDOI(doi)))
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, nil
	}
	jsonRel := bucketRelKey(paperassets.DOIAssetKey("json", doi))
	tag, err := y.store.pool.Exec(ctx, `
		UPDATE paper_assets
		SET mineru_md_path = $2, mineru_json_path = $3
		WHERE paper_id = $1 AND source = 'published'`,
		paperID, rel, jsonRel)
	if err != nil {
		return 0, catalogUnavailable("registry: sync merge doi markdown", err)
	}
	return int(tag.RowsAffected()), nil
}
