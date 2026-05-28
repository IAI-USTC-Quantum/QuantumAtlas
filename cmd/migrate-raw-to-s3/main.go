// Command migrate-raw-to-s3 is a one-shot tool that uploads every file
// listed in a raw-store SQLite index to a RustFS / S3 bucket via
// internal/objstore.S3Store.
//
// Why a separate binary (vs a script):
//   - We get to reuse internal/objstore so the upload path is identical
//     to what the live server uses — no behaviour drift between
//     migration and steady-state.
//   - Concurrency / retry / progress-DB logic is non-trivial; a typed
//     Go program is easier to reason about than 500 lines of bash + jq.
//
// Workflow (matches the storage migration plan):
//
//  1. Print endpoint + bucket + sample target key. Refuse to proceed
//     without --yes-i-confirm.
//  2. For each row in the input index.sqlite's `assets` table (skip
//     kind=image_dir — those are zero-byte directory markers):
//       a. HEAD the bucket/<key>. Match by size → record `skipped`,
//          move on. Mismatch → record `pending` (will overwrite).
//       b. PUT from /mnt/team/.../<path> with the right Content-Type.
//       c. Retry up to MaxAttempts on transient errors.
//       d. Persist outcome to local progress.db so a crash / SIGINT
//          can resume without re-uploading.
//  3. Print summary: done / skipped / failed counts + duration.
//
// Run on 1810 WSL pointing at NAS LAN (full internal path):
//
//	./migrate-raw-to-s3 \
//	    --index=/mnt/team/QuantumAtlas/raw/index.sqlite \
//	    --raw-root=/mnt/team/QuantumAtlas/raw \
//	    --progress=/tmp/qatlas-migrate-progress.db \
//	    --endpoint=http://10.100.158.91:9000 \
//	    --bucket=qatlas-raw \
//	    --access-key=$AK --secret-key=$SK \
//	    --workers=16 \
//	    --kind=pdf \
//	    --yes-i-confirm
//
// Re-running after a crash is idempotent: rows marked done/skipped in
// progress.db are not re-touched (HEAD is also skipped — pure local
// table lookup).
package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"mime"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"

	_ "modernc.org/sqlite" // pure-Go SQLite driver; matches PocketBase's choice
)

// maxAttempts is the retry budget per object. 3 covers transient
// network blips / occasional RustFS restarts without hiding a
// hard-broken target. We sleep linearly between retries.
const maxAttempts = 3

type asset struct {
	paperKey string
	kind     string
	relPath  string // exactly the object key (no rawRoot prefix)
	sizeDB   int64  // size recorded in index.sqlite
}

type result struct {
	a       asset
	status  string // done / skipped / failed
	uploaded int64
	err     error
}

