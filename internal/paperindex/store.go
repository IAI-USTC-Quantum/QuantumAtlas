// Package paperindex provides a fast, in-process catalog of every paper
// known to QuantumAtlas, backed by a single Parquet object in the same
// RustFS bucket that holds the PDF / markdown / JSON assets.
//
// See docs/architecture.md → "论文元数据索引 (paperindex — Parquet +
// DuckDB Lakehouse 模式)" for the rationale (object store ⇄ query
// engine separation, no second database service, idempotent CAS flush
// to keep two edges in sync via the bucket itself).
//
// # Lifecycle (read path, Phase 1)
//
// At process startup, Store.New downloads index/papers.parquet from
// the bucket into a local cache file, then `CREATE TABLE papers AS
// SELECT * FROM read_parquet(<local>)` materialises it in an in-memory
// DuckDB instance. Subsequent queries (QueryStats, NeedsMineru, …) run
// against the in-memory table — sub-millisecond for the catalog sizes
// we expect (~10⁵ papers). A background goroutine re-downloads the
// parquet every RefreshInterval to pick up cross-edge writes.
//
// # Lifecycle (write path, Phase 2)
//
// Upsert{PDF,MD,JSON}Asset / UpsertJSONMetadata / AdjustImageCount /
// RemoveAsset mutate the in-memory papers table and set a dirty bit.
// A second background goroutine (flushLoop) wakes every FlushInterval,
// dumps the table to a local temp parquet, then re-uploads it to the
// bucket via objstore.PutWithOptions{IfMatch: lastETag} — the
// conditional PUT that the S3 wire protocol promises. On 412
// PreconditionFailed (another edge wrote between our Stat and Put) we
// re-Load and mark dirty again, so the next tick merges in our
// pending changes on top of whatever the other edge committed.
//
// Reads never block on writes: queries hit the in-memory table; flush
// only re-Stats/re-Puts the parquet object behind the scenes.
//
// # Schema migration
//
// The full target schema is defined once in schemaDDL. On Load we
// CREATE the table with the latest schema, then INSERT INTO it from
// the parquet by name-matching columns — any column the parquet
// happens to lack defaults to NULL. This means an old parquet
// (e.g. the bootstrap PoC's catalog without title/abstract columns)
// works against newer code; newer columns just stay NULL until the
// next upload event triggers an Upsert that populates them, or until
// `qatlas-server bootstrap-index` rebuilds the parquet from scratch.
package paperindex

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"

	_ "github.com/marcboeker/go-duckdb/v2"
)

// DefaultParquetKey is the object key inside the qatlas-raw bucket
// where the catalog parquet lives. All references go through this
// constant so the read path (Store.Load), the write path (flushLoop),
// the bootstrap subcommand, and the webhook handler stay in sync.
const DefaultParquetKey = "index/papers.parquet"

// DefaultRefreshInterval is how often the background ticker re-loads
// the parquet from object storage. 60s balances "see other edge's
// writes promptly" against "don't HEAD the bucket every second".
// Cross-edge writes show up locally within at most this interval.
const DefaultRefreshInterval = 60 * time.Second

// DefaultFlushInterval is the cadence at which dirty in-memory state
// is dumped to a new parquet and CAS-PUT back to the bucket. 5s
// trades latency-to-other-edges for fewer S3 round-trips on bursts.
const DefaultFlushInterval = 5 * time.Second

