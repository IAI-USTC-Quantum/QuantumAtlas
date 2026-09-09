// Package downloadfleet implements the durable, approval-gated outbound PDF fleet.
package downloadfleet

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrDisabled     = errors.New("download fleet disabled")
	ErrUnauthorized = errors.New("invalid worker credential")
	ErrForbidden    = errors.New("worker is not approved")
	ErrConflict     = errors.New("attempt is no longer current or lease expired")
	ErrNotFound     = errors.New("not found")
)

type Config struct {
	Enabled           bool
	MaxInFlight       int
	MaxWorkerInFlight int
	MaxWorkerAttempts int
	TaskTimeout       time.Duration
	WorkerTimeout     time.Duration
	LeaseDuration     time.Duration
	UploadTimeout     time.Duration // independent bounded ready/upload phase, default 2m
	EnrollmentTTL     time.Duration
	Retention         time.Duration
	SpoolDir          string
	SpoolMaxBytes     int64
	MaxPDFBytes       int64
}

func (c Config) defaults() Config {
	if c.MaxInFlight == 0 {
		c.MaxInFlight = 6
	}
	if c.MaxWorkerInFlight == 0 {
		c.MaxWorkerInFlight = 2
	}
	if c.MaxWorkerAttempts == 0 {
		c.MaxWorkerAttempts = 3
	}
	if c.TaskTimeout == 0 {
		c.TaskTimeout = 15 * time.Minute
	}
	if c.WorkerTimeout == 0 {
		c.WorkerTimeout = 6 * time.Minute
	}
	if c.LeaseDuration == 0 {
		c.LeaseDuration = 60 * time.Second
	}
	if c.UploadTimeout == 0 {
		c.UploadTimeout = 2 * time.Minute
	}
	if c.EnrollmentTTL == 0 {
		c.EnrollmentTTL = 15 * time.Minute
	}
	if c.Retention == 0 {
		c.Retention = 7 * 24 * time.Hour
	}
	if c.SpoolDir == "" {
		c.SpoolDir = filepath.Join(os.TempDir(), "qatlas-downloadfleet")
	}
	if c.MaxPDFBytes == 0 {
		c.MaxPDFBytes = downloader.DefaultMaxPDFBytes
	}
	if c.SpoolMaxBytes == 0 {
		c.SpoolMaxBytes = 1 << 30
	}
	return c
}
func (c Config) validate() error {
	// Defaults are operational recommendations, not product ceilings. Separate
	// safety ceilings bound per-task histories and long-lived abandoned work.
	if c.MaxInFlight < 1 || c.MaxWorkerInFlight < 1 || c.MaxWorkerAttempts < 1 || c.MaxWorkerAttempts > 32 {
		return errors.New("fleet concurrency must be positive; attempts must be 1..32 (default 3)")
	}
	if c.TaskTimeout <= 0 || c.TaskTimeout > 24*time.Hour || c.WorkerTimeout <= 0 || c.WorkerTimeout > time.Hour || c.LeaseDuration <= 0 || c.LeaseDuration > 5*time.Minute || c.LeaseDuration > c.WorkerTimeout || c.WorkerTimeout > c.TaskTimeout {
		return errors.New("fleet durations require lease <= worker <= task; safety ceilings 5m/1h/24h")
	}
	if c.UploadTimeout <= 0 || c.UploadTimeout > 10*time.Minute {
		return errors.New("upload timeout must be positive and at most 10m")
	}
	if c.EnrollmentTTL <= 0 || c.Retention <= 0 || c.MaxPDFBytes < downloader.DefaultMinPDFBytes || c.MaxPDFBytes > downloader.DefaultMaxPDFBytes || c.SpoolMaxBytes < 1 {
		return errors.New("invalid fleet retention, enrollment TTL or spool limits")
	}
	return nil
}

type ArchiveFunc func(context.Context, registry.PaperRef, *downloader.FetchOutcome) error
type Service struct {
	pool    *pgxpool.Pool
	cfg     Config
	mu      sync.RWMutex
	archive ArchiveFunc
	hooks   ArchiveFunc
}