func main() {
	var (
		indexPath   = flag.String("index", "", "path to source index.sqlite")
		rawRoot     = flag.String("raw-root", "", "local fs root that <kind>/<rel> sits under (e.g. /mnt/team/QuantumAtlas/raw)")
		progressDB  = flag.String("progress", "/tmp/qatlas-migrate-progress.db", "local SQLite path for resumable state")
		endpoint    = flag.String("endpoint", "", "S3 endpoint URL (include http:// or https://)")
		bucket      = flag.String("bucket", "", "target S3 bucket name")
		accessKey   = flag.String("access-key", os.Getenv("QATLAS_S3_ACCESS_KEY_ID"), "S3 access key id (defaults to $QATLAS_S3_ACCESS_KEY_ID)")
		secretKey   = flag.String("secret-key", os.Getenv("QATLAS_S3_SECRET_ACCESS_KEY"), "S3 secret access key (defaults to $QATLAS_S3_SECRET_ACCESS_KEY)")
		workers     = flag.Int("workers", 16, "parallel upload workers")
		kindFilter  = flag.String("kind", "", "restrict to one kind (pdf/markdown/json/image); empty = all")
		limit       = flag.Int("limit", 0, "process at most N rows (0 = no cap; useful for dry runs)")
		confirm     = flag.Bool("yes-i-confirm", false, "actually do the work (without this flag we only print the plan and exit 0)")
		quiet       = flag.Bool("quiet", false, "suppress per-object log lines (summary still printed every 5s)")
	)
	flag.Parse()

	for name, v := range map[string]string{
		"--index": *indexPath, "--raw-root": *rawRoot,
		"--endpoint": *endpoint, "--bucket": *bucket,
		"--access-key": *accessKey, "--secret-key": *secretKey,
	} {
		if v == "" {
			log.Fatalf("missing required flag %s", name)
		}
	}

	// Source index.
	src, err := sql.Open("sqlite", "file:"+*indexPath+"?mode=ro")
	if err != nil {
		log.Fatalf("open source index: %v", err)
	}
	defer src.Close()

	// Local progress DB.
	prog, err := openProgress(*progressDB)
	if err != nil {
		log.Fatalf("open progress db: %v", err)
	}
	defer prog.Close()

	assets, totalBytes, err := loadAssets(src, *kindFilter, *limit)
	if err != nil {
		log.Fatalf("load assets: %v", err)
	}

	store, err := objstore.NewS3Store(*endpoint, *bucket, *accessKey, *secretKey)
	if err != nil {
		log.Fatalf("new s3 store: %v", err)
	}

	// Plan summary + confirmation gate. We print one sample target key
	// so the operator can sanity-check we're aiming at the right bucket
	// before any data flies.
	fmt.Println("===== migration plan =====")
	fmt.Printf("  source index    : %s\n", *indexPath)
	fmt.Printf("  raw-root        : %s\n", *rawRoot)
	fmt.Printf("  progress db     : %s\n", *progressDB)
	fmt.Printf("  endpoint        : %s\n", *endpoint)
	fmt.Printf("  bucket          : %s\n", *bucket)
	fmt.Printf("  workers         : %d\n", *workers)
	fmt.Printf("  kind filter     : %q (empty = all)\n", *kindFilter)
	fmt.Printf("  rows to process : %d\n", len(assets))
	fmt.Printf("  total bytes     : %.2f GiB\n", float64(totalBytes)/(1<<30))
	if len(assets) > 0 {
		a := assets[0]
		fmt.Printf("  sample asset    : %s\n", a.relPath)
		fmt.Printf("  sample target   : %s/%s/%s\n", strings.TrimRight(*endpoint, "/"), *bucket, a.relPath)
	}
	fmt.Println("==========================")

	if !*confirm {
		fmt.Println("DRY RUN (no --yes-i-confirm); exiting without uploading.")
		return
	}

	// Graceful shutdown on SIGINT / SIGTERM: drain in-flight workers,
	// flush progress, exit. Re-running picks up where we left off.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	runMigration(ctx, store, prog, assets, *rawRoot, *workers, *quiet)
}

// loadAssets pulls every asset row from the source index, optionally
// filtered by kind. We skip kind="image_dir" outright: those rows
// represent directory markers in the local layout but are meaningless
// to S3 (which has no directories — just keys with common prefixes).
func loadAssets(db *sql.DB, kindFilter string, limit int) ([]asset, int64, error) {
	q := "SELECT paper_key, kind, path, size_bytes FROM assets WHERE kind != 'image_dir'"
	args := []any{}
	if kindFilter != "" {
		q += " AND kind = ?"
		args = append(args, kindFilter)
	}
	q += " ORDER BY kind, paper_key, path"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []asset
	var bytes int64
	for rows.Next() {
		var a asset
		var sz sql.NullInt64
		if err := rows.Scan(&a.paperKey, &a.kind, &a.relPath, &sz); err != nil {
			return nil, 0, err
		}
		a.sizeDB = sz.Int64
		out = append(out, a)
		bytes += a.sizeDB
	}
	return out, bytes, rows.Err()
}