// schemaDDL is the canonical CREATE TABLE for the in-memory papers
// catalog. The columns fall into three groups:
//
//  1. Identity / partition: arxiv_id (PRIMARY KEY for ON CONFLICT
//     upsert semantics) + yymm (the arxiv year-month bucket used as
//     a key prefix in the bucket layout).
//  2. Arxiv metadata: title / abstract / authors / categories /
//     submitter / update_date — populated from json/<id>.json
//     events; NULL until that asset is ingested.
//  3. Per-kind asset state: has_X / X_size_bytes / X_uploaded_at /
//     X_etag — updated by webhook events as PDF / markdown / JSON
//     are PUT or DELETEd in the bucket. image_count is the per-paper
//     image-asset tally maintained incrementally via AdjustImageCount.
//
// authors and categories are stored as comma-joined VARCHAR for now
// (Parquet LIST<VARCHAR> works in DuckDB but adds complexity for
// downstream consumers; can migrate to arrays later if needed).
const schemaDDL = `CREATE OR REPLACE TABLE papers (
    arxiv_id          VARCHAR PRIMARY KEY,
    yymm              VARCHAR,
    title             VARCHAR,
    abstract          VARCHAR,
    authors           VARCHAR,
    categories        VARCHAR,
    submitter         VARCHAR,
    update_date       DATE,
    has_pdf           BOOLEAN,
    has_md            BOOLEAN,
    has_json          BOOLEAN,
    image_count       INTEGER,
    pdf_size_bytes    BIGINT,
    md_size_bytes     BIGINT,
    json_size_bytes   BIGINT,
    pdf_uploaded_at   TIMESTAMP,
    md_processed_at   TIMESTAMP,
    json_uploaded_at  TIMESTAMP,
    pdf_etag          VARCHAR,
    md_etag           VARCHAR,
    json_etag         VARCHAR
)`

// allCols mirrors schemaDDL's column order; used by loadFromParquet to
// build a column-by-name-matched INSERT projection that tolerates an
// older parquet missing newer columns.
var allCols = []string{
	"arxiv_id", "yymm",
	"title", "abstract", "authors", "categories", "submitter", "update_date",
	"has_pdf", "has_md", "has_json", "image_count",
	"pdf_size_bytes", "md_size_bytes", "json_size_bytes",
	"pdf_uploaded_at", "md_processed_at", "json_uploaded_at",
	"pdf_etag", "md_etag", "json_etag",
}

// Config bundles everything Store needs. Production callers wire in
// the objstore.Store already constructed for /api/papers handlers so
// paperindex shares the same bucket / credentials / dual-endpoint
// presign config. Tests can either pass a LocalStore against a temp
// dir, or use LocalParquetPath to skip Store entirely.
type Config struct {
	// Store is the bucket-backed object store the catalog lives in.
	// Reads use Store.Get to download the parquet; flush writes use
	// Store.PutWithOptions{IfMatch: lastETag} for cross-edge CAS.
	// In test setups that don't need either, leave nil and set
	// LocalParquetPath instead.
	Store objstore.Store

	// ParquetKey overrides DefaultParquetKey. Production should leave
	// empty; tests use this to point at a per-test object key.
	ParquetKey string

	// RefreshInterval overrides DefaultRefreshInterval. Tests set
	// short (e.g. 50ms) to exercise refresh in a few hundred ms.
	RefreshInterval time.Duration

	// FlushInterval overrides DefaultFlushInterval. Same test rationale.
	FlushInterval time.Duration

	// LocalParquetPath bypasses Store entirely for hermetic tests: the
	// catalog loads from this local file path, and flush writes back
	// to the same path (no CAS — the local filesystem isn't an S3
	// bucket). Setting this in production silently disables the flush
	// path's CAS guarantees, so we explicitly require Store to be nil
	// when LocalParquetPath is set.
	LocalParquetPath string
}

// Store is the in-process paperindex catalog. Construct one per
// server process at startup and reuse for the process lifetime; all
// methods are safe for concurrent use.
type Store struct {
	db  *sql.DB
	cfg Config

	// state covers mutable fields touched from both query goroutines
	// (RowCount, LoadedAt) and the background refresh/flush loops.
	// All exported accessors take the read lock; refresh and flush
	// take write.
	state struct {
		sync.RWMutex
		rowCount int
		loadedAt time.Time
		lastETag string    // ETag of the parquet object we last loaded or flushed
		dirty    bool      // set by Upsert*, cleared by successful flush
		dirtyAt  time.Time // time of first un-flushed mutation (debug only)
	}

	closeCh chan struct{}
	wg      sync.WaitGroup
}

