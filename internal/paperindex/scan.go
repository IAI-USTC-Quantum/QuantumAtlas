package paperindex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// AssetKind enumerates the four asset categories under qatlas-raw
// bucket. Used by both the live webhook handler and the bootstrap
// subcommand to dispatch per-kind logic uniformly.
type AssetKind string

const (
	KindPDF      AssetKind = "pdf"
	KindMarkdown AssetKind = "markdown"
	KindJSON     AssetKind = "json"
	KindImages   AssetKind = "images"
)

// ParseAssetKey decodes a bucket object key into the (kind, arxivID,
// yymm) triple needed by paperindex.Store upsert methods. Returns
// ok=false for any key shape we don't recognise (the catch-all so
// noise like index/* or operator-uploaded files at the bucket root
// doesn't enter the catalog).
//
// Recognised shapes (matching internal/paperassets.AssetPath):
//
//	pdf/<yymm>/<id>.pdf                         → kind=pdf
//	markdown/<yymm>/<id>.md                     → kind=markdown
//	json/<yymm>/<id>.json                       → kind=json
//	images/<yymm>/<id>/<anything>               → kind=images
//
// Lives in the paperindex package (not routes / paperassets) so both
// the webhook event handler and the bootstrap-index subcommand can
// share one implementation. paperassets owns the *writing* side of
// the key convention; this owns the *parsing* side (inverse).
func ParseAssetKey(key string) (kind AssetKind, arxivID, yymm string, ok bool) {
	parts := strings.Split(key, "/")
	if len(parts) < 3 {
		return "", "", "", false
	}
	switch AssetKind(parts[0]) {
	case KindPDF, KindMarkdown, KindJSON:
		if len(parts) != 3 {
			return "", "", "", false
		}
		yymm = parts[1]
		base := parts[2]
		stem := strings.TrimSuffix(base, path.Ext(base))
		if stem == "" {
			return "", "", "", false
		}
		return AssetKind(parts[0]), stem, yymm, true
	case KindImages:
		if len(parts) < 4 {
			return "", "", "", false
		}
		return KindImages, parts[2], parts[1], true
	}
	return "", "", "", false
}

