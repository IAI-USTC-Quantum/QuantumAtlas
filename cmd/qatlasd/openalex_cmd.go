// Command-line surface for OpenAlex snapshot ingest.
//
// `openalex bootstrap-pg` streams the filtered OpenAlex works snapshot
// (byte-faithful jsonl.gz parts in the qatlas-openalex bucket) into the
// PostgreSQL openalex_works corpus (ADR 0006). This is the metadata half
// of the catalog (titles / authors / citations); the asset half is owned
// by `papers sync`.
//
// EXECUTION IS OPERATOR-DRIVEN AND DECOUPLED FROM THIS SESSION. The
// full-corpus bootstrap is a long, resource-heavy run against the corpus
// database; it must be scheduled deliberately, not as a side effect of a
// deploy.

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalexcorpus"

	"github.com/spf13/cobra"
)

// NewOpenAlexCommand mounts the `openalex` subcommand group on the
// PocketBase root cobra command.
func NewOpenAlexCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "openalex",
		Short: "Ingest the OpenAlex works snapshot into the PostgreSQL corpus",
		Long: `OpenAlex snapshot ingest.

Reads the byte-faithful jsonl.gz parts in the
s3.bucket_openalex bucket:

  bootstrap-pg  → PostgreSQL openalex_works corpus (ADR 0006: full works as
                  jsonb, only filtered never modified; citation edges + the
                  arxiv join key derived without rewriting the record)

Requires s3.bucket_openalex + S3 creds and
postgres.dsn in config.yaml.

NOTE: the full bootstrap is a long, resource-heavy run — schedule it
deliberately.`,
	}
	root.AddCommand(newOpenAlexBootstrapPGCmd())
	root.AddCommand(newOpenAlexQueryPGCmd())
	return root
}

type openalexBootstrapPGFlags struct {
	prefix string
	limit  int    // cap number of part files (0 = all); smoke-test knob
	batch  int    // unnest batch size (0 = package default)
	since  string // only ingest parts with updated_date >= YYYY-MM-DD (incremental)
}

type openalexQueryPGFlags struct {
	id      string
	doi     string
	search  string
	filter  string
	sort    string
	page    int
	perPage int
	count   bool
	pretty  bool
}