// ----- Lifecycle ----------------------------------------------------

// New constructs and warms a Store: opens an in-memory DuckDB, applies
// schemaDDL, loads the catalog from the bucket (or LocalParquetPath),
// and starts the refresh + flush background goroutines. The flush
// goroutine is started even on a fresh / empty catalog so the very
// first Upsert can be persisted without needing a manual nudge.
//
// A missing parquet object is NOT an error: Store starts empty,
// QueryStats returns zeros, and the first flush will create the
// object. This makes paperindex idempotent across fresh deployments
// and bucket recreations.
func New(ctx context.Context, cfg Config) (*Store, error) {
	if cfg.RefreshInterval == 0 {
		cfg.RefreshInterval = DefaultRefreshInterval
	}
	if cfg.FlushInterval == 0 {
		cfg.FlushInterval = DefaultFlushInterval
	}
	if cfg.ParquetKey == "" {
		cfg.ParquetKey = DefaultParquetKey
	}
	if cfg.Store == nil && cfg.LocalParquetPath == "" {
		return nil, errors.New("paperindex: Config requires either Store or LocalParquetPath")
	}
	if cfg.Store != nil && cfg.LocalParquetPath != "" {
		return nil, errors.New("paperindex: Config.Store and LocalParquetPath are mutually exclusive")
	}

	db, err := sql.Open("duckdb", "") // empty DSN = ephemeral in-memory
	if err != nil {
		return nil, fmt.Errorf("paperindex: open duckdb: %w", err)
	}
	// Bumped from 1 → 8 (2026-05-29) for concurrent reads. DuckDB
	// uses MVCC + per-DB locking internally, so multiple connections
	// against the same in-memory database serialise writes but
	// parallelise reads. With 1 conn, dashboard pages with multiple
	// concurrent /api/papers/... queries would queue serially even
	// though each was sub-30ms; with 8 we serve typical small-team
	// concurrent traffic without queuing. Writes (Upsert path) still
	// serialise at the DuckDB level — pool size is a read-parallelism
	// tunable, not a write one.
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(2)

	s := &Store{
		db:      db,
		cfg:     cfg,
		closeCh: make(chan struct{}),
	}

	if _, err := db.ExecContext(ctx, schemaDDL); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("paperindex: apply schema: %w", err)
	}

	if err := s.Load(ctx); err != nil {
		slog.Warn("paperindex: initial Load failed; continuing with empty catalog",
			"error", err)
		// Don't fail — empty catalog is the documented soft-fail
		// behaviour for fresh deployments.
	}

	s.wg.Add(2)
	go s.refreshLoop()
	go s.flushLoop()
	return s, nil
}

// Close stops both background goroutines and releases DB resources.
// In-flight queries finish first; pending dirty state is flushed
// synchronously so a graceful shutdown doesn't lose just-written data.
func (s *Store) Close() error {
	close(s.closeCh)
	s.wg.Wait()
	// Final synchronous flush to persist any tail upserts.
	if s.isDirty() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := s.flush(ctx); err != nil {
			slog.Warn("paperindex: final flush at Close failed; data may be lost", "error", err)
		}
	}
	return s.db.Close()
}

// LoadedAt returns when the catalog was last (re)loaded from the source.
func (s *Store) LoadedAt() time.Time {
	s.state.RLock()
	defer s.state.RUnlock()
	return s.state.loadedAt
}

// RowCount returns the current number of papers in the in-memory table.
func (s *Store) RowCount() int {
	s.state.RLock()
	defer s.state.RUnlock()
	return s.state.rowCount
}

// ----- Load (download + materialise) --------------------------------

