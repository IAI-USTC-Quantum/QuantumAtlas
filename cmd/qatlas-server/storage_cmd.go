// Command-line surface for QuantumAtlas object-store maintenance.
//
// We expose a `storage` subcommand group on the server binary because
// the ops that live here (enumerating bucket versions, deleting
// noncurrent versions) need the S3 credentials qatlas already has but
// are too destructive to wire as HTTP endpoints — accidentally
// triggering "prune noncurrent" via a web call would be a footgun.
// Shell access on the server box is the right trust boundary.
//
// # Why noncurrent versions accumulate
//
// `storage prune` exists because of a deliberate design choice in the
// upload pipeline (see internal/routes/papers.go + .github/copilot-
// instructions.md "写口语义"):
//
//   - Every `?overwrite=true` upload writes a new version; the prior
//     becomes "noncurrent" (preserved, not deleted).
//   - We do NOT install an S3 lifecycle rule to auto-expire them. Same
//     mental model as Synology Snapshot / Time Machine: keep everything
//     by default, prune on demand.
//   - sha256 dedup already short-circuits "same bytes uploaded twice"
//     so the noncurrent versions we accumulate are real content
//     changes, worth a manual review before destroying.
//
// `storage prune` is that manual review tool. It defaults to --dry-run
// because the bucket is content-addressed (sort of) and there is no
// undo: once a version is removed from RustFS, the bytes are gone.
//
// # Filters
//
//   --prefix     scope to a key prefix (e.g. "pdf/2511/" for one cohort).
//                Default: all keys in the bucket.
//   --older-than DURATION
//                only consider versions LastModified older than D ago.
//                Accepts Go duration syntax (1h, 24h, 720h) plus
//                d/w/y units we parse below (30d, 4w, 1y).
//                Default: 0 (no age cap).
//   --keep-last N
//                per object key, keep the N most-recent noncurrent
//                versions; only delete those beyond that count. The
//                current version is ALWAYS kept regardless of N.
//                Default: 0 (no per-key cap).
//
// Filters compose: a version is deleted only if BOTH --older-than and
// --keep-last say it can go. So `--older-than 90d --keep-last 5`
// means "keep at least 5 noncurrent per key, but among those the
// oldest are dropped first if they're also older than 90 days".
//
// # Output contract
//
//   stdout:
//     - dry-run / preview: one line per candidate "<key> @<version-id>
//       (<size>, <age>) DELETE_PLANNED"
//     - apply: one line per deletion "<key> @<version-id> DELETED"
//   stderr:
//     - summary header + final tallies (candidates, deleted, freed bytes)
//
// Operator sequence we recommend:
//   1. `qatlas-server storage prune --older-than 90d` (dry-run preview)
//   2. eyeball the list
//   3. add `--yes` to execute

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"

	"github.com/spf13/cobra"
)

// NewStorageCommand mounts the `storage` subcommand group on the
// PocketBase root cobra command. Mirrors NewPATCommand structure.
func NewStorageCommand() *cobra.Command {
	root := &cobra.Command{
		Use:   "storage",
		Short: "Maintenance operations on the QuantumAtlas object store (S3/RustFS backend)",
		Long: `Storage-side maintenance commands.

These commands require the QATLAS_S3_* env vars (or .env entries) and
are no-ops on the LocalStore dev backend — there is no version concept
to prune when assets live as plain files.`,
	}
	root.AddCommand(newStoragePruneCmd())
	return root
}

// pruneFlags carries the parsed CLI flags for `storage prune`.
type pruneFlags struct {
	kind       string // which per-kind bucket (pdf/md/images); v0.7.0 split
	prefix     string
	olderThan  string // raw flag value; parsed into duration
	keepLast   int
	apply      bool // --yes; without it we force dry-run regardless of --dry-run
	dryRun     bool
	jsonOutput bool
}

func newStoragePruneCmd() *cobra.Command {
	var f pruneFlags
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Delete noncurrent object versions per filter rules",
		Long: `Walk the bucket's version list and delete noncurrent versions
matching the filter flags. Current (latest) versions are NEVER deleted.

By default this is a DRY RUN — pass --yes to actually delete.

Examples:
  # Preview every noncurrent version in the bucket
  qatlas-server storage prune

  # Preview noncurrent versions under one paper's key, older than 90 days
  qatlas-server storage prune --prefix pdf/2511/2511.00010v1.pdf --older-than 90d

  # Keep at most 5 noncurrent versions per object key, delete the rest
  qatlas-server storage prune --keep-last 5 --yes

  # Aggressive: drop EVERY noncurrent version across the bucket
  qatlas-server storage prune --yes
`,
		// Don't dump usage on RunE error — operator-visible failure
		// (S3 perm denied, network timeout, etc.) deserves to surface
		// the actual error, not 30 lines of flag help.
		SilenceUsage:  true,
		SilenceErrors: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runStoragePrune(cmd.OutOrStdout(), cmd.ErrOrStderr(), f)
		},
	}
	cmd.Flags().StringVar(&f.kind, "kind", "pdf", "which per-kind bucket to prune: pdf | md | images (v0.7.0 split qatlas-raw into three)")
	cmd.Flags().StringVar(&f.prefix, "prefix", "", "limit to keys under this prefix (default: whole bucket)")
	cmd.Flags().StringVar(&f.olderThan, "older-than", "", "only delete noncurrent versions older than this duration (e.g. 24h, 30d, 1y); empty = no age cap")
	cmd.Flags().IntVar(&f.keepLast, "keep-last", 0, "per object key, keep the N most-recent noncurrent versions; 0 = no per-key cap")
	cmd.Flags().BoolVar(&f.apply, "yes", false, "actually delete (without this we force dry-run regardless of --dry-run)")
	cmd.Flags().BoolVar(&f.dryRun, "dry-run", true, "preview only; deletion requires --yes anyway")
	cmd.Flags().BoolVar(&f.jsonOutput, "json", false, "machine-readable output (one JSON object per line on stdout)")
	return cmd
}