// newOpenAlexBootstrapPGCmd streams the OpenAlex snapshot parts into the
// PostgreSQL openalex_works corpus (ADR 0006), the relational sink that
// makes citation context + vector joins plain SQL. Decoupled from boot and
// operator-driven.
func newOpenAlexBootstrapPGCmd() *cobra.Command {
	var f openalexBootstrapPGFlags
	cmd := &cobra.Command{
		Use:   "bootstrap-pg",
		Short: "Stream the OpenAlex snapshot parts into the PostgreSQL openalex_works corpus",
		Long: `Walk every works part under the snapshot prefix and upsert each
record verbatim as jsonb into openalex_works (ADR 0006), deriving the
indexed hot columns, the arxiv join key, and the citation out-edge array
(openalex_referenced_work_ids, ADR 0010) without rewriting the record.

Idempotent + resumable: re-running re-upserts the same rows, so an
interrupted run is safe to resume (or restrict with --since for an
incremental refresh of only new updated_date partitions).

Examples:
  # Smoke test: ingest just the first part file
  qatlasd openalex bootstrap-pg --limit 1

  # Full ingest
  qatlasd openalex bootstrap-pg

  # Incremental: only partitions updated on/after a date
  qatlasd openalex bootstrap-pg --since 2026-01-01
`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOpenAlexBootstrapPG(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().StringVar(&f.prefix, "prefix", "works/", "object-key prefix of the works parts within the snapshot bucket")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "cap the number of part files processed (0 = all; smoke-test knob)")
	cmd.Flags().IntVar(&f.batch, "batch", 0, "unnest batch size for the upsert statements (0 = default)")
	cmd.Flags().StringVar(&f.since, "since", "", "only ingest parts whose updated_date partition is >= this YYYY-MM-DD (incremental refresh)")
	return cmd
}

func runOpenAlexBootstrapPG(stdout, stderr io.Writer, f openalexBootstrapPGFlags) error {
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.S3BucketOpenAlex == "" {
		return errors.New("openalex bootstrap-pg requires s3.bucket_openalex in config.yaml")
	}
	if cfg.S3Endpoint == "" || cfg.S3AccessKeyID == "" {
		return errors.New("openalex bootstrap-pg requires the S3 backend (s3.* in config.yaml)")
	}
	if cfg.PostgresDSN == "" {
		return errors.New("openalex bootstrap-pg requires postgres.dsn (the central corpus database)")
	}

	// The OpenAlex snapshot lives in its own bucket, not part of the
	// 3-kind upload Router, so we build a dedicated S3Store for it.
	snap, err := objstore.NewS3Store(cfg.S3Endpoint, cfg.S3BucketOpenAlex, cfg.S3AccessKeyID, cfg.S3SecretAccessKey)
	if err != nil {
		return fmt.Errorf("connect openalex bucket: %w", err)
	}

	pool, err := initPostgresPool(cfg)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	if pool == nil {
		return errors.New("postgres pool not configured")
	}
	defer pool.Close()
	corpus := openalexcorpus.NewStore(pool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := corpus.EnsureSchema(ctx); err != nil {
		return fmt.Errorf("ensure corpus schema: %w", err)
	}

	keys, err := openalexcorpus.ListPartKeys(ctx, snap, f.prefix)
	if err != nil {
		return fmt.Errorf("list parts: %w", err)
	}
	keys = filterPartsSince(keys, f.since)
	if f.limit > 0 && len(keys) > f.limit {
		keys = keys[:f.limit]
	}

	fmt.Fprintf(stderr, "Postgres: %s\n", redactDSN(cfg.PostgresDSN))
	fmt.Fprintf(stderr, "Snapshot: %s/%s (%d parts under %q)\n", cfg.S3Endpoint, cfg.S3BucketOpenAlex, len(keys), f.prefix)
	if f.since != "" {
		fmt.Fprintf(stderr, "Since   : %s (incremental)\n", f.since)
	}
	fmt.Fprintln(stderr, "---")

	opts := openalexcorpus.IngestOptions{BatchSize: f.batch}
	totalWorks, totalCites := 0, 0
	for i, key := range keys {
		rep, err := corpus.IngestPart(ctx, snap, key, opts)
		if err != nil {
			return fmt.Errorf("ingest %s: %w", key, err)
		}
		totalWorks += rep.Works
		totalCites += rep.Citations
		fmt.Fprintf(stderr, "[%d/%d] %s → %d works, %d cites\n", i+1, len(keys), key, rep.Works, rep.Citations)
	}

	fmt.Fprintln(stderr, "--- building corpus indexes (CONCURRENTLY; can take a long time + heavy I/O on a large corpus) ---")
	if err := corpus.EnsureIndexes(ctx); err != nil {
		return fmt.Errorf("ensure corpus indexes: %w", err)
	}

	fmt.Fprintf(stdout, "works ingested    : %d\n", totalWorks)
	fmt.Fprintf(stdout, "citations ingested: %d\n", totalCites)
	if st, err := corpus.QueryStats(ctx); err == nil {
		fmt.Fprintf(stdout, "corpus total      : %d works (%d with arxiv), %d citation edges\n",
			st.Works, st.WithArxiv, st.Citations)
	}
	return nil
}

func newOpenAlexQueryPGCmd() *cobra.Command {
	var f openalexQueryPGFlags
	cmd := &cobra.Command{
		Use:   "query-pg",
		Short: "Query the local PostgreSQL OpenAlex corpus (OpenAlex-like, internal CLI)",
		Long: `Query the local PostgreSQL OpenAlex corpus created by bootstrap-pg.

This is an internal/operator CLI, not an outbound OpenAlex-compatible HTTP API.
It returns the raw jsonb records stored in openalex_works, wrapped in an
OpenAlex-like {meta, results} envelope for list/search queries.

Examples:
  # Get one work by OpenAlex id
  qatlasd openalex query-pg --id W2741809807 --pretty

  # Get one work by DOI
  qatlasd openalex query-pg --doi 10.7717/peerj.4375 --pretty

  # OpenAlex-like filters (comma-separated key:value)
  qatlasd openalex query-pg --filter 'type:article,from_publication_year:2020,has_arxiv:true' --sort cited_by_count:desc --per-page 10

  # Local full-text-ish title/display_name search
  qatlasd openalex query-pg --search 'quantum error correction' --per-page 10
`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runOpenAlexQueryPG(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().StringVar(&f.id, "id", "", "bare or URL-form OpenAlex work id (W...) to fetch")
	cmd.Flags().StringVar(&f.doi, "doi", "", "DOI to fetch (bare DOI or doi.org URL)")
	cmd.Flags().StringVar(&f.search, "search", "", "title/display_name search text (local PostgreSQL tsvector)")
	cmd.Flags().StringVar(&f.filter, "filter", "", "comma-separated OpenAlex-like filters, e.g. type:article,has_arxiv:true")
	cmd.Flags().StringVar(&f.sort, "sort", "", "sort key: cited_by_count[:desc], publication_year[:desc], updated_date[:desc], openalex_id[:asc]")
	cmd.Flags().IntVar(&f.page, "page", 1, "1-based page number")
	cmd.Flags().IntVar(&f.perPage, "per-page", 25, "results per page (max 200)")
	cmd.Flags().BoolVar(&f.count, "count", false, "include exact count(*) in meta (expensive on the full corpus)")
	cmd.Flags().BoolVar(&f.pretty, "pretty", false, "pretty-print JSON output")
	return cmd
}

func runOpenAlexQueryPG(stdout, stderr io.Writer, f openalexQueryPGFlags) error {
	_ = stderr
	cfg, err := loadConfig()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if cfg.PostgresDSN == "" {
		return errors.New("openalex query-pg requires postgres.dsn (the central corpus database)")
	}
	pool, err := initPostgresPool(cfg)
	if err != nil {
		return fmt.Errorf("postgres: %w", err)
	}
	if pool == nil {
		return errors.New("postgres pool not configured")
	}
	defer pool.Close()
	corpus := openalexcorpus.NewStore(pool)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var payload any
	switch {
	case f.id != "":
		raw, ok, err := corpus.GetWork(ctx, f.id)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("openalex query-pg: no work found for id %q", f.id)
		}
		payload = raw
	case f.doi != "":
		raw, ok, err := corpus.GetWorkByDOI(ctx, f.doi)
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("openalex query-pg: no work found for doi %q", f.doi)
		}
		payload = raw
	default:
		filters, err := openalexcorpus.ParseFilters(f.filter)
		if err != nil {
			return err
		}
		payload, err = corpus.QueryWorks(ctx, openalexcorpus.QueryOptions{
			Search:  f.search,
			Filters: filters,
			Sort:    f.sort,
			Page:    f.page,
			PerPage: f.perPage,
			Count:   f.count,
		})
		if err != nil {
			return err
		}
	}
	if f.pretty {
		b, err := json.MarshalIndent(payload, "", "  ")
		if err != nil {
			return fmt.Errorf("marshal result: %w", err)
		}
		_, err = fmt.Fprintln(stdout, string(b))
		return err
	}
	enc := json.NewEncoder(stdout)
	return enc.Encode(payload)
}

// filterPartsSince drops parts whose updated_date partition is strictly
// before since (YYYY-MM-DD). An empty since keeps everything. Parts whose
// key carries no partition date are always kept (can't prove they're old).
func filterPartsSince(keys []string, since string) []string {
	if since == "" {
		return keys
	}
	out := keys[:0:0]
	for _, k := range keys {
		d := openalexcorpus.PartitionDate(k)
		if d == "" || d >= since {
			out = append(out, k)
		}
	}
	return out
}

// redactDSN masks the password in a postgres DSN for logging.
func redactDSN(dsn string) string {
	at := strings.LastIndexByte(dsn, '@')
	if at < 0 {
		return dsn
	}
	scheme := strings.Index(dsn, "://")
	if scheme < 0 {
		return "***"
	}
	creds := dsn[scheme+3 : at]
	if colon := strings.IndexByte(creds, ':'); colon >= 0 {
		return dsn[:scheme+3] + creds[:colon] + ":***" + dsn[at:]
	}
	return dsn
}
