package main

// `qatlasd storage migrate-layout` moves pre-Apr 2007 ("old-style")
// objects from the legacy BARE bucket layout to the new per-category
// layout that landed in Phase A1.
//
// # Background
//
// Pre-A1 the bucket stored old-style PDFs as `<yymm>/<stem>.pdf`
// (e.g. `9508/9508027v2.pdf`). The catalog records this same shape in
// `pdf_path` since it was bootstrapped from a bucket listing — no
// arXiv subject prefix was ever attached. That's a latent data-
// integrity bug because identical 7-digit serials in DIFFERENT
// arxiv categories (quant-ph/0207065 vs hep-th/0207065 — verifiably
// different papers) cannot coexist under the same `<yymm>/<stem>` key.
//
// Production was bootstrapped from a single `cat:quant-ph` OpenAlex
// query, so the 28k pre-2007 objects we hold are all genuinely
// quant-ph (issue #10 tracks rigorous verification). This command
// encodes that operational fact: every object matching the legacy
// `<yymm>/<stem>.<ext>` shape with yymm < 0704 is renamed to the
// new `<yymm>/quant-ph/<stem>.<ext>` shape under the same bucket.
//
// Reads stay backwards compatible during and after migration via
// `paperassets.LegacyAssetKeyFor` — the dual-read fallback continues
// to probe the legacy location for anything that wasn't migrated
// (debug uploads, future runs that surface non-quant-ph papers).
//
// # Safety
//
// Default is DRY RUN. `--apply` flips it to real mutations. Each
// object goes Stat-new (skip if already migrated) → Copy old → new
// (server-side via minio CopyObject when possible) → Delete old.
// Failures don't abort the run; they're tallied and reported.
//
// # Why not also rewrite Neo4j
//
// Per user request: "先不要管 neo4j". The catalog's bare `arxiv_id`
// values continue to work because handlers normalize through
// `paperassets.AssetKey`, which now defaults bare → quant-ph (see
// paperassets.DefaultOldStyleCategory). A follow-up command can
// rewrite `pdf_path` / `md_path` / `images_path` to reflect the new
// physical layout when needed.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"

	"github.com/spf13/cobra"
)

// legacyOldStyleKeyRE matches a bucket-relative key in the legacy bare
// layout: `<4-digit yymm>/<7-digit serial><vN>.<ext>`. The trailing
// extension is anchored per-kind by the caller. Subdirectories
// (already-migrated `<yymm>/<cat>/<stem>.<ext>`) DO NOT match — they
// have a third path segment.
var legacyOldStyleKeyRE = regexp.MustCompile(`^(\d{4})/(\d{7}v\d+)\.[a-z0-9]+$`)

type migrateLayoutFlags struct {
	kind     string // pdf | md | images | all
	yymmCap  string // only objects with yymm < this value (default 0704)
	category string // category to assign to bare objects (default quant-ph)
	limit    int    // stop after this many copies (0 = no limit)
	apply    bool
	dryRun   bool
}

