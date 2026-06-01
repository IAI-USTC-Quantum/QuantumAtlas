// Command-line surface for the Neo4j paper catalog.
//
// `papers sync` reconciles the catalog's per-paper asset flags
// (has_pdf / has_md / image_count) against the actual objects in the
// per-kind object-store buckets (qatlas-pdf / qatlas-md /
// qatlas-images). It is the v0.7.0 §4.2 safety net: if a write-through
// Cypher failed because Neo4j was momentarily down during an upload
// (the handler still 201s the S3 write and sets X-Catalog-Sync:
// deferred), a later `papers sync` re-MERGEs the node from the bucket
// listing so the catalog converges.
//
// It is also the disaster-recovery path: after a Neo4j wipe, the asset
// layer (has_pdf/has_md/image_count) can be rebuilt one-to-one from the
// buckets via `papers sync --full --from-rustfs`. (OpenAlex metadata —
// titles, authors, citations — is rebuilt separately via the `openalex`
// subcommand; sync only attaches asset state.)
//
// Unlike the old bootstrap-index, sync is safe to run while the server
// is live: every write is an idempotent MERGE, so a concurrent
// write-through and a sync converge to the same node.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/neo4j"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"

	"github.com/spf13/cobra"
)

type papersSyncFlags struct {
	full       bool
	fromRustFS bool
	dryRun     bool
	batchSize  int
}

// NewPapersCommand mounts the `papers` subcommand group on the
// PocketBase root cobra command. Mirrors NewStorageCommand structure.
func NewPapersCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "papers",
		Short: "Maintenance operations on the Neo4j paper catalog",
		Long: `Catalog-side maintenance commands.

These commands require NEO4J_URI (+ creds) and the QATLAS_S3_* env vars
(the per-kind buckets the catalog reconciles against).`,
	}
	root.AddCommand(newPapersSyncCmd())
	return root
}

func newPapersSyncCmd() *cobra.Command {
	var f papersSyncFlags
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile catalog asset flags (has_pdf/has_md/image_count) from the buckets",
		Long: `Walk the per-kind object-store buckets and MERGE each paper's
asset state (has_pdf / has_md / image_count) into Neo4j.

This repairs drift from deferred write-throughs (Neo4j was down during
an upload) and rebuilds the asset layer after a catalog wipe.

Examples:
  # Reconcile everything from the buckets (disaster rebuild)
  qatlas-server papers sync --full --from-rustfs

  # Preview the diff without writing to Neo4j
  qatlas-server papers sync --full --from-rustfs --dry-run
`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runPapersSync(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().BoolVar(&f.full, "full", false, "scan the whole bucket (currently the only supported mode)")
	cmd.Flags().BoolVar(&f.fromRustFS, "from-rustfs", false, "reconcile against the object-store buckets (required)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", false, "report the diff without writing to Neo4j")
	cmd.Flags().IntVar(&f.batchSize, "batch-size", 0, "UNWIND batch size for MERGE statements (0 = default)")
	return cmd
}

func runPapersSync(stdout, stderr io.Writer, f papersSyncFlags) error {
	if !f.fromRustFS {
		return errors.New("papers sync currently only supports --from-rustfs (bucket reconcile)")
	}
	dotenvPath := loadDotEnv()
	cfg, err := config.Load(dotenvPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.S3Enabled() {
		return errors.New("papers sync requires the S3 backend (QATLAS_S3_* env all set)")
	}

	nc, err := neo4j.NewClient(cfg.Neo4jURI, cfg.Neo4jUser, cfg.Neo4jPassword, cfg.Neo4jDatabase)
	if err != nil {
		return fmt.Errorf("neo4j: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := nc.Connect(ctx); err != nil {
		return fmt.Errorf("connect neo4j: %w", err)
	}
	defer nc.Close(ctx)

	catalog := papers.NewStore(nc)
	if err := catalog.EnsureSchema(ctx); err != nil {
		fmt.Fprintf(stderr, "warning: EnsureSchema: %v\n", err)
	}

	rawStore, err := initRawStore(cfg)
	if err != nil {
		return fmt.Errorf("init raw store: %w", err)
	}

	fmt.Fprintf(stderr, "Neo4j   : %s\n", cfg.Neo4jURI)
	fmt.Fprintf(stderr, "Buckets : %s (pdf=%s md=%s images=%s)\n",
		cfg.S3Endpoint, cfg.S3BucketPDF, cfg.S3BucketMD, cfg.S3BucketImages)
	fmt.Fprintf(stderr, "Mode    : %s\n", map[bool]string{true: "DRY-RUN", false: "APPLY"}[f.dryRun])
	fmt.Fprintln(stderr, "---")

	rep, err := catalog.SyncFromStore(ctx, rawStore, papers.SyncOptions{
		DryRun:    f.dryRun,
		BatchSize: f.batchSize,
	})
	if err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	fmt.Fprintf(stdout, "pdf objects   : %d\n", rep.PDFObjects)
	fmt.Fprintf(stdout, "md objects    : %d\n", rep.MDObjects)
	fmt.Fprintf(stdout, "image objects : %d\n", rep.ImageObjects)
	fmt.Fprintf(stdout, "papers touched: %d\n", rep.PapersTouched)
	fmt.Fprintf(stdout, "duration      : %s\n", rep.Duration.Round(time.Millisecond))
	return nil
}

var _ = os.Stdout