func runStoragePrune(stdout, stderr io.Writer, f pruneFlags) error {
	dotenvPath := loadDotEnv()
	cfg, err := config.Load(dotenvPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if !cfg.S3Enabled() {
		return errors.New("storage prune requires the S3 backend (QATLAS_S3_* env all set); LocalStore has no version concept")
	}

	bucket, err := bucketForKind(cfg, f.kind)
	if err != nil {
		return err
	}
	store, err := objstore.NewS3Store(cfg.S3Endpoint, bucket, cfg.S3AccessKeyID, cfg.S3SecretAccessKey)
	if err != nil {
		return fmt.Errorf("connect s3: %w", err)
	}

	// Parse --older-than. We accept Go duration ("1h", "720h") plus
	// the operator-friendly d/w/y units. Empty string = no cap.
	var ageCap time.Duration
	if f.olderThan != "" {
		ageCap, err = parseDurationExt(f.olderThan)
		if err != nil {
			return fmt.Errorf("--older-than: %w", err)
		}
	}

	if f.keepLast < 0 {
		return errors.New("--keep-last must be >= 0")
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	fmt.Fprintf(stderr, "Bucket    : %s/%s (kind=%s)\n", cfg.S3Endpoint, bucket, f.kind)
	fmt.Fprintf(stderr, "Prefix    : %q\n", f.prefix)
	if ageCap > 0 {
		fmt.Fprintf(stderr, "Older than: %s (versions modified before %s)\n", ageCap, time.Now().Add(-ageCap).Format(time.RFC3339))
	} else {
		fmt.Fprintln(stderr, "Older than: (no age cap; all noncurrent versions eligible)")
	}
	if f.keepLast > 0 {
		fmt.Fprintf(stderr, "Keep last : %d (per key)\n", f.keepLast)
	} else {
		fmt.Fprintln(stderr, "Keep last : (no per-key cap)")
	}
	fmt.Fprintf(stderr, "Mode      : %s\n", modeLabel(f))
	fmt.Fprintln(stderr, "---")

	versions, err := store.ListAllVersions(ctx, f.prefix)
	if err != nil {
		return fmt.Errorf("list versions: %w", err)
	}

	candidates := planPruneCandidates(versions, ageCap, f.keepLast, time.Now())

	// Always print the plan so a dry-run shows exactly what an
	// --yes pass would do.
	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	if !f.jsonOutput {
		fmt.Fprintln(w, "KEY\tVERSION_ID\tSIZE\tAGE\tACTION")
	}
	var totalBytes int64
	for _, c := range candidates {
		age := time.Since(c.LastModified).Round(time.Second)
		totalBytes += c.Size
		if f.jsonOutput {
			// Minimal JSON — operators wanting full structure can
			// post-process, but most use cases (audit log, sed,
			// awk) want one-line-per-row.
			fmt.Fprintf(stdout, "{\"key\":%q,\"version_id\":%q,\"size\":%d,\"age_seconds\":%d,\"delete_marker\":%v}\n",
				c.Key, c.VersionID, c.Size, int64(age.Seconds()), c.IsDeleteMarker)
		} else {
			label := "DELETE_PLANNED"
			if c.IsDeleteMarker {
				label = "DELETE_MARKER_PLANNED"
			}
			fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\n", c.Key, c.VersionID, c.Size, age, label)
		}
	}
	_ = w.Flush()

	if len(candidates) == 0 {
		fmt.Fprintln(stderr, "no noncurrent versions match the filters; nothing to do")
		return nil
	}

	fmt.Fprintf(stderr, "---\ncandidates: %d versions, %.2f MiB total\n",
		len(candidates), float64(totalBytes)/(1<<20))

	if !f.apply {
		fmt.Fprintln(stderr, "dry-run only — pass --yes to delete the listed versions")
		return nil
	}

	// Apply. We delete sequentially because RustFS rate limits batch
	// delete and the operator-impact ceiling is "fast enough for
	// thousands of versions per run". For tens of thousands the loop
	// is still acceptable; cancel via Ctrl-C is honored via ctx.
	var deleted, failed int
	for _, c := range candidates {
		if err := store.DeleteVersion(ctx, c.Key, c.VersionID); err != nil {
			fmt.Fprintf(stderr, "FAIL %s @%s: %s\n", c.Key, c.VersionID, err)
			failed++
			continue
		}
		if !f.jsonOutput {
			fmt.Fprintf(stdout, "%s @%s DELETED\n", c.Key, c.VersionID)
		}
		deleted++
	}
	fmt.Fprintf(stderr, "---\ndeleted: %d, failed: %d, freed: %.2f MiB\n",
		deleted, failed, float64(totalBytes)/(1<<20))
	if failed > 0 {
		return fmt.Errorf("%d versions failed to delete (see stderr)", failed)
	}
	return nil
}

// planPruneCandidates filters the full version list down to those that
// pass the operator's policy gate. The rules:
//
//  1. Current (IsLatest) versions are NEVER deleted, regardless of age.
//     Same with delete markers that are themselves the latest entry
//     (deleting them would resurrect the prior version — unexpected).
//  2. Eligibility = noncurrent OR (delete marker that is not the
//     latest). Delete markers always count toward keep-last because
//     each one represents one prior version.
//  3. If --older-than is set, the version must be older than that
//     duration to be eligible.
//  4. If --keep-last is set, we group by key, sort eligible versions
//     newest-first, drop the first --keep-last, and only the
//     remainder are returned.
//
// Returns candidates sorted by key (ascending) then LastModified
// (descending) so the operator-facing list reads predictably.
func planPruneCandidates(versions []ObjectVersionForPrune, ageCap time.Duration, keepLast int, now time.Time) []ObjectVersionForPrune {
	// First pass: filter out always-keep (current + latest delete
	// marker) and apply the age cap.
	eligibleByKey := map[string][]ObjectVersionForPrune{}
	cutoff := now.Add(-ageCap)
	for _, v := range versions {
		if v.IsLatest {
			continue // never touch current version (or top delete marker)
		}
		if ageCap > 0 && v.LastModified.After(cutoff) {
			continue // too new to prune
		}
		eligibleByKey[v.Key] = append(eligibleByKey[v.Key], v)
	}

	// Per-key: sort newest-first, drop first keepLast (those stay).
	var out []ObjectVersionForPrune
	for _, vs := range eligibleByKey {
		sort.Slice(vs, func(i, j int) bool {
			return vs[i].LastModified.After(vs[j].LastModified)
		})
		if keepLast > 0 && len(vs) > keepLast {
			vs = vs[keepLast:]
		} else if keepLast > 0 {
			// all noncurrent for this key fit within keep-last → keep all
			continue
		}
		out = append(out, vs...)
	}

	// Stable cross-key order.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key != out[j].Key {
			return out[i].Key < out[j].Key
		}
		return out[i].LastModified.After(out[j].LastModified)
	})
	return out
}