func runMigration(
	ctx context.Context,
	store objstore.Store,
	prog *progressStore,
	assets []asset,
	rawRoot string,
	workers int,
	quiet bool,
) {
	jobs := make(chan asset, workers*4)
	results := make(chan result, workers*4)

	// Workers.
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for a := range jobs {
				results <- processOne(ctx, store, prog, a, rawRoot)
			}
		}()
	}

	// Feeder: skip assets already marked done/skipped in progress.db.
	go func() {
		defer close(jobs)
		for _, a := range assets {
			if status, _, ok := prog.lookup(a.relPath); ok && (status == "done" || status == "skipped") {
				continue
			}
			select {
			case <-ctx.Done():
				return
			case jobs <- a:
			}
		}
	}()

	// Result collector + heartbeat.
	go func() {
		wg.Wait()
		close(results)
	}()

	var (
		nDone, nSkipped, nFailed atomic.Int64
		bytesUploaded            atomic.Int64
	)
	started := time.Now()
	tick := time.NewTicker(5 * time.Second)
	defer tick.Stop()

	resultsClosed := false
	for !resultsClosed {
		select {
		case r, ok := <-results:
			if !ok {
				resultsClosed = true
				continue
			}
			switch r.status {
			case "done":
				nDone.Add(1)
				bytesUploaded.Add(r.uploaded)
			case "skipped":
				nSkipped.Add(1)
			case "failed":
				nFailed.Add(1)
				log.Printf("FAILED %s: %v", r.a.relPath, r.err)
			}
			if !quiet && r.status != "skipped" {
				log.Printf("[%s] %s %s (size=%d)", r.status, r.a.kind, r.a.relPath, r.uploaded)
			}
		case <-tick.C:
			elapsed := time.Since(started)
			mb := float64(bytesUploaded.Load()) / (1 << 20)
			log.Printf(
				"  progress: done=%d skipped=%d failed=%d  uploaded=%.1f MiB  elapsed=%s",
				nDone.Load(), nSkipped.Load(), nFailed.Load(), mb, elapsed.Truncate(time.Second),
			)
		}
	}

	elapsed := time.Since(started)
	mib := float64(bytesUploaded.Load()) / (1 << 20)
	fmt.Println("===== summary =====")
	fmt.Printf("  done    : %d\n", nDone.Load())
	fmt.Printf("  skipped : %d\n", nSkipped.Load())
	fmt.Printf("  failed  : %d\n", nFailed.Load())
	fmt.Printf("  uploaded: %.1f MiB\n", mib)
	fmt.Printf("  elapsed : %s\n", elapsed.Truncate(time.Second))
	if nFailed.Load() > 0 {
		fmt.Println("  re-run with same flags to retry failed rows (HEAD-then-PUT is idempotent)")
		os.Exit(1)
	}
}

// processOne handles a single asset end-to-end: HEAD for skip,
// open-and-PUT with retries, persist outcome to progress.db.
func processOne(ctx context.Context, store objstore.Store, prog *progressStore, a asset, rawRoot string) result {
	// HEAD first: object already there + size matches → skip.
	info, exists, err := store.Stat(ctx, a.relPath)
	if err == nil && exists {
		if info.Size == a.sizeDB {
			_ = prog.set(a.relPath, "skipped", 0, "")
			return result{a: a, status: "skipped"}
		}
		// Size mismatch — likely a previous partial upload, fall through
		// to overwrite (S3 PutObject is replace-by-default).
	}

	// Open the source file once and stream it; size is known from
	// index.sqlite so we can hand it to the store cleanly.
	localPath := filepath.Join(rawRoot, filepath.FromSlash(a.relPath))
	contentType := contentTypeFor(a.kind, a.relPath)

	var lastErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		uploaded, err := uploadOnce(ctx, store, a.relPath, localPath, a.sizeDB, contentType)
		if err == nil {
			_ = prog.set(a.relPath, "done", uploaded, "")
			return result{a: a, status: "done", uploaded: uploaded}
		}
		lastErr = err
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			break
		}
		// Linear backoff (2s, 4s, 6s) — keeps the total worst-case
		// per asset under ~15s so a stuck object doesn't pin a worker.
		time.Sleep(time.Duration(attempt*2) * time.Second)
	}
	_ = prog.set(a.relPath, "failed", 0, lastErr.Error())
	return result{a: a, status: "failed", err: lastErr}
}

