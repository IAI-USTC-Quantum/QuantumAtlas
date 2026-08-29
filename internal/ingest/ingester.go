// Package ingest is the lazy-ingestion pipeline that turns a freshly
// minted 'pending' paper into a fetched arXiv PDF asset.
//
// The search engine mints papers with status 'pending' and fires its
// onMint hook (see internal/search.Engine); Ingester.OnMint matches that
// hook signature exactly. Each notification is coalesced by paper_id
// (singleflight) and handed to a small bounded worker pool that:
//
//  1. skips refs without an arXiv id (DOI-only refs have nothing to
//     fetch from arXiv — the paper stays 'pending'),
//  2. parses the id and resolves the latest version when the ref came
//     in unversioned (OpenAlex landing_page_url never carries vN),
//  3. fetches the PDF via internal/arxiv (rate-limited, byte-immutable
//     versioned URL),
//  4. writes the bytes to the paper-assets object store under the
//     canonical AssetKey ("pdf/<yymm>/<stem>.pdf"),
//  5. records the asset via registry.UpsertPDF with the bucket-relative
//     path ("<yymm>/<stem>.pdf"), which flips the paper to 'ready'.
//
// Failures mark the paper 'failed' and log a warning; there are no
// retries here — a later janitor pass can re-drive failed papers.
package ingest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"

	"golang.org/x/sync/singleflight"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/safego"
)

// DefaultConcurrency is the worker-pool size when WithConcurrency is
// not given. Fetch throughput is bounded far more by the arxiv
// fetcher's global rate limit than by this number.
const DefaultConcurrency = 2

// registryWriter is the slice of *registry.Store the ingester needs,
// factored out (as internal/search did with minter) so tests can fake
// the catalog without a database. *registry.Store satisfies it
// implicitly.
type registryWriter interface {
	UpsertPDF(ctx context.Context, ref registry.PaperRef, version int, sha256 string, size int64, pdfPath string) (paperID string, assetID int64, err error)
	UpdateStatus(ctx context.Context, paperID, status string) (found bool, err error)
}

// assetPutter is the slice of objstore.Store the ingester needs. Both
// *objstore.Router (production: routes the "pdf/" prefix to the pdf
// bucket) and *objstore.LocalStore (dev) satisfy it implicitly.
type assetPutter interface {
	Put(ctx context.Context, key string, r io.Reader, size int64, contentType string) (int64, error)
}

// IndexPusher is the slice of the qatlas-rag client (internal/rag) the
// ingester needs, factored out so tests can fake the push.
// *rag.RemoteClient satisfies it implicitly.
type IndexPusher interface {
	PushIndex(ctx context.Context, paperID string) error
}

// Option configures an Ingester.
type Option func(*Ingester)

// WithConcurrency sets the worker-pool size (number of papers ingested
// in parallel). Values < 1 fall back to DefaultConcurrency.
func WithConcurrency(n int) Option {
	return func(i *Ingester) {
		if n >= 1 {
			i.concurrency = n
		}
	}
}

// WithLogger installs a logger; nil keeps the default (slog.Default).
func WithLogger(l *slog.Logger) Option {
	return func(i *Ingester) {
		if l != nil {
			i.log = l
		}
	}
}

// WithIndexPusher installs the qatlas-rag index-push client. After a
// paper flips to 'ready' the ingester pushes an index build for it;
// the push is best-effort (failures are logged, never fatal). nil
// (the default) disables the push.
func WithIndexPusher(p IndexPusher) Option {
	return func(i *Ingester) {
		if p != nil {
			i.pusher = p
		}
	}
}

// Ingester is the lazy-ingestion pipeline. Construct with New; the
// worker pool starts immediately. Safe for concurrent use.
type Ingester struct {
	reg         registryWriter
	fetcher     *arxiv.Fetcher
	store       assetPutter
	pusher      IndexPusher
	log         *slog.Logger
	concurrency int

	jobs chan job
	done chan struct{}

	sf        singleflight.Group
	wg        sync.WaitGroup
	closeOnce sync.Once
	closed    atomic.Bool

	queued   atomic.Int64
	inFlight atomic.Int64
	fetched  atomic.Int64
	failed   atomic.Int64
	skipped  atomic.Int64
}