// Load (re)fetches the parquet object and re-materialises the papers
// table in DuckDB. Atomic from a query standpoint: the CREATE OR
// REPLACE TABLE statement is transactional in DuckDB, so concurrent
// readers see either the entire old or entire new table.
//
// If the parquet object is absent (404 from S3), Load resets the
// table to empty schema and clears lastETag so the next flush
// creates the object from scratch.
func (s *Store) Load(ctx context.Context) error {
	body, etag, found, err := s.fetchParquet(ctx)
	if err != nil {
		return fmt.Errorf("fetch parquet: %w", err)
	}
	if !found {
		if _, err := s.db.ExecContext(ctx, schemaDDL); err != nil {
			return fmt.Errorf("reset empty table: %w", err)
		}
		s.state.Lock()
		s.state.rowCount = 0
		s.state.lastETag = ""
		s.state.loadedAt = time.Now()
		s.state.Unlock()
		slog.Info("paperindex: source parquet not present yet; using empty catalog")
		return nil
	}

	tmpDir, err := os.MkdirTemp("", "paperindex-load-")
	if err != nil {
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	stagePath := filepath.Join(tmpDir, "papers.parquet")
	if err := os.WriteFile(stagePath, body, 0o600); err != nil {
		return fmt.Errorf("write staged parquet: %w", err)
	}

	if _, err := s.db.ExecContext(ctx, schemaDDL); err != nil {
		return fmt.Errorf("reset table: %w", err)
	}
	parquetCols, err := s.detectParquetCols(ctx, stagePath)
	if err != nil {
		return fmt.Errorf("detect parquet schema: %w", err)
	}
	selectExpr := buildInsertSelect(parquetCols)
	insertStmt := fmt.Sprintf(
		`INSERT INTO papers (%s) SELECT %s FROM read_parquet('%s')`,
		strings.Join(allCols, ","), selectExpr, strings.ReplaceAll(stagePath, "'", "''"),
	)
	if _, err := s.db.ExecContext(ctx, insertStmt); err != nil {
		return fmt.Errorf("insert from parquet: %w", err)
	}

	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM papers`).Scan(&n); err != nil {
		return fmt.Errorf("count: %w", err)
	}

	s.state.Lock()
	s.state.rowCount = n
	s.state.lastETag = etag
	s.state.loadedAt = time.Now()
	// Loading takes a fresh snapshot of cross-edge state; any local
	// upserts already in the table are still dirty (we want them
	// re-flushed on top of the new parquet) so we deliberately do
	// NOT clear the dirty flag here.
	s.state.Unlock()

	slog.Info("paperindex: catalog loaded", "rows", n, "etag", etag, "source", s.sourceDesc())
	return nil
}

// fetchParquet pulls the catalog parquet from Store or, in test mode,
// reads it from LocalParquetPath. Returns body, etag, found, err.
// found=false means the object doesn't exist yet (HTTP 404 / fs ENOENT)
// — caller should bootstrap from an empty schema and let flush create it.
func (s *Store) fetchParquet(ctx context.Context) (body []byte, etag string, found bool, err error) {
	if s.cfg.LocalParquetPath != "" {
		b, err := os.ReadFile(s.cfg.LocalParquetPath)
		if errors.Is(err, os.ErrNotExist) {
			return nil, "", false, nil
		}
		if err != nil {
			return nil, "", false, err
		}
		return b, "", true, nil // local mode has no ETag concept
	}

	rc, info, err := s.cfg.Store.Get(ctx, s.cfg.ParquetKey)
	if errors.Is(err, objstore.ErrNotFound) {
		return nil, "", false, nil
	}
	if err != nil {
		return nil, "", false, err
	}
	defer rc.Close()
	buf := bytes.Buffer{}
	if _, err := buf.ReadFrom(rc); err != nil {
		return nil, "", false, fmt.Errorf("read body: %w", err)
	}
	return buf.Bytes(), info.ETag, true, nil
}

// detectParquetCols reads the parquet's schema by DESCRIBE-ing a SELECT
// against it. DuckDB only fetches the parquet footer for this (a few KB).
func (s *Store) detectParquetCols(ctx context.Context, path string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT column_name FROM (DESCRIBE SELECT * FROM read_parquet('%s'))`,
			strings.ReplaceAll(path, "'", "''")))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var c string
		if err := rows.Scan(&c); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// buildInsertSelect returns a SELECT clause that maps the parquet's