// FetchArxivMetadata downloads json/<…>.json from the bucket and
// extracts title / abstract / authors / categories / submitter /
// update_date. Returns ok=false on any failure — caller falls back
// to the metadata-less UpsertJSONAsset path.
//
// Body is capped at 2 MiB (arxiv metadata jsons run ~3-5 KB in
// practice; the cap is paranoid against malformed inputs).
//
// The schema here mirrors what was observed in the production bucket
// (see PoC peek_json.py): top-level fields title / abstract / authors
// / categories / submitter / update_date / authors_parsed / etc.
// Missing fields produce empty strings / zero-times, which the
// paperindex Upsert path COALESCEs against existing values rather
// than clobbering them — partial payloads are safe to apply.
//
// authors here is the joined string field ("J. Doe, A. Smith"). The
// `authors_parsed` list-of-list shape (a.k.a. [[surname, given,
// suffix], ...]) is intentionally not consumed — joined string is
// good enough for the parquet column and avoids LIST<VARCHAR> schema
// complexity downstream.
func FetchArxivMetadata(ctx context.Context, store objstore.Store, key string) (JSONMetadata, bool) {
	rc, _, err := store.Get(ctx, key)
	if err != nil {
		return JSONMetadata{}, false
	}
	defer rc.Close()
	body, err := io.ReadAll(io.LimitReader(rc, 2<<20))
	if err != nil {
		return JSONMetadata{}, false
	}
	var parsed struct {
		Title      string `json:"title"`
		Abstract   string `json:"abstract"`
		Authors    string `json:"authors"`
		Categories string `json:"categories"`
		Submitter  string `json:"submitter"`
		UpdateDate string `json:"update_date"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return JSONMetadata{}, false
	}
	md := JSONMetadata{
		Title:      strings.TrimSpace(parsed.Title),
		Abstract:   strings.TrimSpace(parsed.Abstract),
		Authors:    strings.TrimSpace(parsed.Authors),
		Categories: strings.TrimSpace(parsed.Categories),
		Submitter:  strings.TrimSpace(parsed.Submitter),
	}
	if parsed.UpdateDate != "" {
		if t, err := time.Parse("2006-01-02", parsed.UpdateDate); err == nil {
			md.UpdateDate = t
		}
	}
	return md, true
}

// BootstrapOptions tunes a full-bucket Bootstrap scan.
type BootstrapOptions struct {
	// Concurrency caps the number of in-flight LIST + GET operations.
	// RustFS-beta-5 returns HTTP 500 under heavy concurrent LIST,
	// so 4 is the empirical safe default (PoC python scrape data
	// point). Bumping above 8 is risky against current RustFS.
	Concurrency int

	// EnrichJSON controls whether json/<id>.json objects are
	// fetched + parsed for title/abstract/etc. Set false for a
	// faster (and cheaper) "asset existence only" scan, e.g. for
	// disaster recovery where metadata can be filled in later.
	EnrichJSON bool

	// OnProgress is invoked after each (kind, yymm) batch with
	// human-readable counters. nil = no progress reporting.
	OnProgress func(stage string, doneBatches, totalBatches int, papersTouched int)
}

// BootstrapResult summarises a Bootstrap run for the operator.
type BootstrapResult struct {
	Duration       time.Duration
	BatchesTotal   int           // discovered (kind, yymm) tuples
	BatchesOK      int           // batches that completed without LIST error
	BatchesErrored int           // batches that returned an error (likely RustFS 500)
	BatchErrors    []BatchError  // first 10 errors for triage
	PapersTouched  int           // unique arxiv_ids upserted
	MetadataParsed int           // successful FetchArxivMetadata calls (≤ jsons listed)
	MetadataMissed int           // jsons listed but FetchArxivMetadata returned ok=false
	PerKindObjects map[AssetKind]int
}

// BatchError captures one (kind, yymm) LIST failure for the result log.
type BatchError struct {
	Kind  AssetKind
	YYMM  string
	Error string
}

// Bootstrap performs a one-shot full-bucket scan and upserts every
// discovered asset into the in-memory papers table. Intended for:
//
//  1. First-time deployments (no parquet exists yet — Bootstrap
//     creates it from scratch).
//  2. Disaster recovery when paperindex drifted from bucket state
//     (e.g. someone uploaded via mc bypassing the webhook flow, or
//     the parquet got corrupted).
//  3. Backfilling new metadata columns (e.g. when the schema added
//     title/abstract and 134k legacy rows are still NULL on those
//     fields).
//
// **MUST be run against a paused or stopped qatlas-server**: while
// the in-memory state we build is fine, the flusher goroutine
// concurrently writing parquet via CAS during a bootstrap will fight
// the bootstrap's final flush. Stop the service first, run bootstrap
// (typically 1-2 hours for 10^5 papers), then start the service.
//
// At the end Bootstrap triggers a synchronous final flush so the
// parquet on disk reflects the full scan result before returning.
// Subsequent qatlas startups will Load this fresh parquet and serve
// it as the authoritative state.
//
// Recommended invocation pattern (in a cobra subcommand):
//
//	rawStore, _ := objstore.NewS3StoreDual(...)
//	store, _ := paperindex.New(ctx, paperindex.Config{Store: rawStore})
//	defer store.Close()  // triggers final flush
//	result, err := store.Bootstrap(ctx, rawStore, paperindex.BootstrapOptions{
//	    Concurrency: 4, EnrichJSON: true,
//	    OnProgress:  func(stage string, done, total, papers int) {
//	        log.Printf("[%s] %d/%d batches, %d papers", stage, done, total, papers)
//	    },
//	})
func (s *Store) Bootstrap(ctx context.Context, rawStore objstore.Store, opts BootstrapOptions) (BootstrapResult, error) {
	if opts.Concurrency <= 0 {
		opts.Concurrency = 4
	}

	t0 := time.Now()
	res := BootstrapResult{PerKindObjects: map[AssetKind]int{}}

	kinds := []AssetKind{KindPDF, KindMarkdown, KindJSON, KindImages}

	// Phase 1: discover yymms per kind via delimiter=/ list at top.
	// This is cheap — one LIST per kind, ~200 yymms total.
	yymmsByKind := map[AssetKind][]string{}
	for _, k := range kinds {
		yymms, err := discoverYYMMs(ctx, rawStore, k)
		if err != nil {
			return res, fmt.Errorf("discover yymms for %s: %w", k, err)
		}
		yymmsByKind[k] = yymms
		if opts.OnProgress != nil {
			opts.OnProgress(fmt.Sprintf("discover-%s", k), len(yymms), len(yymms), 0)
		}
	}

	// Compute total batches.
	totalBatches := 0
	for _, yymms := range yymmsByKind {
		totalBatches += len(yymms)
	}
	res.BatchesTotal = totalBatches

	// Phase 2: sequentially per kind, per-yymm LIST (with
	// concurrency cap inside each kind's batch loop). We do kinds
	// sequentially so progress logs read linearly and to avoid
	// cross-kind LIST flooding on RustFS.
	doneBatches := 0
	papersTouched := map[string]struct{}{}

	type listResult struct {
		kind  AssetKind
		yymm  string
		objs  []objstore.ObjectInfo
		err   error
	}

	for _, kind := range kinds {
		yymms := yymmsByKind[kind]
		if len(yymms) == 0 {
			continue
		}
		// Bounded-concurrency worker pool over yymms for this kind.
		jobs := make(chan string, len(yymms))
		results := make(chan listResult, len(yymms))
		for _, y := range yymms {
			jobs <- y
		}
		close(jobs)
		for w := 0; w < opts.Concurrency; w++ {
			go func() {
				for yymm := range jobs {
					objs, err := rawStore.ListPrefix(ctx, fmt.Sprintf("%s/%s/", kind, yymm), 0)
					results <- listResult{kind: kind, yymm: yymm, objs: objs, err: err}
				}
			}()
		}

		for i := 0; i < len(yymms); i++ {
			r := <-results
			doneBatches++
			if r.err != nil {
				res.BatchesErrored++
				if len(res.BatchErrors) < 10 {
					res.BatchErrors = append(res.BatchErrors, BatchError{
						Kind: r.kind, YYMM: r.yymm, Error: r.err.Error(),
					})
				}
				continue
			}
			res.BatchesOK++
			res.PerKindObjects[r.kind] += len(r.objs)
			for _, obj := range r.objs {
				if err := s.applyDiscovered(ctx, rawStore, r.kind, obj, opts.EnrichJSON, &res); err != nil {
					// Per-object apply errors are non-fatal; the
					// rest of the scan continues. Track for the
					// result summary but don't abort.
					if len(res.BatchErrors) < 10 {
						res.BatchErrors = append(res.BatchErrors, BatchError{
							Kind: r.kind, YYMM: r.yymm, Error: fmt.Sprintf("apply %s: %v", obj.Key, err),
						})
					}
					continue
				}
				papersTouched[stemFromKey(obj.Key, r.kind)] = struct{}{}
			}
			if opts.OnProgress != nil {
				opts.OnProgress(fmt.Sprintf("scan-%s", kind), doneBatches, totalBatches, len(papersTouched))
			}
		}
		close(results)
	}

	res.PapersTouched = len(papersTouched)
	res.Duration = time.Since(t0)

	// Final synchronous flush so the new parquet lands before we
	// return — caller doesn't have to wait on the 5s flushLoop tick.
	if err := s.flush(ctx); err != nil {
		return res, fmt.Errorf("final flush: %w", err)
	}
	return res, nil
}

// applyDiscovered routes one discovered object to the right Store
// upsert method. For json kind, optionally fetches metadata first.
func (s *Store) applyDiscovered(
	ctx context.Context,
	rawStore objstore.Store,
	kind AssetKind,
	obj objstore.ObjectInfo,
	enrichJSON bool,
	res *BootstrapResult,
) error {
	switch kind {
	case KindPDF:
		k, arxivID, yymm, ok := ParseAssetKey(obj.Key)
		if !ok || k != KindPDF {
			return nil
		}
		return s.UpsertPDFAsset(ctx, arxivID, yymm, obj.Size, obj.UpdatedAt, obj.ETag)
	case KindMarkdown:
		k, arxivID, yymm, ok := ParseAssetKey(obj.Key)
		if !ok || k != KindMarkdown {
			return nil
		}
		return s.UpsertMDAsset(ctx, arxivID, yymm, obj.Size, obj.UpdatedAt, obj.ETag)
	case KindJSON:
		k, arxivID, yymm, ok := ParseAssetKey(obj.Key)
		if !ok || k != KindJSON {
			return nil
		}
		if !enrichJSON {
			return s.UpsertJSONAsset(ctx, arxivID, yymm, obj.Size, obj.UpdatedAt, obj.ETag)
		}
		md, mdOK := FetchArxivMetadata(ctx, rawStore, obj.Key)
		if !mdOK {
			res.MetadataMissed++
			return s.UpsertJSONAsset(ctx, arxivID, yymm, obj.Size, obj.UpdatedAt, obj.ETag)
		}
		res.MetadataParsed++
		return s.UpsertJSONMetadata(ctx, arxivID, yymm, obj.Size, obj.UpdatedAt, obj.ETag, md)
	case KindImages:
		_, arxivID, yymm, ok := ParseAssetKey(obj.Key)
		if !ok {
			return nil
		}
		// images event = +1 per file. Bootstrap path is full scan,
		// so we count each image once which is the desired total.
		return s.AdjustImageCount(ctx, arxivID, yymm, 1)
	}
	return nil
}

// discoverYYMMs lists immediate children under "<kind>/" with
// delimiter=/ to enumerate per-month sub-prefixes. Falls back to
// recursive list + dedup if the backend doesn't support delimiter
// listing (LocalStore does; S3 / RustFS do).
//
// For S3Store: this is a single ListObjectsV2 with delimiter and
// returns ~200 CommonPrefixes — far cheaper than recursive scan.
func discoverYYMMs(ctx context.Context, rawStore objstore.Store, kind AssetKind) ([]string, error) {
	// objstore.Store doesn't expose delimiter parameter, so we
	// recursive-list and dedup the second path segment. On S3 this
	// is slower than a delimiter scan (the protocol returns all
	// objects rather than just sub-prefixes), but it works
	// uniformly across backends and is one-shot ("cheap enough" at
	// bootstrap cadence — not in the hot path).
	objs, err := rawStore.ListPrefix(ctx, string(kind)+"/", 0)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	for _, o := range objs {
		parts := strings.SplitN(o.Key, "/", 3)
		if len(parts) < 2 {
			continue
		}
		yymm := parts[1]
		if yymm == "" {
			continue
		}
		seen[yymm] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for y := range seen {
		out = append(out, y)
	}
	return out, nil
}

// stemFromKey extracts the arxiv_id used by ParseAssetKey, so
// Bootstrap can dedup "papers touched" across kinds without
// re-parsing.
func stemFromKey(key string, kind AssetKind) string {
	_, arxivID, _, ok := ParseAssetKey(key)
	if !ok {
		return ""
	}
	return arxivID
}

// ensure types are exported / linter has something to chew on.
var _ = errors.New