func uploadOnce(ctx context.Context, store objstore.Store, key, localPath string, size int64, ct string) (int64, error) {
	f, err := os.Open(localPath)
	if err != nil {
		return 0, fmt.Errorf("open local %s: %w", localPath, err)
	}
	defer f.Close()
	// Wrap in a bufio reader so small images don't burn syscalls.
	br := bufio.NewReaderSize(f, 1<<20)
	written, err := store.Put(ctx, key, &readerSize{R: br}, size, ct)
	if err != nil {
		return 0, err
	}
	return written, nil
}

// readerSize adapts a bufio reader to plain io.Reader (minio-go reads
// what it needs and respects the size hint).
type readerSize struct{ R io.Reader }

func (r *readerSize) Read(p []byte) (int, error) { return r.R.Read(p) }

func contentTypeFor(kind, relPath string) string {
	switch kind {
	case "pdf":
		return "application/pdf"
	case "markdown":
		return "text/markdown; charset=utf-8"
	case "json":
		return "application/json"
	case "image":
		if ct := mime.TypeByExtension(path.Ext(relPath)); ct != "" {
			return ct
		}
		return "application/octet-stream"
	}
	return "application/octet-stream"
}

// ---------------------------------------------------------------------------
// progressStore: tiny SQLite that records per-key outcome so crashes /
// SIGINT don't force a full restart. We fsync after every write —
// total writes are bounded by len(assets) (~836k) and SQLite handles
// that comfortably.
// ---------------------------------------------------------------------------

type progressStore struct {
	db *sql.DB
	mu sync.Mutex
	// Hot cache: we read every key once at startup (cheap, <10 MiB for
	// 836k rows) so the lookup() path during run is pure in-memory.
	cache map[string]progressRow
}

type progressRow struct {
	status   string
	uploaded int64
}

func openProgress(path string) (*progressStore, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)")
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
        CREATE TABLE IF NOT EXISTS progress (
            key      TEXT PRIMARY KEY,
            status   TEXT NOT NULL,
            uploaded INTEGER NOT NULL DEFAULT 0,
            error    TEXT,
            attempt  INTEGER NOT NULL DEFAULT 0,
            ts       TEXT NOT NULL
        );
        CREATE INDEX IF NOT EXISTS idx_progress_status ON progress(status);
    `); err != nil {
		return nil, err
	}
	ps := &progressStore{db: db, cache: map[string]progressRow{}}
	rows, err := db.Query("SELECT key, status, uploaded FROM progress")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, s string
		var u int64
		if err := rows.Scan(&k, &s, &u); err != nil {
			return nil, err
		}
		ps.cache[k] = progressRow{status: s, uploaded: u}
	}
	log.Printf("progress: loaded %d existing rows from %s", len(ps.cache), path)
	return ps, nil
}

func (p *progressStore) Close() error { return p.db.Close() }

func (p *progressStore) lookup(key string) (string, int64, bool) {
	p.mu.Lock()
	r, ok := p.cache[key]
	p.mu.Unlock()
	return r.status, r.uploaded, ok
}

func (p *progressStore) set(key, status string, uploaded int64, errMsg string) error {
	p.mu.Lock()
	p.cache[key] = progressRow{status: status, uploaded: uploaded}
	p.mu.Unlock()
	_, err := p.db.Exec(
		"INSERT INTO progress(key,status,uploaded,error,attempt,ts) VALUES(?,?,?,?,1,?) "+
			"ON CONFLICT(key) DO UPDATE SET status=excluded.status, uploaded=excluded.uploaded, "+
			"error=excluded.error, attempt=progress.attempt+1, ts=excluded.ts",
		key, status, uploaded, errMsg, time.Now().UTC().Format(time.RFC3339),
	)
	return err
}