// available columns into the target schema's column order: when a
// target col exists in the parquet, project it through; when missing,
// substitute typed-NULL. Output is comma-joined and safe to splice
// into "INSERT INTO papers (allCols...) SELECT <here> FROM read_parquet".
func buildInsertSelect(parquetCols []string) string {
	have := make(map[string]struct{}, len(parquetCols))
	for _, c := range parquetCols {
		have[c] = struct{}{}
	}
	parts := make([]string, len(allCols))
	for i, target := range allCols {
		if _, ok := have[target]; ok {
			parts[i] = target
		} else {
			parts[i] = "NULL AS " + target
		}
	}
	return strings.Join(parts, ", ")
}

func (s *Store) sourceDesc() string {
	if s.cfg.LocalParquetPath != "" {
		return s.cfg.LocalParquetPath
	}
	return "s3:" + s.cfg.ParquetKey
}

// ----- refreshLoop / flushLoop --------------------------------------

func (s *Store) refreshLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.cfg.RefreshInterval)
	defer t.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if err := s.Load(ctx); err != nil {
				slog.Warn("paperindex: refresh failed", "error", err)
			}
			cancel()
		}
	}
}

func (s *Store) flushLoop() {
	defer s.wg.Done()
	t := time.NewTicker(s.cfg.FlushInterval)
	defer t.Stop()
	for {
		select {
		case <-s.closeCh:
			return
		case <-t.C:
			if !s.isDirty() {
				continue
			}
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			if err := s.flush(ctx); err != nil {
				slog.Warn("paperindex: flush failed; will retry next tick", "error", err)
			}
			cancel()
		}
	}
}

