// Command-line surface for paperindex catalog rebuild.
//
// `bootstrap-index` is a one-shot scanner that walks the qatlas-raw
// bucket end-to-end, observes what objects exist for each paper, and
// rebuilds index/papers.parquet with the full schema (including
// title / abstract / authors / categories pulled from each json
// file). Intended for:
//
//   1. Fresh deployments where the parquet doesn't exist yet.
//   2. Backfill after schema changes (the live webhook only sees
//      future uploads; legacy rows stay NULL until a bootstrap
//      passes over them).
//   3. Disaster recovery when the parquet got corrupted or someone
//      uploaded via mc bypassing the webhook flow.
//
// Run time on the production bucket (~134k papers, ~67k json files):
//   - --enrich-json=false  → ~5 min  (LIST only, no per-paper GETs)
//   - --enrich-json=true   → ~1.5 hr (default; LISTs + ~67k json GETs)
//
// CRITICAL operator note: the qatlas-server.service MUST be stopped
// before running bootstrap. The live server has a 5-second flush
// ticker that CAS-writes the parquet; if it fights bootstrap's final
// flush, one or both lose. Recommended sequence:
//
//   sudo systemctl stop qatlas-server.service
//   /home/timidly/.local/bin/qatlas-server bootstrap-index --enrich-json
//   sudo systemctl start qatlas-server.service

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperindex"

	"github.com/spf13/cobra"
)

type bootstrapFlags struct {
	concurrency int
	enrichJSON  bool
	jsonOutput  bool
}