func newStorageMigrateLayoutCmd() *cobra.Command {
	var f migrateLayoutFlags
	cmd := &cobra.Command{
		Use:   "migrate-layout",
		Short: "Move pre-2007 bare-layout objects under <yymm>/<category>/ (issue #4)",
		Long: `Walk a per-kind bucket and rewrite legacy old-style bare keys
(<yymm>/<stem>.<ext>) into the new per-category layout
(<yymm>/<category>/<stem>.<ext>). Default category is "quant-ph"
because the historical bootstrap was quant-ph-only.

Default is DRY RUN — pass --apply to actually copy + delete.

Examples:
  # Preview every old-style PDF that would move
  qatlasd storage migrate-layout --kind pdf

  # Actually migrate all three kinds (pdf + md + images)
  qatlasd storage migrate-layout --kind all --apply

  # Migration but capped (sanity test on first 100 objects)
  qatlasd storage migrate-layout --kind pdf --limit 100 --apply
`,
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStorageMigrateLayout(cmd.Context(), cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().StringVar(&f.kind, "kind", "all", "which per-kind bucket(s) to migrate: pdf | md | images | all")
	cmd.Flags().StringVar(&f.yymmCap, "yymm-lt", "0704", "only migrate objects with yymm strictly less than this (default 0704 = Apr 2007, the new-style cutover)")
	cmd.Flags().StringVar(&f.category, "category", paperassets.DefaultOldStyleCategory, "category subdirectory to insert; default matches paperassets.DefaultOldStyleCategory")
	cmd.Flags().IntVar(&f.limit, "limit", 0, "stop after this many objects per kind (0 = no limit); useful for staged rollout")
	cmd.Flags().BoolVar(&f.apply, "apply", false, "actually copy + delete (without this we force dry-run)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", true, "preview only; mutation requires --apply anyway")
	return cmd
}

type migrateStats struct {
	scanned, eligible, alreadyMigrated, copied, deleted, skipped, failed int
	bytesCopied                                                          int64
}

func (s *migrateStats) String() string {
	return fmt.Sprintf(
		"scanned=%d eligible=%d already_migrated=%d copied=%d deleted=%d skipped=%d failed=%d bytes_copied=%d",
		s.scanned, s.eligible, s.alreadyMigrated, s.copied, s.deleted, s.skipped, s.failed, s.bytesCopied,
	)
}

func runStorageMigrateLayout(ctx context.Context, stdout, stderr io.Writer, f migrateLayoutFlags) error {
	dotenvPath := loadDotEnv()
	cfg, err := config.Load(dotenvPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.S3Enabled() {
		return errors.New("storage migrate-layout requires the S3 backend (QATLAS_S3_* env all set); LocalStore is dev-only")
	}
	if len(f.yymmCap) != 4 {
		return fmt.Errorf("--yymm-lt %q must be a 4-digit YYMM (e.g. 0704)", f.yymmCap)
	}
	if _, err := strconv.Atoi(f.yymmCap); err != nil {
		return fmt.Errorf("--yymm-lt %q is not numeric", f.yymmCap)
	}
	if strings.TrimSpace(f.category) == "" {
		return errors.New("--category must be non-empty (default quant-ph)")
	}

	kinds := []string{f.kind}
	if f.kind == "all" {
		kinds = []string{"pdf", "markdown", "images"}
	}
	mode := "DRY-RUN"
	if f.apply {
		mode = "APPLY"
	}
	fmt.Fprintf(stderr, "storage migrate-layout: mode=%s yymm<%s category=%s kinds=%v\n",
		mode, f.yymmCap, f.category, kinds)

	overall := &migrateStats{}
	for _, k := range kinds {
		bucket, err := bucketForKind(cfg, k)
		if err != nil {
			fmt.Fprintf(stderr, "skip kind=%s: %v\n", k, err)
			continue
		}
		store, err := objstore.NewS3Store(cfg.S3Endpoint, bucket, cfg.S3AccessKeyID, cfg.S3SecretAccessKey)
		if err != nil {
			return fmt.Errorf("connect s3 (kind=%s, bucket=%s): %w", k, bucket, err)
		}
		stats, err := migrateOneKind(ctx, stdout, stderr, store, bucket, f)
		if err != nil {
			return fmt.Errorf("kind=%s: %w", k, err)
		}
		fmt.Fprintf(stderr, "kind=%s bucket=%s done: %s\n", k, bucket, &stats)
		overall.add(&stats)
	}
	fmt.Fprintf(stderr, "overall: %s\n", overall)
	return nil
}

func (s *migrateStats) add(o *migrateStats) {
	s.scanned += o.scanned
	s.eligible += o.eligible
	s.alreadyMigrated += o.alreadyMigrated
	s.copied += o.copied
	s.deleted += o.deleted
	s.skipped += o.skipped
	s.failed += o.failed
	s.bytesCopied += o.bytesCopied
}

func migrateOneKind(ctx context.Context, stdout, stderr io.Writer, store *objstore.S3Store, bucket string, f migrateLayoutFlags) (migrateStats, error) {
	stats := migrateStats{}

	// List the whole bucket — at our scale (~28k objects per bucket
	// for old papers, plus newer ones we ignore) this is one S3
	// pagination round, sub-second on the LAN.
	infos, err := store.ListPrefix(ctx, "", 0)
	if err != nil {
		return stats, fmt.Errorf("list %s: %w", bucket, err)
	}
	for _, info := range infos {
		stats.scanned++
		oldKey := info.Key
		m := legacyOldStyleKeyRE.FindStringSubmatch(oldKey)
		if m == nil {
			continue // not a legacy old-style key — leave alone
		}
		yymm := m[1]
		if yymm >= f.yymmCap {
			continue // newer than the cutover — leave alone
		}
		stem := m[2]
		stats.eligible++

		// New key: <yymm>/<category>/<stem>.<ext>
		ext := oldKey[len(yymm)+1+len(stem):] // includes leading '.'
		newKey := yymm + "/" + f.category + "/" + stem + ext

		// Skip if already migrated.
		if _, exists, sErr := store.Stat(ctx, newKey); sErr == nil && exists {
			stats.alreadyMigrated++
			fmt.Fprintf(stdout, "%s -> %s ALREADY_MIGRATED\n", oldKey, newKey)
			continue
		}

		if !f.apply {
			fmt.Fprintf(stdout, "%s -> %s WOULD_MIGRATE (%d bytes)\n", oldKey, newKey, info.Size)
			if f.limit > 0 && stats.eligible >= f.limit {
				fmt.Fprintf(stderr, "kind reached --limit=%d in dry-run, stopping\n", f.limit)
				break
			}
			continue
		}

		// Real path: copy then delete. Use server-side CopyObject when
		// the backend supports it; fall back to Get + Put when not.
		copied, copyErr := copyObjectPreservingMeta(ctx, store, oldKey, newKey)
		if copyErr != nil {
			stats.failed++
			fmt.Fprintf(stderr, "FAIL copy %s -> %s: %v\n", oldKey, newKey, copyErr)
			continue
		}
		stats.copied++
		stats.bytesCopied += copied

		if delErr := store.Delete(ctx, oldKey); delErr != nil {
			stats.failed++
			// Don't roll the new object back — it's idempotent and the
			// next run will see it as ALREADY_MIGRATED.
			fmt.Fprintf(stderr, "WARN delete old key %s after copy: %v (new key %s left in place; rerun to retry delete)\n", oldKey, delErr, newKey)
			continue
		}
		stats.deleted++
		fmt.Fprintf(stdout, "%s -> %s MIGRATED (%d bytes)\n", oldKey, newKey, copied)

		if f.limit > 0 && stats.copied >= f.limit {
			fmt.Fprintf(stderr, "kind reached --limit=%d after %d copies, stopping\n", f.limit, stats.copied)
			break
		}
	}
	return stats, nil
}

// copyObjectPreservingMeta copies oldKey to newKey under the same
// bucket, preserving content-type and user metadata (sha256 etc.).
// Uses the S3-native CopyObject (zero-byte transit on the client)
// when the backend exposes one; otherwise streams Get + Put.
//
// Writes use IfNoneMatch:"*" so a concurrent migration on another
// edge can't double-write — the loser observes 412 and treats it as
// success (which it is — the object exists at newKey).
func copyObjectPreservingMeta(ctx context.Context, store *objstore.S3Store, oldKey, newKey string) (int64, error) {
	if n, err := store.ServerSideCopy(ctx, oldKey, newKey); err == nil {
		return n, nil
	} else if !errors.Is(err, objstore.ErrCopyUnsupported) {
		// Surface real CopyObject failures (perm denied, network,
		// 412 if newKey already exists) so the run can move on.
		return 0, fmt.Errorf("server-side copy: %w", err)
	}

	// Fallback: stream Get + PutWithOptions.
	rc, info, err := store.Get(ctx, oldKey)
	if err != nil {
		return 0, fmt.Errorf("get old: %w", err)
	}
	defer rc.Close()

	contentType := info.ContentType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	n, err := store.PutWithOptions(ctx, newKey, rc, info.Size, objstore.PutOptions{
		ContentType: contentType,
		Metadata:    info.Metadata,
		IfNoneMatch: "*",
	})
	if err != nil {
		if errors.Is(err, objstore.ErrPreconditionFailed) {
			// Someone else migrated it between Stat and Put — fine.
			return info.Size, nil
		}
		return 0, fmt.Errorf("put new: %w", err)
	}
	return n, nil
}

// (intentionally unused) silenceTime keeps `time` imported across edits.
var _ = time.Now