// flush dumps the in-memory papers table to a fresh parquet and writes
// it back to object storage with an If-Match CAS precondition.
func (s *Store) flush(ctx context.Context) error {
	s.state.Lock()
	if !s.state.dirty {
		s.state.Unlock()
		return nil
	}
	s.state.dirty = false
	startedFromETag := s.state.lastETag
	s.state.Unlock()

	tmpDir, err := os.MkdirTemp("", "paperindex-flush-")
	if err != nil {
		s.markDirty()
		return fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	dumpPath := filepath.Join(tmpDir, "papers.parquet")
	if _, err := s.db.ExecContext(ctx,
		fmt.Sprintf(`COPY (SELECT * FROM papers) TO '%s' (FORMAT 'parquet', COMPRESSION 'zstd')`,
			strings.ReplaceAll(dumpPath, "'", "''"))); err != nil {
		s.markDirty()
		return fmt.Errorf("dump parquet: %w", err)
	}
	body, err := os.ReadFile(dumpPath)
	if err != nil {
		s.markDirty()
		return fmt.Errorf("read dump: %w", err)
	}

	if s.cfg.LocalParquetPath != "" {
		if err := os.WriteFile(s.cfg.LocalParquetPath, body, 0o600); err != nil {
			s.markDirty()
			return fmt.Errorf("write local parquet: %w", err)
		}
		return nil
	}

	opts := objstore.PutOptions{
		ContentType: "application/x-parquet",
	}
	if startedFromETag != "" {
		opts.IfMatch = startedFromETag
	} else {
		opts.IfNoneMatch = "*"
	}
	_, err = s.cfg.Store.PutWithOptions(ctx, s.cfg.ParquetKey,
		bytes.NewReader(body), int64(len(body)), opts)
	if err != nil {
		if objstore.IsPreconditionFailed(err) {
			slog.Info("paperindex: flush CAS conflict — reloading and retrying", "key", s.cfg.ParquetKey)
			if loadErr := s.Load(ctx); loadErr != nil {
				s.markDirty()
				return fmt.Errorf("flush conflict; reload also failed: %w", loadErr)
			}
			s.markDirty()
			return nil
		}
		s.markDirty()
		return fmt.Errorf("put parquet: %w", err)
	}

	info, exists, statErr := s.cfg.Store.Stat(ctx, s.cfg.ParquetKey)
	if statErr == nil && exists {
		s.state.Lock()
		s.state.lastETag = info.ETag
		s.state.Unlock()
	}
	return nil
}

func (s *Store) isDirty() bool {
	s.state.RLock()
	defer s.state.RUnlock()
	return s.state.dirty
}

func (s *Store) markDirty() {
	s.state.Lock()
	if !s.state.dirty {
		s.state.dirty = true
		s.state.dirtyAt = time.Now()
	}
	s.state.Unlock()
}

// ----- Upsert API ----------------------------------------------------

// UpsertPDFAsset records that PDF arxivID exists in the bucket with
// the given size/etag/upload-time. Schedules a background flush.
func (s *Store) UpsertPDFAsset(ctx context.Context, arxivID, yymm string, size int64, uploadedAt time.Time, etag string) error {
	return s.upsertAsset(ctx, arxivID, yymm, "pdf", size, uploadedAt, etag)
}

// UpsertMDAsset is the markdown counterpart of UpsertPDFAsset.
func (s *Store) UpsertMDAsset(ctx context.Context, arxivID, yymm string, size int64, processedAt time.Time, etag string) error {
	return s.upsertAsset(ctx, arxivID, yymm, "md", size, processedAt, etag)
}

// UpsertJSONAsset is the json-asset (size/etag) counterpart. For
// the metadata fields parsed FROM that json, additionally call
// UpsertJSONMetadata after fetching the file.
func (s *Store) UpsertJSONAsset(ctx context.Context, arxivID, yymm string, size int64, uploadedAt time.Time, etag string) error {
	return s.upsertAsset(ctx, arxivID, yymm, "json", size, uploadedAt, etag)
}

func (s *Store) upsertAsset(ctx context.Context, arxivID, yymm, kind string, size int64, ts time.Time, etag string) error {
	if arxivID == "" {
		return errors.New("paperindex: arxivID required")
	}
	sizeCol, tsCol, etagCol, hasCol := kindColumns(kind)
	if sizeCol == "" {
		return fmt.Errorf("paperindex: unknown asset kind %q", kind)
	}
	stmt := fmt.Sprintf(`
		INSERT INTO papers (arxiv_id, yymm, %s, %s, %s, %s)
		VALUES (?, ?, true, ?, ?, ?)
		ON CONFLICT (arxiv_id) DO UPDATE SET
			yymm = COALESCE(papers.yymm, excluded.yymm),
			%s = true,
			%s = excluded.%s,
			%s = excluded.%s,
			%s = excluded.%s`,
		hasCol, sizeCol, tsCol, etagCol,
		hasCol,
		sizeCol, sizeCol,
		tsCol, tsCol,
		etagCol, etagCol,
	)
	if _, err := s.db.ExecContext(ctx, stmt, arxivID, yymm, size, ts.UTC(), etag); err != nil {
		return fmt.Errorf("upsert %s asset for %s: %w", kind, arxivID, err)
	}
	s.markDirty()
	s.bumpRowCountCache(ctx)
	return nil
}

// JSONMetadata bundles the arxiv-side metadata fields extracted from
// json/<id>.json. Callers populate this from the parsed JSON.
type JSONMetadata struct {
	Title      string
	Abstract   string
	Authors    string // already joined (comma-separated)
	Categories string
	Submitter  string
	UpdateDate time.Time // arxiv "update_date" — date-only; time portion ignored
}

// UpsertJSONMetadata records the asset (size/etag) AND populates the
// metadata columns. Called by the webhook handler AFTER fetching and
// parsing json/<id>.json on a json upload event, and by the bootstrap
// subcommand for backfill.
//
// Unlike upsertAsset, this one COALESCEs existing metadata: a partial
// payload (e.g. empty title in a re-upload) won't clobber a prior good
// value.
func (s *Store) UpsertJSONMetadata(ctx context.Context, arxivID, yymm string, size int64, uploadedAt time.Time, etag string, md JSONMetadata) error {
	if arxivID == "" {
		return errors.New("paperindex: arxivID required")
	}
	var updateDate any
	if !md.UpdateDate.IsZero() {
		updateDate = md.UpdateDate.UTC()
	}
	stmt := `
		INSERT INTO papers (
			arxiv_id, yymm,
			has_json, json_size_bytes, json_uploaded_at, json_etag,
			title, abstract, authors, categories, submitter, update_date
		)
		VALUES (?, ?, true, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (arxiv_id) DO UPDATE SET
			yymm = COALESCE(papers.yymm, excluded.yymm),
			has_json = true,
			json_size_bytes = excluded.json_size_bytes,
			json_uploaded_at = excluded.json_uploaded_at,
			json_etag = excluded.json_etag,
			title = COALESCE(NULLIF(excluded.title, ''), papers.title),
			abstract = COALESCE(NULLIF(excluded.abstract, ''), papers.abstract),
			authors = COALESCE(NULLIF(excluded.authors, ''), papers.authors),
			categories = COALESCE(NULLIF(excluded.categories, ''), papers.categories),
			submitter = COALESCE(NULLIF(excluded.submitter, ''), papers.submitter),
			update_date = COALESCE(excluded.update_date, papers.update_date)`
	if _, err := s.db.ExecContext(ctx, stmt,
		arxivID, yymm, size, uploadedAt.UTC(), etag,
		md.Title, md.Abstract, md.Authors, md.Categories, md.Submitter, updateDate,
	); err != nil {
		return fmt.Errorf("upsert json metadata for %s: %w", arxivID, err)
	}
	s.markDirty()
	s.bumpRowCountCache(ctx)
	return nil
}

// AdjustImageCount adds delta (may be negative) to the per-paper image
// count. Webhook handler calls AdjustImageCount(+1) on ObjectCreated
// for an images/<yymm>/<id>/<…> key and AdjustImageCount(-1) on
// ObjectRemoved. If the paper row doesn't exist yet (image arrived
// before any other asset), creates a stub row with arxiv_id+yymm.
func (s *Store) AdjustImageCount(ctx context.Context, arxivID, yymm string, delta int) error {
	if arxivID == "" {
		return errors.New("paperindex: arxivID required")
	}
	stmt := `
		INSERT INTO papers (arxiv_id, yymm, image_count) VALUES (?, ?, ?)
		ON CONFLICT (arxiv_id) DO UPDATE SET
			yymm = COALESCE(papers.yymm, excluded.yymm),
			image_count = COALESCE(papers.image_count, 0) + ?`
	if _, err := s.db.ExecContext(ctx, stmt, arxivID, yymm, delta, delta); err != nil {
		return fmt.Errorf("adjust image count for %s: %w", arxivID, err)
	}
	s.markDirty()
	s.bumpRowCountCache(ctx)
	return nil
}

// RemoveAsset records the deletion of a single asset (kind ∈ "pdf" /
// "md" / "json"). The corresponding has_X flag is cleared and the
// size/uploaded_at/etag columns are reset to NULL. Other assets on
// the same paper are untouched.
func (s *Store) RemoveAsset(ctx context.Context, arxivID, kind string) error {
	if arxivID == "" {
		return errors.New("paperindex: arxivID required")
	}
	sizeCol, tsCol, etagCol, hasCol := kindColumns(kind)
	if sizeCol == "" {
		return fmt.Errorf("paperindex: unknown asset kind %q", kind)
	}
	stmt := fmt.Sprintf(`
		UPDATE papers SET
			%s = false,
			%s = NULL,
			%s = NULL,
			%s = NULL
		WHERE arxiv_id = ?`,
		hasCol, sizeCol, tsCol, etagCol)
	if _, err := s.db.ExecContext(ctx, stmt, arxivID); err != nil {
		return fmt.Errorf("remove %s asset for %s: %w", kind, arxivID, err)
	}
	s.markDirty()
	return nil
}

func kindColumns(kind string) (size, ts, etag, has string) {
	switch kind {
	case "pdf":
		return "pdf_size_bytes", "pdf_uploaded_at", "pdf_etag", "has_pdf"
	case "md":
		return "md_size_bytes", "md_processed_at", "md_etag", "has_md"
	case "json":
		return "json_size_bytes", "json_uploaded_at", "json_etag", "has_json"
	}
	return "", "", "", ""
}

func (s *Store) bumpRowCountCache(ctx context.Context) {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM papers`).Scan(&n); err == nil {
		s.state.Lock()
		s.state.rowCount = n
		s.state.Unlock()
	}
}

// ----- Query API ----------------------------------------------------

// Stats is the aggregate counter snapshot.
type Stats struct {
	Total       int
	HasPDF      int
	HasMD       int
	HasJSON     int
	NeedsMineru int
	NeedsJSON   int
	TotalImages int
	LoadedAt    time.Time
}

func (s *Store) QueryStats(ctx context.Context) (Stats, error) {
	var st Stats
	// NB: NOT coalesce(has_md, false) is required because brand-new rows
	// inserted by UpsertPDFAsset don't touch has_md → it stays NULL, and
	// `NOT NULL` evaluates to NULL (SQL three-valued logic), making the
	// row vanish from "needs mineru" counts. COALESCE flattens to false.
	row := s.db.QueryRowContext(ctx, `
		SELECT
			count(*),
			coalesce(sum(CASE WHEN coalesce(has_pdf, false) THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN coalesce(has_md, false) THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN coalesce(has_json, false) THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN coalesce(has_pdf, false) AND NOT coalesce(has_md, false) THEN 1 ELSE 0 END), 0),
			coalesce(sum(CASE WHEN coalesce(has_pdf, false) AND NOT coalesce(has_json, false) THEN 1 ELSE 0 END), 0),
			coalesce(sum(image_count), 0)
		FROM papers`)
	if err := row.Scan(&st.Total, &st.HasPDF, &st.HasMD, &st.HasJSON,
		&st.NeedsMineru, &st.NeedsJSON, &st.TotalImages); err != nil {
		return st, fmt.Errorf("paperindex: query stats: %w", err)
	}
	st.LoadedAt = s.LoadedAt()
	return st, nil
}

// NeedsMineruRow projects one "PDF without markdown" paper.
type NeedsMineruRow struct {
	ArxivID       string
	YYMM          string
	PDFKey        string
	PDFSizeBytes  int64
	PDFUploadedAt sql.NullTime
}

func (s *Store) NeedsMineru(ctx context.Context, limit int) ([]NeedsMineruRow, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT arxiv_id, yymm,
		       'pdf/' || yymm || '/' || arxiv_id || '.pdf' AS pdf_key,
		       pdf_size_bytes, pdf_uploaded_at
		FROM papers
		WHERE coalesce(has_pdf, false) AND NOT coalesce(has_md, false)
		ORDER BY pdf_uploaded_at DESC NULLS LAST
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("paperindex: needs-mineru query: %w", err)
	}
	defer rows.Close()
	var out []NeedsMineruRow
	for rows.Next() {
		var r NeedsMineruRow
		if err := rows.Scan(&r.ArxivID, &r.YYMM, &r.PDFKey, &r.PDFSizeBytes, &r.PDFUploadedAt); err != nil {
			return nil, fmt.Errorf("paperindex: scan needs-mineru row: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