// job is one pending paper handed to the worker pool. done is closed
// by the worker when processing finishes — it is what keeps the
// singleflight entry alive for the whole queue+fetch window so
// duplicate OnMint calls coalesce onto the same run.
type job struct {
	paperID string
	ref     registry.PaperRef
	ctx     context.Context // detached from the caller's cancellation
	done    chan struct{}
}

// New builds an Ingester over the registry, arXiv fetcher, and the
// paper-assets object store (what main.go passes as rawStore — an
// *objstore.Router in production, *objstore.LocalStore in dev). A nil
// fetcher disables ingestion entirely: OnMint becomes a no-op and
// minted papers stay 'pending'.
func New(reg *registry.Store, fetcher *arxiv.Fetcher, store objstore.Store, opts ...Option) *Ingester {
	return newIngester(reg, fetcher, store, opts...)
}

// newIngester is the test-constructible core; the public New pins reg
// and store to the production types.
func newIngester(reg registryWriter, fetcher *arxiv.Fetcher, store assetPutter, opts ...Option) *Ingester {
	i := &Ingester{
		reg:         reg,
		fetcher:     fetcher,
		store:       store,
		log:         slog.Default(),
		concurrency: DefaultConcurrency,
		jobs:        make(chan job, 64),
		done:        make(chan struct{}),
	}
	for _, opt := range opts {
		opt(i)
	}
	for w := 0; w < i.concurrency; w++ {
		i.wg.Add(1)
		go func() {
			defer i.wg.Done()
			defer func() {
				if r := recover(); r != nil {
					safego.LogPanic("ingest.worker", r)
				}
			}()
			i.worker()
		}()
	}
	return i
}

// OnMint matches the search engine's onMint hook signature. It never
// blocks the caller: the first call for a paper_id enqueues ingestion
// (bounded by the worker pool); concurrent duplicates coalesce via
// singleflight and return immediately.
func (i *Ingester) OnMint(ctx context.Context, paperID string, ref registry.PaperRef) {
	if i.fetcher == nil || i.closed.Load() {
		return
	}
	i.sf.DoChan(paperID, func() (any, error) {
		j := job{
			paperID: paperID,
			ref:     ref,
			ctx:     context.WithoutCancel(ctx),
			done:    make(chan struct{}),
		}
		select {
		case i.jobs <- j:
			i.queued.Add(1)
			// Keep the flight alive until the worker finishes so a
			// duplicate OnMint during queue+fetch coalesces here.
			<-j.done
		case <-i.done:
		}
		return nil, nil
	})
}

// worker drains the job queue until Shutdown, then drains whatever is
// left before exiting. The WaitGroup is owned by the spawn site in
// newIngester (which also recovers panics via safego.LogPanic).
func (i *Ingester) worker() {
	for {
		select {
		case j := <-i.jobs:
			i.process(j)
		case <-i.done:
			for {
				select {
				case j := <-i.jobs:
					i.process(j)
				default:
					return
				}
			}
		}
	}
}

