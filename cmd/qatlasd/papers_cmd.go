// Command-line surface for the PostgreSQL paper registry.
//
// `papers sync` reconciles the registry's per-paper asset state
// (PDF / markdown pointers) against the actual objects in the
// per-kind object-store buckets (qatlas-pdf / qatlas-md). It is the
// safety net: if a write-through failed
// because PostgreSQL was momentarily down during an upload
// (the handler still 201s the S3 write and sets X-Catalog-Sync:
// deferred), a later `papers sync` re-resolves the paper from the bucket
// listing so the registry converges. Image counts are NOT synced —
// paper_assets.image_count is populated from MinerU upload metadata, and
// the actual image file list is served on demand from the images bucket
// (GET /api/papers/{paper_id}/images).
//
// It is also the disaster-recovery path: after a registry wipe, the asset
// layer can be rebuilt one-to-one from the buckets via
// `papers sync --full --from-rustfs`. (OpenAlex metadata — titles, authors,
// citations — is rebuilt separately via the `openalex` subcommand; sync only
// attaches asset state.)
//
// Sync is safe to run while the server is live: every paper a pdf listing
// introduces is minted through ResolveOrMint and every asset upsert is
// idempotent, so a concurrent write-through and a sync converge.
//
// `papers backfill-metadata` is the metadata companion: sync mints papers
// from bucket listings with only an external id (no title/authors), and
// backfill fills title / authors / publication_date / abstract from the
// arXiv Atom API (--source=arxiv, by arxiv_id) or the OpenAlex works API
// (--source=openalex, by DOI), claiming the reported DOI / arXiv id when
// it is free.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/arxiv"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/spf13/cobra"
)

type papersSyncFlags struct {
	full       bool
	fromRustFS bool
	dryRun     bool
	batchSize  int
	kinds      string
	shardRange string
}

// NewPapersCommand mounts the `papers` subcommand group on the
// PocketBase root cobra command. Mirrors NewStorageCommand structure.
func NewPapersCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "papers",
		Short: "Maintenance operations on the PostgreSQL paper registry",
		Long: `Registry-side maintenance commands.

These commands require postgres.dsn and the s3 section in config.yaml
(the per-kind buckets the registry reconciles against).`,
	}
	root.AddCommand(newPapersSyncCmd())
	root.AddCommand(newPapersBackfillMetadataCmd())
	return root
}