// ObjectVersionForPrune is a local alias so the planning function can
// be unit-tested without importing the objstore package's S3-tied
// type. Same shape, no behaviour difference.
type ObjectVersionForPrune = objstore.ObjectVersion

// parseDurationExt extends time.ParseDuration with the operator-
// friendly units d (24h), w (168h), y (8760h). Anything time.ParseDuration
// already accepts (ns / us / ms / s / m / h) passes straight through.
func parseDurationExt(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, errors.New("empty duration")
	}
	switch last := s[len(s)-1]; last {
	case 'd', 'w', 'y':
		num, err := strconv.ParseFloat(s[:len(s)-1], 64)
		if err != nil {
			return 0, fmt.Errorf("parse %q: %w", s, err)
		}
		var mult time.Duration
		switch last {
		case 'd':
			mult = 24 * time.Hour
		case 'w':
			mult = 7 * 24 * time.Hour
		case 'y':
			mult = 365 * 24 * time.Hour
		}
		return time.Duration(num * float64(mult)), nil
	}
	return time.ParseDuration(s)
}

func modeLabel(f pruneFlags) string {
	if f.apply {
		return "APPLY (--yes set, deletions will execute)"
	}
	return "dry-run (pass --yes to apply)"
}

// bucketForKind maps a --kind value to the configured per-kind bucket.
// v0.7.0 split the single qatlas-raw bucket into qatlas-pdf /
// qatlas-md / qatlas-images, so prune (which talks to one bucket at a
// time) needs to know which one.
func bucketForKind(cfg *config.Config, kind string) (string, error) {
	switch kind {
	case "pdf":
		return cfg.S3BucketPDF, nil
	case "md", "markdown":
		return cfg.S3BucketMD, nil
	case "images", "img":
		return cfg.S3BucketImages, nil
	default:
		return "", fmt.Errorf("--kind %q invalid (want pdf | md | images)", kind)
	}
}

// silenceUnused keeps `os` imported when build tags strip the apply
// branch — defensive against future refactors. Not called.
var _ = os.Stdout