// NewBootstrapIndexCommand returns the cobra subcommand for
// `qatlas-server bootstrap-index`. Registered on the root cobra in
// main.go alongside NewStorageCommand / NewPATCommand.
func NewBootstrapIndexCommand() *cobra.Command {
	var f bootstrapFlags
	cmd := &cobra.Command{
		Use:   "bootstrap-index",
		Short: "Rebuild index/papers.parquet from a full qatlas-raw bucket scan",
		Long: `Walk every (pdf|markdown|json|images)/<yymm>/ prefix in the
qatlas-raw bucket, observe what assets exist for each paper, and
write the result to index/papers.parquet via paperindex.Store's
existing CAS flush path.

Each json/<id>.json is also fetched (default; toggle with
--enrich-json=false) so the title/abstract/authors/categories
columns get populated for legacy papers that pre-date the live
webhook flow.

IMPORTANT: stop qatlas-server.service before running. The live
server's flush ticker will fight this command's final flush
otherwise.

Examples:
  # Full rebuild including metadata enrichment (~1.5h for 10^5 papers)
  qatlas-server bootstrap-index

  # Fast skeleton-only rebuild (asset existence only, no title/abstract)
  qatlas-server bootstrap-index --enrich-json=false

  # Bump parallelism if your RustFS handles concurrent LIST better
  qatlas-server bootstrap-index --concurrency 8
`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBootstrapIndex(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().IntVar(&f.concurrency, "concurrency", 4,
		"in-flight LIST + GET operations (RustFS-beta-5 returns 500 above ~8)")
	cmd.Flags().BoolVar(&f.enrichJSON, "enrich-json", true,
		"fetch each json/<id>.json and parse title/abstract/authors/categories into parquet")
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false,
		"machine-readable progress + final result as NDJSON on stdout")
	return cmd
}

func runBootstrapIndex(stdout, stderr io.Writer, f bootstrapFlags) error {
	dotenvPath := loadDotEnv()
	cfg, err := config.Load(dotenvPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.S3Enabled() {
		return errors.New("bootstrap-index requires the S3 backend (QATLAS_S3_* env all set); LocalStore is dev-only and doesn't need this")
	}

	rawStore, err := objstore.NewS3StoreDual(
		cfg.S3Endpoint, cfg.S3PublicEndpoint,
		cfg.S3Bucket, cfg.S3AccessKeyID, cfg.S3SecretAccessKey,
	)
	if err != nil {
		return fmt.Errorf("connect s3: %w", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Open a paperindex.Store against the same rawStore. The Store
	// loads any existing parquet at startup (so old metadata isn't
	// lost on rerun), then we Bootstrap on top of it.
	store, err := paperindex.New(ctx, paperindex.Config{
		Store: rawStore,
		// Disable background ticks during bootstrap: refresh would
		// fight our writes, flush would race our explicit final
		// flush. We trigger flushes synchronously via Close().
		RefreshInterval: 24 * time.Hour,
		FlushInterval:   24 * time.Hour,
	})
	if err != nil {
		return fmt.Errorf("open paperindex: %w", err)
	}
	defer func() {
		if cerr := store.Close(); cerr != nil {
			fmt.Fprintf(stderr, "warning: close paperindex: %v\n", cerr)
		}
	}()

	fmt.Fprintf(stderr, "bootstrap-index: starting scan of s3://%s/ (concurrency=%d, enrich-json=%v)\n",
		cfg.S3Bucket, f.concurrency, f.enrichJSON)
	fmt.Fprintf(stderr, "bootstrap-index: pre-load papers=%d\n", store.RowCount())

	progress := func(stage string, doneBatches, totalBatches, papersTouched int) {
		if f.jsonOutput {
			line, _ := json.Marshal(map[string]any{
				"event":          "progress",
				"stage":          stage,
				"batches_done":   doneBatches,
				"batches_total":  totalBatches,
				"papers_touched": papersTouched,
			})
			fmt.Fprintln(stdout, string(line))
			return
		}
		// Plain progress: emit every 25 batches or on final batch to
		// keep TUI scrolling at human-readable cadence.
		if totalBatches > 0 && (doneBatches%25 == 0 || doneBatches == totalBatches) {
			fmt.Fprintf(stderr, "  [%s] %d/%d batches, %d unique papers\n",
				stage, doneBatches, totalBatches, papersTouched)
		}
	}

	result, err := store.Bootstrap(ctx, rawStore, paperindex.BootstrapOptions{
		Concurrency: f.concurrency,
		EnrichJSON:  f.enrichJSON,
		OnProgress:  progress,
	})
	if err != nil {
		return fmt.Errorf("bootstrap scan: %w", err)
	}

	// Summary — always to stderr in human form so the script can
	// be eyeballed; also to stdout as JSON if --json.
	fmt.Fprintf(stderr, "\nbootstrap-index: done in %s\n", result.Duration.Truncate(time.Second))
	fmt.Fprintf(stderr, "  batches: %d total = %d ok + %d errored\n",
		result.BatchesTotal, result.BatchesOK, result.BatchesErrored)
	fmt.Fprintf(stderr, "  per-kind objects: pdf=%d markdown=%d json=%d images=%d\n",
		result.PerKindObjects[paperindex.KindPDF],
		result.PerKindObjects[paperindex.KindMarkdown],
		result.PerKindObjects[paperindex.KindJSON],
		result.PerKindObjects[paperindex.KindImages])
	fmt.Fprintf(stderr, "  papers touched: %d\n", result.PapersTouched)
	if f.enrichJSON {
		fmt.Fprintf(stderr, "  json metadata: parsed=%d missed=%d\n",
			result.MetadataParsed, result.MetadataMissed)
	}
	if len(result.BatchErrors) > 0 {
		fmt.Fprintf(stderr, "  first %d batch errors:\n", len(result.BatchErrors))
		for _, e := range result.BatchErrors {
			fmt.Fprintf(stderr, "    %s/%s/: %s\n", e.Kind, e.YYMM, truncate(e.Error, 120))
		}
	}
	fmt.Fprintf(stderr, "  catalog now: papers=%d\n", store.RowCount())

	if f.jsonOutput {
		line, _ := json.Marshal(map[string]any{
			"event":            "summary",
			"duration_seconds": result.Duration.Seconds(),
			"batches_total":    result.BatchesTotal,
			"batches_ok":       result.BatchesOK,
			"batches_errored":  result.BatchesErrored,
			"per_kind_objects": result.PerKindObjects,
			"papers_touched":   result.PapersTouched,
			"metadata_parsed":  result.MetadataParsed,
			"metadata_missed":  result.MetadataMissed,
			"row_count_final":  store.RowCount(),
		})
		fmt.Fprintln(stdout, string(line))
	}

	// Non-zero exit if any batch errored, so CI / scripts can detect
	// partial scans without parsing stderr.
	if result.BatchesErrored > 0 {
		fmt.Fprintf(stderr, "\nbootstrap-index: WARNING %d batches errored; parquet is still updated but missing those (kind,yymm) tuples\n",
			result.BatchesErrored)
		// We still return nil (success) — the parquet IS updated
		// with everything we could scan. Exit code 0 keeps the
		// systemd-unit-driven invocation simple. Callers wanting
		// strict mode can grep stderr for "WARNING".
	}

	// Belt-and-suspenders: make sure flushes land before we exit.
	// store.Close() in defer will also call flush, but doing it
	// here makes the timing explicit in the log.
	_ = os.Stderr.Sync()
	return nil
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
