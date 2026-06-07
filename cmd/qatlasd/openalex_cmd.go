// Command-line surface for OpenAlex snapshot ingest.
//
// `openalex bootstrap` streams the filtered OpenAlex works snapshot
// (byte-faithful jsonl.gz parts in the qatlas-openalex bucket) into the
// :PaperWork layer of Neo4j: one MERGE per work keyed by arxiv_id, then
// a second pass for PAPER_CITES citation edges. This is the metadata
// half of the catalog (titles / authors / citations); the asset half
// (has_pdf / has_md / image_count) is owned by `papers sync`.
//
// EXECUTION IS OPERATOR-DRIVEN AND DECOUPLED FROM THIS SESSION. The
// 10M-node bootstrap is a long, resource-heavy run against the catalog
// Neo4j; it must be scheduled deliberately, not as a side effect of a
// deploy. See handoff.md for the full runbook (snapshot sync into
// qatlas-openalex, then this command). The code is wired + compiles so
// the runbook is a one-liner when the operator is ready.

package main

import (
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/neo4j"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/papers"

	"github.com/spf13/cobra"
)

type openalexBootstrapFlags struct {
	prefix    string
	citations bool
	limit     int // cap number of part files (0 = all); smoke-test knob
}

// NewOpenAlexCommand mounts the `openalex` subcommand group on the
// PocketBase root cobra command.
func NewOpenAlexCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "openalex",
		Short: "Ingest the OpenAlex works snapshot into the Neo4j :PaperWork layer",
		Long: `OpenAlex snapshot ingest.

Requires NEO4J_URI (+ creds) and QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT
(the reserved bucket holding the byte-faithful filtered jsonl.gz parts).

NOTE: the full bootstrap is a long, resource-heavy run — schedule it
deliberately. See handoff.md.`,
	}
	root.AddCommand(newOpenAlexBootstrapCmd())
	return root
}

func newOpenAlexBootstrapCmd() *cobra.Command {
	var f openalexBootstrapFlags
	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Stream the OpenAlex snapshot parts and MERGE :PaperWork nodes + PAPER_CITES edges",
		Long: `Walk every works part under the snapshot prefix, MERGE one
:PaperWork per arxiv-linked work, then (optionally) a second pass for
PAPER_CITES citation edges.

Examples:
  # Smoke test: ingest just the first part file
  qatlasd openalex bootstrap --limit 1

  # Full ingest including citation edges
  qatlasd openalex bootstrap --citations
`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOpenAlexBootstrap(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().StringVar(&f.prefix, "prefix", "works/", "object-key prefix of the works parts within the snapshot bucket")
	cmd.Flags().BoolVar(&f.citations, "citations", false, "second pass: MERGE PAPER_CITES edges (requires both endpoints already ingested)")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "cap the number of part files processed (0 = all; smoke-test knob)")
	return cmd
}

func runOpenAlexBootstrap(stdout, stderr io.Writer, f openalexBootstrapFlags) error {
	dotenvPath := loadDotEnv()
	cfg, err := config.Load(dotenvPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.S3BucketOpenAlex == "" {
		return errors.New("openalex bootstrap requires QATLAS_S3_BUCKET_OPENALEX_SNAPSHOT")
	}
	if cfg.S3Endpoint == "" || cfg.S3AccessKeyID == "" {
		return errors.New("openalex bootstrap requires the S3 backend (QATLAS_S3_* env)")
	}

	// The OpenAlex snapshot lives in its own bucket, not part of the
	// 3-kind upload Router, so we build a dedicated S3Store for it.
	snap, err := objstore.NewS3Store(cfg.S3Endpoint, cfg.S3BucketOpenAlex, cfg.S3AccessKeyID, cfg.S3SecretAccessKey)
	if err != nil {
		return fmt.Errorf("connect openalex bucket: %w", err)
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

	keys, err := openalex.ListPartKeys(ctx, snap, f.prefix)
	if err != nil {
		return fmt.Errorf("list parts: %w", err)
	}
	if f.limit > 0 && len(keys) > f.limit {
		keys = keys[:f.limit]
	}
	fmt.Fprintf(stderr, "Neo4j   : %s\n", cfg.Neo4jURI)
	fmt.Fprintf(stderr, "Snapshot: %s/%s (%d parts under %q)\n", cfg.S3Endpoint, cfg.S3BucketOpenAlex, len(keys), f.prefix)
	fmt.Fprintln(stderr, "---")

	var works []openalex.Work
	totalWorks, totalCites := 0, 0
	for i, key := range keys {
		works = works[:0]
		if err := openalex.StreamWorks(ctx, snap, key, func(w openalex.Work) error {
			works = append(works, w)
			return nil
		}); err != nil {
			return fmt.Errorf("stream %s: %w", key, err)
		}
		n, err := openalex.IngestWorks(ctx, nc, works)
		if err != nil {
			return fmt.Errorf("ingest works %s: %w", key, err)
		}
		totalWorks += n
		if f.citations {
			c, err := openalex.IngestCitations(ctx, nc, works)
			if err != nil {
				return fmt.Errorf("ingest citations %s: %w", key, err)
			}
			totalCites += c
		}
		fmt.Fprintf(stderr, "[%d/%d] %s → %d works, %d cites\n", i+1, len(keys), key, n, totalCites)
	}

	fmt.Fprintf(stdout, "works ingested    : %d\n", totalWorks)
	fmt.Fprintf(stdout, "citations ingested: %d\n", totalCites)
	return nil
}