func New(pool *pgxpool.Pool, cfg Config) (*Service, error) {
	cfg = cfg.defaults()
	if e := cfg.validate(); e != nil {
		return nil, e
	}
	if cfg.Enabled && pool == nil {
		return nil, errors.New("download fleet requires PostgreSQL")
	}
	if cfg.Enabled {
		p, e := filepath.Abs(cfg.SpoolDir)
		if e != nil {
			return nil, e
		}
		cfg.SpoolDir = p
		if e = os.MkdirAll(p, 0700); e != nil {
			return nil, e
		}
	}
	return &Service{pool: pool, cfg: cfg}, nil
}
func (s *Service) Enabled() bool { return s != nil && s.cfg.Enabled && s.pool != nil }

// SetArchive must be called before serving. Callback must be idempotent across crash retries.
func (s *Service) SetArchive(f ArchiveFunc) { s.mu.Lock(); defer s.mu.Unlock(); s.archive = f }

// SetHooks installs the at-least-once durable post-archive callback. It must be idempotent.
func (s *Service) SetHooks(f ArchiveFunc) { s.mu.Lock(); defer s.mu.Unlock(); s.hooks = f }
func (s *Service) callbacks() (ArchiveFunc, ArchiveFunc) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.archive, s.hooks
}
func randomID() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}
func hash(v string) []byte { h := sha256.Sum256([]byte(v)); return h[:] }
func truncate(v string, n int) string {
	if len(v) > n {
		return strings.ToValidUTF8(v[:n], "")
	}
	return strings.ToValidUTF8(v, "")
}
func wireRef(r registry.PaperRef) wp.PaperRef {
	return wp.PaperRef{ArxivID: r.ArxivID, DOI: r.DOI, OpenAlexID: r.OpenAlexID, Title: r.Title, Authors: r.Authors, Year: r.Year}
}
func identity(r registry.PaperRef) (string, error) {
	if r.ArxivID != "" {
		p, e := paperassets.Parse(r.ArxivID)
		if e != nil {
			return "", e
		}
		return "arxiv:" + p.Canonical, nil
	}
	if v, ok := paperassets.ValidateDOI(r.DOI); ok {
		return "doi:" + v, nil
	}
	return "", errors.New("valid arxiv ID or DOI required")
}
func interval(d time.Duration) string { return fmt.Sprintf("%d microseconds", d.Microseconds()) }
func (s *Service) FetchPDF(ctx context.Context, ref registry.PaperRef) (*downloader.FetchOutcome, error) {
	if !s.Enabled() {
		return nil, ErrDisabled
	}
	key, e := identity(ref)
	if e != nil {
		return nil, e
	}
	raw, e := json.Marshal(ref)
	if e != nil {
		return nil, e
	}
	if len(raw) > 32768 {
		return nil, errors.New("paper reference too large")
	}
	id, deadline, e := s.enqueue(ctx, key, raw)
	if e != nil {
		return remotePending(id, e)
	}
	// Query with the caller's context BEFORE evaluating the stored deadline.
	// A recovered done/failed task remains observable after its execution deadline.
	// Cancellation or uncertainty is pending, never evidence of terminal failure.
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		var state, msg string
		var out []byte
		if e = s.pool.QueryRow(ctx, `SELECT state,error,outcome FROM download_fleet_tasks WHERE id=$1`, id).Scan(&state, &msg, &out); e != nil {
			return remotePending(id, e)
		}
		if state == "done" {
			var o downloader.FetchOutcome
			if e = json.Unmarshal(out, &o); e != nil {
				return remotePending(id, e)
			}
			o.Pending = false
			o.RemoteTaskID = id
			o.Archived = true
			return &o, nil
		}
		if state == "failed" {
			return &downloader.FetchOutcome{RemoteTaskID: id}, fmt.Errorf("remote download failed: %s", msg)
		}
		if !deadline.After(time.Now()) {
			return remotePending(id, context.DeadlineExceeded)
		}
		select {
		case <-ctx.Done():
			return remotePending(id, ctx.Err())
		case <-ticker.C:
		}
	}
}

type Enrollment struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

func (s *Service) Enrollment(ctx context.Context) (Enrollment, error) {
	if !s.Enabled() {
		return Enrollment{}, ErrDisabled
	}
	v := Enrollment{Token: randomID(), ExpiresAt: time.Now().Add(s.cfg.EnrollmentTTL)}
	_, e := s.pool.Exec(ctx, `INSERT INTO download_fleet_enrollments(token_hash,expires_at) VALUES($1,$2)`, hash(v.Token), v.ExpiresAt)
	return v, e
}