// process runs the fetch→store→catalog pipeline for one paper. It
// always closes j.done (releasing the singleflight window) and always
// resolves the paper to a terminal state itself: success flips to
// 'ready' via UpsertPDF, failure marks 'failed', unfetchable refs stay
// 'pending' untouched.
func (i *Ingester) process(j job) {
	defer close(j.done)
	defer i.sf.Forget(j.paperID)
	i.inFlight.Add(1)
	defer i.inFlight.Add(-1)

	log := i.log.With("paper_id", j.paperID)
	ctx := j.ctx

	if j.ref.ArxivID == "" {
		// DOI-only refs can't be fetched from arXiv; leave the paper
		// 'pending' for some other contribution path.
		i.skipped.Add(1)
		log.Debug("ingest: no arxiv id; leaving paper pending")
		return
	}

	parsed, err := paperassets.Parse(j.ref.ArxivID)
	if err != nil {
		i.fail(ctx, log, j.paperID, "parse", err)
		return
	}
	if parsed.Version == "" {
		parsed, err = i.fetcher.ResolveLatestVersion(ctx, parsed)
		if err != nil {
			i.fail(ctx, log, j.paperID, "resolve-version", err)
			return
		}
	}
	version := registry.ArxivVersionOf(parsed.Canonical)
	if version <= 0 {
		i.fail(ctx, log, j.paperID, "resolve-version", fmt.Errorf("no arxiv version resolved for %q", parsed.Canonical))
		return
	}

	res, err := i.fetcher.Fetch(ctx, parsed)
	if err != nil {
		i.fail(ctx, log, j.paperID, "fetch", err)
		return
	}

	assetKey := paperassets.AssetKeyFor("pdf", parsed)
	if assetKey == "" {
		i.fail(ctx, log, j.paperID, "asset-key", fmt.Errorf("no pdf asset key for %q", parsed.Canonical))
		return
	}
	if _, err := i.store.Put(ctx, assetKey, res.Body, res.Size, "application/pdf"); err != nil {
		i.fail(ctx, log, j.paperID, "store-put", err)
		return
	}

	// paper_assets.pdf_path is bucket-relative (the legacy
	// bucketRelKey convention): strip the leading "pdf/" kind segment
	// the Router re-adds on write.
	ref := j.ref
	ref.ArxivID = parsed.Canonical
	if _, _, err := i.reg.UpsertPDF(ctx, ref, version, res.Sha256, res.Size, bucketRelKey(assetKey)); err != nil {
		i.fail(ctx, log, j.paperID, "upsert-pdf", err)
		return
	}

	// The paper is 'ready' now: push the index build to qatlas-rag.
	// Best-effort — a push failure must not retro-fail the ingest.
	if i.pusher != nil {
		if err := i.pusher.PushIndex(ctx, parsed.Canonical); err != nil {
			log.Warn("ingest: rag index push failed", "arxiv_id", parsed.Canonical, "error", err)
		}
	}

	i.fetched.Add(1)
	log.Info("ingest: pdf ingested",
		"arxiv_id", parsed.Canonical,
		"version", version,
		"size", res.Size,
		"attempts", res.Attempts,
	)
}

// fail marks the paper 'failed' and counts the failure. A failure to
// update the status is logged but not otherwise surfaced — the paper
// stays in whatever state the catalog has.
func (i *Ingester) fail(ctx context.Context, log *slog.Logger, paperID, stage string, err error) {
	i.failed.Add(1)
	log.Warn("ingest: failed", "stage", stage, "error", err)
	if _, uerr := i.reg.UpdateStatus(ctx, paperID, "failed"); uerr != nil {
		log.Warn("ingest: could not mark paper failed", "error", uerr)
	}
}

// Shutdown stops accepting new work and drains the queue: workers
// finish the in-flight and queued jobs, then exit. It blocks until the
// pool is drained or ctx expires.
func (i *Ingester) Shutdown(ctx context.Context) error {
	i.closeOnce.Do(func() {
		i.closed.Store(true)
		close(i.done)
	})
	drained := make(chan struct{})
	go func() {
		i.wg.Wait()
		close(drained)
	}()
	select {
	case <-drained:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Snapshot returns the ingester's counters for healthz. queued /
// fetched / failed / skipped are cumulative; in_flight is the current
// number of papers being processed.
func (i *Ingester) Snapshot() map[string]int64 {
	return map[string]int64{
		"queued":    i.queued.Load(),
		"in_flight": i.inFlight.Load(),
		"fetched":   i.fetched.Load(),
		"failed":    i.failed.Load(),
		"skipped":   i.skipped.Load(),
	}
}

// bucketRelKey strips the leading "<kind>/" segment from an AssetKey,
// yielding the object key relative to a per-kind bucket, e.g.
// "pdf/9508/quant-ph/9508027v1.pdf" -> "9508/quant-ph/9508027v1.pdf".
// This mirrors the convention used by the legacy catalog
// (internal/papers bucketRelKey) and registry.UpsertMDByDOI.
func bucketRelKey(assetKey string) string {
	if i := strings.IndexByte(assetKey, '/'); i >= 0 {
		return assetKey[i+1:]
	}
	return assetKey
}