func newPapersSyncCmd() *cobra.Command {
	var f papersSyncFlags
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile registry asset state (pdf/markdown) from the buckets",
		Long: `		Walk the per-kind object-store buckets and upsert each paper's
		asset state (pdf / mineru markdown) into PostgreSQL.

This repairs drift from deferred write-throughs (PostgreSQL was down during
an upload) and rebuilds the asset layer after a registry wipe. The images
bucket is not listed: image counts come from MinerU upload metadata.

Examples:
  # Reconcile everything from the buckets (disaster rebuild)
  qatlasd papers sync --full --from-rustfs

  # Preview the diff without writing to PostgreSQL
  qatlasd papers sync --full --from-rustfs --dry-run

  # Reconcile only pdfs in shards 0001..0912 (the doi pseudo-shard
  # always runs with its kind)
  qatlasd papers sync --full --from-rustfs --kinds=pdf --shard-range=0001:0912
`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPapersSync(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().BoolVar(&f.full, "full", false, "scan the whole bucket (currently the only supported mode)")
	cmd.Flags().BoolVar(&f.fromRustFS, "from-rustfs", false, "reconcile against the object-store buckets (required)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "report the diff without writing to PostgreSQL")
	cmd.Flags().IntVar(&f.batchSize, "batch-size", 0, "retained for shape parity with the legacy catalog (0 = default)")
	cmd.Flags().StringVar(&f.kinds, "kinds", "", "comma-separated subset of kinds to reconcile: pdf,markdown (default both)")
	cmd.Flags().StringVar(&f.shardRange, "shard-range", "", "inclusive shard range FROM:TO (e.g. 0001:0912; either side may be empty = open-ended)")
	return cmd
}

func runPapersSync(stdout, stderr io.Writer, f papersSyncFlags) error {
	if !f.fromRustFS {
		return errors.New("papers sync currently only supports --from-rustfs (bucket reconcile)")
	}
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.S3Enabled() {
		return errors.New("papers sync requires the S3 backend (s3.* fully set in config.yaml)")
	}

	pgPool, err := initPostgresPool(cfg)
	if err != nil {
		return err
	}
	if pgPool == nil {
		return errors.New("papers sync requires postgres.dsn in config.yaml")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer pgPool.Close()

	if err := registry.Migrate(ctx, pgPool); err != nil {
		return fmt.Errorf("registry migrate: %w", err)
	}
	registryStore := registry.NewStore(pgPool)

	rawStore, err := initRawStore(cfg)
	if err != nil {
		return fmt.Errorf("init raw store: %w", err)
	}

	fmt.Fprintf(stderr, "Postgres: configured\n")
	fmt.Fprintf(stderr, "Buckets : %s (pdf=%s md=%s images=%s)\n",
		cfg.S3Endpoint, cfg.S3BucketPDF, cfg.S3BucketMD, cfg.S3BucketImages)
	fmt.Fprintf(stderr, "Mode    : %s\n", map[bool]string{true: "DRY-RUN", false: "APPLY"}[f.dryRun])
	fmt.Fprintln(stderr, "---")

	opts := registry.SyncOptions{
		DryRun:    f.dryRun,
		BatchSize: f.batchSize,
	}
	if f.kinds != "" {
		for _, k := range strings.Split(f.kinds, ",") {
			if k = strings.TrimSpace(k); k != "" {
				opts.Kinds = append(opts.Kinds, k)
			}
		}
	}
	if f.shardRange != "" {
		parts := strings.SplitN(f.shardRange, ":", 2)
		if len(parts) != 2 {
			return errors.New("--shard-range must be FROM:TO (either side may be empty, e.g. 0001:0912 or 0906:)")
		}
		opts.ShardFrom, opts.ShardTo = parts[0], parts[1]
	}

	rep, err := registry.NewSyncer(registryStore).SyncFromStore(ctx, rawStore, opts)

	// Print the summary even when the pass ended with failed shards —
	// the operator needs the partial-progress numbers alongside the
	// non-zero exit.
	fmt.Fprintf(stdout, "pdf objects   : %d\n", rep.PDFObjects)
	fmt.Fprintf(stdout, "md objects    : %d\n", rep.MDObjects)
	fmt.Fprintf(stdout, "papers touched: %d\n", rep.PapersTouched)
	fmt.Fprintf(stdout, "shards done   : %d/%d\n", rep.ShardsDone, rep.ShardsTotal)
	if len(rep.FailedShards) > 0 {
		fmt.Fprintf(stdout, "failed shards : %d\n", len(rep.FailedShards))
		for _, fs := range rep.FailedShards {
			fmt.Fprintf(stdout, "  - %s: %s\n", fs.Shard, fs.Err)
		}
	}
	fmt.Fprintf(stdout, "duration      : %s\n", rep.Duration.Round(time.Millisecond))
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}
	return nil
}

type papersBackfillMetadataFlags struct {
	source string
	batch  int
	rps    float64
	limit  int
	dryRun bool
}

// applySourceDefaults retunes --batch / --rps for the OpenAlex source
// when the operator didn't set them explicitly: the arXiv defaults
// (100 ids, 1 req / 3s) are needlessly conservative against OpenAlex's
// polite pool (~10 req/s), and its OR-DOI filter pages best at 50.
func (f *papersBackfillMetadataFlags) applySourceDefaults(batchChanged, rpsChanged bool) {
	if f.source != "openalex" {
		return
	}
	if !batchChanged {
		f.batch = 50
	}
	if !rpsChanged {
		f.rps = 5
	}
}

// newPapersBackfillMetadataCmd backfills title / authors /
// publication_date / abstract (and claims the reported DOI / arXiv id
// when free) for the untitled papers `papers sync` minted from bucket
// listings. Idempotent and resumable: updated rows drop out of the
// untitled set, so a re-run (or a resumed run) only sees what's left.
func newPapersBackfillMetadataCmd() *cobra.Command {
	var f papersBackfillMetadataFlags
	cmd := &cobra.Command{
		Use:   "backfill-metadata",
		Short: "Backfill titles/authors/abstracts for untitled papers from the arXiv or OpenAlex API",
		Long: `Walk the untitled papers (minted by 'papers sync' from bucket
listings) and fill title, authors, publication_date and abstract from a
metadata source:

  --source=arxiv    (default) papers with an arxiv_id, via the arXiv Atom
                    API (batched id_list queries). When arXiv reports a
                    DOI and no other paper owns it, the DOI is claimed on
                    the paper plus a doi:<doi> identity row.
  --source=openalex papers with a DOI, via the OpenAlex works API (OR-DOI
                    filter batches; polite pool via paper_access.openalex_mailto
                    allows ~10 req/s, so DOI-bearing papers backfill much
                    faster than arXiv's aggressive 429s allow). When the
                    work links an arXiv id the paper doesn't have, it is
                    claimed the same guarded way (arxiv:<id> identity row).

External-id claims are guarded: an existing value is never overwritten
and a value owned by another paper is never claimed (no merging here).

Idempotent and resumable: backfilled papers leave the untitled set, so
an interrupted run is safe to resume by re-running the command.

Examples:
  # Preview what an arXiv run would touch
  qatlasd papers backfill-metadata --dry-run --limit=200

  # Full arXiv backfill (politeness default: 1 request / 3s, 100 ids each)
  qatlasd papers backfill-metadata

  # Fast pass over the DOI-bearing subset (default: 50 DOIs, 5 req/s)
  qatlasd papers backfill-metadata --source=openalex
`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			f.applySourceDefaults(cmd.Flags().Changed("batch"), cmd.Flags().Changed("rps"))
			return runPapersBackfillMetadata(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().StringVar(&f.source, "source", "arxiv", "metadata source: arxiv (papers with arxiv_id) | openalex (papers with doi)")
	cmd.Flags().IntVar(&f.batch, "batch", 100, "ids per API request (arXiv caps id_list at ~200; openalex default 50)")
	cmd.Flags().Float64Var(&f.rps, "rps", 0.33, "max API requests per second (politeness; openalex default 5)")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "cap the number of papers processed (0 = all)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "fetch and report without writing to PostgreSQL")
	return cmd
}

// backfillCounts accumulates the run counters shared by both sources.
type backfillCounts struct {
	processed    int
	updated      int
	missing      int // source didn't return the paper (or it had no title)
	doiClaimed   int
	arxivClaimed int
}

func runPapersBackfillMetadata(stdout, stderr io.Writer, f papersBackfillMetadataFlags) error {
	if f.source != "arxiv" && f.source != "openalex" {
		return fmt.Errorf("--source must be arxiv or openalex, got %q", f.source)
	}
	if f.batch < 1 {
		return errors.New("--batch must be >= 1")
	}
	if f.rps <= 0 {
		return errors.New("--rps must be > 0")
	}
	if f.limit < 0 {
		return errors.New("--limit must be >= 0")
	}
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	pgPool, err := initPostgresPool(cfg)
	if err != nil {
		return err
	}
	if pgPool == nil {
		return errors.New("papers backfill-metadata requires postgres.dsn in config.yaml")
	}
	defer pgPool.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := registry.Migrate(ctx, pgPool); err != nil {
		return fmt.Errorf("registry migrate: %w", err)
	}
	registryStore := registry.NewStore(pgPool)
	httpClient := &http.Client{Timeout: 30 * time.Second}

	fmt.Fprintf(stderr, "Postgres: configured\n")
	fmt.Fprintf(stderr, "Mode    : %s source=%s (batch=%d rps=%.2f limit=%d)\n",
		map[bool]string{true: "DRY-RUN", false: "APPLY"}[f.dryRun], f.source, f.batch, f.rps, f.limit)
	if f.source == "openalex" && cfg.OpenAlexMailto == "" {
		fmt.Fprintln(stderr, "WARN    : paper_access.openalex_mailto unset — OpenAlex anonymous pool is heavily rate-limited")
	}
	fmt.Fprintln(stderr, "---")

	var counts backfillCounts
	start := time.Now()
	ticker := time.NewTicker(time.Duration(float64(time.Second) / f.rps))
	defer ticker.Stop()

	if f.source == "openalex" {
		err = runOpenAlexBackfill(ctx, stderr, registryStore, httpClient, cfg.OpenAlexMailto, f, &counts, ticker)
	} else {
		err = runArxivBackfill(ctx, stderr, registryStore, httpClient, f, &counts, ticker)
	}
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		fmt.Fprintln(stderr, "interrupted; partial progress below (safe to resume)")
	}

	fmt.Fprintf(stdout, "processed     : %d\n", counts.processed)
	fmt.Fprintf(stdout, "updated       : %d\n", counts.updated)
	fmt.Fprintf(stdout, "still_untitled: %d\n", counts.missing)
	fmt.Fprintf(stdout, "doi_claimed   : %d\n", counts.doiClaimed)
	fmt.Fprintf(stdout, "arxiv_claimed : %d\n", counts.arxivClaimed)
	fmt.Fprintf(stdout, "duration      : %s\n", time.Since(start).Round(time.Millisecond))
	return nil
}

// runArxivBackfill is the --source=arxiv loop: keyset over untitled
// papers with an arxiv_id, batched arXiv id_list fetches, guarded
// per-paper update (+ DOI claim).
func runArxivBackfill(ctx context.Context, stderr io.Writer, store *registry.Store, httpClient *http.Client,
	f papersBackfillMetadataFlags, counts *backfillCounts, ticker *time.Ticker) error {
	var after string
	start := time.Now()
loop:
	for {
		batchSize := f.batch
		if f.limit > 0 {
			if remaining := f.limit - counts.processed; remaining < batchSize {
				batchSize = remaining
			}
		}
		if batchSize <= 0 {
			break
		}
		page, err := store.ListUntitled(ctx, after, batchSize)
		if err != nil {
			return fmt.Errorf("list untitled: %w", err)
		}
		if len(page) == 0 {
			break
		}

		ids := make([]string, len(page))
		for i, p := range page {
			ids[i] = p.ArxivID
		}
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
		}
		md, err := fetchWithRetry(ctx, stderr, len(page), func() (map[string]arxiv.EntryMetadata, error) {
			return arxiv.FetchMetadataBatch(ctx, httpClient, ids)
		})
		if ctx.Err() != nil {
			break loop
		}
		if err != nil {
			// Batch skipped: advance the keyset cursor past it so the run
			// doesn't loop on the same failing page.
			for _, p := range page {
				counts.processed++
				counts.missing++
				after = p.PaperID
			}
			continue
		}

		for _, p := range page {
			counts.processed++
			after = p.PaperID // keyset cursor advances past every row seen
			m, ok := md[p.ArxivID]
			if !ok || m.Title == "" {
				counts.missing++
				continue
			}
			if f.dryRun {
				counts.updated++
				if m.DOI != "" {
					counts.doiClaimed++ // upper bound: the claim is skipped when owned elsewhere
				}
				continue
			}
			claims, err := store.UpdateMetadata(ctx, p.PaperID, registry.Metadata{
				Title:           m.Title,
				Authors:         m.Authors,
				Year:            m.Published.Year(),
				PublicationDate: m.Published,
				Abstract:        m.Abstract,
				DOI:             m.DOI,
			})
			if err != nil {
				return fmt.Errorf("update metadata %s: %w", p.PaperID, err)
			}
			counts.updated++
			if claims.DOIClaimed {
				counts.doiClaimed++
			}
		}
		fmt.Fprintf(stderr, "processed=%d updated=%d doi_claimed=%d arxiv_claimed=%d missing=%d elapsed=%s\n",
			counts.processed, counts.updated, counts.doiClaimed, counts.arxivClaimed, counts.missing,
			time.Since(start).Round(time.Second))
	}
	return nil
}

// runOpenAlexBackfill is the --source=openalex loop: keyset over
// untitled papers with a DOI, batched OpenAlex OR-DOI fetches, guarded
// per-paper update (+ arXiv id claim when the work links one the paper
// lacks). The paper's own DOI is never re-claimed (it already owns it).
func runOpenAlexBackfill(ctx context.Context, stderr io.Writer, store *registry.Store, httpClient *http.Client,
	mailto string, f papersBackfillMetadataFlags, counts *backfillCounts, ticker *time.Ticker) error {
	var after string
	start := time.Now()
loop:
	for {
		batchSize := f.batch
		if f.limit > 0 {
			if remaining := f.limit - counts.processed; remaining < batchSize {
				batchSize = remaining
			}
		}
		if batchSize <= 0 {
			break
		}
		page, err := store.ListUntitledDOI(ctx, after, batchSize)
		if err != nil {
			return fmt.Errorf("list untitled doi: %w", err)
		}
		if len(page) == 0 {
			break
		}

		dois := make([]string, len(page))
		for i, p := range page {
			dois[i] = p.DOI
		}
		select {
		case <-ctx.Done():
			break loop
		case <-ticker.C:
		}
		md, err := fetchWithRetry(ctx, stderr, len(page), func() (map[string]openalex.WorkMetadata, error) {
			return openalex.FetchWorksByDOIBatch(ctx, httpClient, mailto, dois)
		})
		if ctx.Err() != nil {
			break loop
		}
		if err != nil {
			// Batch skipped: advance the keyset cursor past it (resumable).
			for _, p := range page {
				counts.processed++
				counts.missing++
				after = p.PaperID
			}
			continue
		}

		for _, p := range page {
			counts.processed++
			after = p.PaperID // keyset cursor advances past every row seen
			m, ok := md[p.DOI]
			if !ok || m.Title == "" {
				counts.missing++
				continue
			}
			if f.dryRun {
				counts.updated++
				if m.ArxivID != "" {
					counts.arxivClaimed++ // upper bound: claim is skipped when set/owned
				}
				continue
			}
			claims, err := store.UpdateMetadata(ctx, p.PaperID, registry.Metadata{
				Title:           m.Title,
				Authors:         m.Authors,
				Year:            m.PublicationDate.Year(),
				PublicationDate: m.PublicationDate,
				Abstract:        m.Abstract,
				ArxivID:         m.ArxivID,
			})
			if err != nil {
				return fmt.Errorf("update metadata %s: %w", p.PaperID, err)
			}
			counts.updated++
			if claims.ArxivClaimed {
				counts.arxivClaimed++
			}
		}
		fmt.Fprintf(stderr, "processed=%d updated=%d doi_claimed=%d arxiv_claimed=%d missing=%d elapsed=%s\n",
			counts.processed, counts.updated, counts.doiClaimed, counts.arxivClaimed, counts.missing,
			time.Since(start).Round(time.Second))
	}
	return nil
}

// fetchWithRetry wraps one batch fetch. Batch fetches fail transiently
// (proxy resets, arXiv 429 / OpenAlex 5xx): retry with backoff, then
// report the error so the caller skips the batch instead of aborting an
// hours-long run — skipped papers stay untitled and are picked up by a
// later invocation (the command is resumable by design).
func fetchWithRetry[T any](ctx context.Context, stderr io.Writer, batchLen int, fetch func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; ; attempt++ {
		md, err := fetch()
		if err == nil {
			return md, nil
		}
		if ctx.Err() != nil {
			return zero, err
		}
		if attempt >= 2 {
			fmt.Fprintf(stderr, "WARN batch fetch failed after 3 attempts, skipping %d papers (re-run to retry): %v\n", batchLen, err)
			return zero, err
		}
		select {
		case <-ctx.Done():
			return zero, err
		case <-time.After(time.Duration(5*(attempt+1)) * time.Second):
		}
	}
}

var _ = os.Stdout