type Job struct {
	ID         string    `json:"id"`
	WorkerID   string    `json:"worker_id"`
	State      string    `json:"state"`
	Identifier string    `json:"identifier"`
	Error      string    `json:"error"`
	UpdatedAt  time.Time `json:"updated_at"`
}
type Snapshot struct {
	Workers []wp.Node `json:"workers"`
	Jobs    []Job     `json:"jobs"`
}

const nodeColumns = `n.id,n.name,n.status,n.last_seen,n.capacity,(SELECT count(*) FROM download_fleet_attempts a WHERE a.worker_id=n.id AND a.state IN ('running','uploading','staged')),n.browser_ok,n.disk_free_bytes,n.spool_bytes,n.last_error`

func scanNode(row pgx.Row) (wp.Node, error) {
	var n wp.Node
	e := row.Scan(&n.ID, &n.Name, &n.Status, &n.LastSeen, &n.Capacity, &n.Running, &n.BrowserOK, &n.DiskFreeBytes, &n.SpoolBytes, &n.LastError)
	return n, e
}
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	v := Snapshot{Workers: []wp.Node{}, Jobs: []Job{}}
	if !s.Enabled() {
		return v, ErrDisabled
	}
	rows, e := s.pool.Query(ctx, `SELECT `+nodeColumns+` FROM download_fleet_nodes n ORDER BY n.created_at DESC LIMIT 500`)
	if e != nil {
		return v, e
	}
	for rows.Next() {
		n, e := scanNode(rows)
		if e != nil {
			rows.Close()
			return v, e
		}
		v.Workers = append(v.Workers, n)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return v, e
	}
	rows, e = s.pool.Query(ctx, `SELECT t.id,coalesce(a.worker_id,''),t.state,t.identity,t.error,t.updated_at FROM download_fleet_tasks t LEFT JOIN download_fleet_attempts a ON a.id=t.current_attempt ORDER BY t.updated_at DESC LIMIT 500`)
	if e != nil {
		return v, e
	}
	defer rows.Close()
	for rows.Next() {
		var j Job
		if e = rows.Scan(&j.ID, &j.WorkerID, &j.State, &j.Identifier, &j.Error, &j.UpdatedAt); e != nil {
			return v, e
		}
		v.Jobs = append(v.Jobs, j)
	}
	return v, rows.Err()
}

// All scheduler mutations take this transaction-scoped lock, shared across API replicas.
func schedulerLock(ctx context.Context, tx pgx.Tx) error {
	_, e := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(718924610)`)
	return e
}
func (s *Service) Action(ctx context.Context, id, action string) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	target := map[string]string{"approve": "approved", "enable": "approved", "reject": "rejected", "drain": "draining", "revoke": "revoked"}[action]
	if target == "" {
		return errors.New("unknown worker action")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = schedulerLock(ctx, tx); e != nil {
		return e
	}
	var status string
	e = tx.QueryRow(ctx, `SELECT status FROM download_fleet_nodes WHERE id=$1 FOR UPDATE`, id).Scan(&status)
	if errors.Is(e, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if e != nil {
		return e
	}
	if status == "revoked" || status == "rejected" {
		return errors.New("rejected/revoked identity cannot be re-enabled; enroll a new identity")
	}
	if action == "approve" && status != "pending" {
		return errors.New("only pending workers can be approved")
	}
	if action == "enable" && status != "draining" {
		return errors.New("only draining workers can be enabled")
	}
	if action == "drain" && status != "approved" {
		return errors.New("only approved workers can drain")
	}
	if _, e = tx.Exec(ctx, `UPDATE download_fleet_nodes SET status=$2 WHERE id=$1`, id, target); e != nil {
		return e
	}
	if target == "revoked" || target == "rejected" {
		// Fence in-progress work. Already staged bytes are trusted server-side and finish recovery.
		_, e = tx.Exec(ctx, `UPDATE download_fleet_attempts SET lease_expires=clock_timestamp() WHERE worker_id=$1 AND state IN ('running','uploading')`, id)
		if e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}
