package downloadfleet

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/jackc/pgx/v5"
)

func resultIdentity(ref registry.PaperRef, m wp.ResultMetadata) (doi, canonical string, version int, err error) {
	if m.ArxivVersion < 0 || m.ArxivVersion > 100000 {
		return "", "", 0, errors.New("invalid arxiv version")
	}
	if ref.ArxivID != "" {
		original, e := paperassets.Parse(ref.ArxivID)
		if e != nil {
			return "", "", 0, e
		}
		candidate := m.ArxivCanonical
		if candidate == "" {
			candidate = original.Canonical
		}
		p, e := paperassets.Parse(candidate)
		if e != nil {
			return "", "", 0, e
		}
		if p.StemBase != original.StemBase || (original.Category != "" && p.Category != original.Category) || (original.Version != "" && p.Version != original.Version) || p.IsBare {
			return "", "", 0, errors.New("worker arxiv identity mismatch")
		}
		v := m.ArxivVersion
		if p.Version != "" {
			parsed, parseErr := strconv.Atoi(strings.TrimPrefix(p.Version, "v"))
			if parseErr != nil || parsed <= 0 || parsed > 100000 {
				return "", "", 0, errors.New("invalid arxiv version")
			}
			if v != 0 && v != parsed {
				return "", "", 0, errors.New("arxiv version mismatch")
			}
			v = parsed
		}
		if v <= 0 {
			v = 1
		}
		if p.Version == "" {
			candidate = p.Canonical + "v" + strconv.Itoa(v)
		}
		return "", candidate, v, nil
	}
	key, e := identity(ref)
	if e != nil {
		return "", "", 0, e
	}
	if m.DOI != "" {
		other, e := identity(registry.PaperRef{DOI: m.DOI})
		if e != nil || key != other {
			return "", "", 0, errors.New("worker DOI identity mismatch")
		}
	}
	if m.ArxivCanonical != "" {
		return "", "", 0, errors.New("arxiv identity cannot replace DOI task")
	}
	return strings.TrimPrefix(key, "doi:"), "", 0, nil
}

// validatePDF independently rejects HTML, truncated and undersized payloads.
func validatePDF(f *os.File, size int64) error {
	if size < downloader.DefaultMinPDFBytes {
		return errors.New("PDF below minimum size")
	}
	var head [5]byte
	if _, e := f.ReadAt(head[:], 0); e != nil {
		return e
	}
	if string(head[:]) != "%PDF-" {
		return errors.New("invalid PDF signature")
	}
	tail := make([]byte, min(size, 4096))
	if _, e := f.ReadAt(tail, size-int64(len(tail))); e != nil {
		return e
	}
	if !bytes.Contains(tail, []byte("%%EOF")) {
		return errors.New("PDF missing EOF trailer")
	}
	return nil
}
func (s *Service) upload(ctx context.Context, n wp.Node, id, sha string, size int64, source, strategy string, meta wp.ResultMetadata, body io.Reader) (wp.Receipt, error) {
	if len(meta.Trace) > 64 {
		return wp.Receipt{}, errors.New("too many trace entries")
	}
	if meta.SourceURL != "" {
		source = meta.SourceURL
	}
	if meta.Strategy != "" {
		strategy = meta.Strategy
	}
	metadata, e := json.Marshal(meta)
	if e != nil {
		return wp.Receipt{}, e
	}
	sha = strings.ToLower(sha)
	b, e := hex.DecodeString(sha)
	if e != nil || len(b) != 32 || size < downloader.DefaultMinPDFBytes || size > s.cfg.MaxPDFBytes {
		return wp.Receipt{}, errors.New("invalid SHA256 or PDF size")
	}
	if !validID.MatchString(id) {
		return wp.Receipt{}, ErrNotFound
	}
	// Reserve spool space durably before accepting the transfer. Separate quota
	// lock prevents two uploads from both observing the same free capacity.
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return wp.Receipt{}, e
	}
	defer tx.Rollback(ctx)
	var task string
	var refraw []byte
	e = tx.QueryRow(ctx, `SELECT t.id,t.ref FROM download_fleet_tasks t JOIN download_fleet_attempts a ON a.task_id=t.id WHERE a.id=$1 AND a.worker_id=$2 FOR UPDATE OF t`, id, n.ID).Scan(&task, &refraw)
	if errors.Is(e, pgx.ErrNoRows) {
		return wp.Receipt{}, ErrNotFound
	}
	if e != nil {
		return wp.Receipt{}, e
	}
	var ref registry.PaperRef
	if e = json.Unmarshal(refraw, &ref); e != nil {
		return wp.Receipt{}, e
	}
	if _, _, _, e = resultIdentity(ref, meta); e != nil {
		return wp.Receipt{}, e
	}
	var state, oldsha, oldpath string
	var oldsize int64
	e = tx.QueryRow(ctx, `SELECT state,sha256,size,spool_path FROM download_fleet_attempts WHERE id=$1 FOR UPDATE`, id).Scan(&state, &oldsha, &oldsize, &oldpath)
	if e != nil {
		return wp.Receipt{}, e
	}
	if state == "done" || state == "staged" {
		if oldsha != sha || oldsize != size {
			return wp.Receipt{}, ErrConflict
		}
		r, e := scanReceipt(tx.QueryRow(ctx, `SELECT task_id,id,state,sha256,size,error FROM download_fleet_attempts WHERE id=$1`, id))
		if e != nil {
			return r, e
		}
		return r, tx.Commit(ctx)
	}
	if state != "running" && state != "uploading" {
		return wp.Receipt{}, ErrConflict
	}
	// A locked uploading row has no active transfer: safely replace its abandoned
	// generation. Retries keep the FIRST upload deadline, not an extendable TTL.
	if state == "uploading" && (oldsha != sha || oldsize != size) {
		return wp.Receipt{}, ErrConflict
	}
	if oldpath != "" {
		if filepath.Dir(oldpath) != s.cfg.SpoolDir {
			return wp.Receipt{}, errors.New("invalid prior spool path")
		}
		if e = os.Remove(oldpath); e != nil && !errors.Is(e, os.ErrNotExist) {
			return wp.Receipt{}, e
		}
	}
	if _, e = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(718924611)`); e != nil {
		return wp.Receipt{}, e
	}
	var reserved int64
	if e = tx.QueryRow(ctx, `SELECT coalesce(sum(size),0) FROM download_fleet_attempts WHERE spool_path<>''`).Scan(&reserved); e != nil {
		return wp.Receipt{}, e
	}
	if oldpath != "" {
		reserved -= oldsize
	}
	if size > s.cfg.SpoolMaxBytes-reserved {
		return wp.Receipt{}, errors.New("server spool capacity exhausted")
	}
	generation := randomID()
	path := filepath.Join(s.cfg.SpoolDir, id+"-"+generation+".pdf")
	tag, e := tx.Exec(ctx, `UPDATE download_fleet_attempts a SET state='uploading',sha256=$3,size=$4,spool_path=$5,source_url=$6,strategy=$7,metadata=$8,upload_id=$9,lease_expires=CASE WHEN a.state='running' THEN least(clock_timestamp()+$10::interval,t.deadline) ELSE a.lease_expires END,updated_at=clock_timestamp() FROM download_fleet_tasks t,download_fleet_nodes n WHERE a.id=$1 AND a.worker_id=$2 AND t.id=a.task_id AND t.current_attempt=a.id AND t.state='running' AND a.lease_expires>clock_timestamp() AND (a.state='uploading' OR a.deadline>clock_timestamp()) AND t.deadline>clock_timestamp() AND n.id=a.worker_id AND n.status IN ('approved','draining')`, id, n.ID, sha, size, path, truncate(source, 2048), truncate(strategy, 128), metadata, generation, interval(s.cfg.UploadTimeout))
	if e != nil {
		return wp.Receipt{}, e
	}
	if tag.RowsAffected() != 1 {
		return wp.Receipt{}, ErrConflict
	}
	if e = tx.Commit(ctx); e != nil {
		return wp.Receipt{}, e
	}
	if e = s.streamStage(ctx, task, id, generation, path, sha, size, body); e != nil {
		// Reset after streamStage has released all locks; cancellation must not
		// prevent reservation cleanup. Generation fencing protects a newer retry.
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		resetErr := s.resetTransfer(cleanup, task, id, generation, path)
		cancel()
		if resetErr != nil {
			slog.Warn("download fleet transfer cleanup", "attempt", id, "error", resetErr)
		}
		return wp.Receipt{}, e
	}
	if e = s.commitStaged(ctx, id); e != nil {
		return wp.Receipt{TaskID: task, AttemptID: id, State: "staged", SHA256: sha, Size: size}, nil
	}
	return s.receipt(ctx, n.ID, id)
}
func (s *Service) commitStaged(ctx context.Context, id string) error {
	archive, hook := s.callbacks()
	if archive == nil {
		return errors.New("archive callback not configured")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var task string
	var refraw []byte
	e = tx.QueryRow(ctx, `SELECT t.id,t.ref FROM download_fleet_tasks t JOIN download_fleet_attempts a ON a.task_id=t.id WHERE a.id=$1 AND t.current_attempt=a.id AND t.state='staged' FOR UPDATE OF t SKIP LOCKED`, id).Scan(&task, &refraw)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var path, sha, source, strategy, worker string
	var size int64
	var metadata []byte
	e = tx.QueryRow(ctx, `SELECT spool_path,sha256,size,source_url,strategy,worker_id,metadata FROM download_fleet_attempts WHERE id=$1 AND state='staged' FOR UPDATE`, id).Scan(&path, &sha, &size, &source, &strategy, &worker, &metadata)
	if e != nil {
		return e
	}
	if filepath.Dir(path) != s.cfg.SpoolDir {
		return errors.New("staged file outside configured spool")
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	digest := sha256.New()
	readSize, e := io.Copy(digest, io.LimitReader(f, s.cfg.MaxPDFBytes+1))
	if e != nil {
		return e
	}
	if readSize != size || hex.EncodeToString(digest.Sum(nil)) != sha {
		return errors.New("staged PDF integrity mismatch")
	}
	if e = validatePDF(f, size); e != nil {
		return e
	}
	if _, e = f.Seek(0, io.SeekStart); e != nil {
		return e
	}
	var ref registry.PaperRef
	if e = json.Unmarshal(refraw, &ref); e != nil {
		return e
	}
	out := &downloader.FetchOutcome{Strategy: "worker:" + worker + ":" + strategy, URL: source, DOI: ref.DOI, Result: &downloader.FetchResult{Body: f, Size: size, Sha256: sha, URL: source, Attempts: 1}}
	out.WorkerID = worker
	out.RemoteTaskID = task
	var meta wp.ResultMetadata
	if e = json.Unmarshal(metadata, &meta); e != nil {
		return e
	}
	out.DOI, out.ArxivCanonical, out.ArxivVersion, e = resultIdentity(ref, meta)
	if e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT worker_id,failure,error,trace FROM download_fleet_attempts WHERE task_id=$1 ORDER BY created_at`, task)
	if e != nil {
		return e
	}
	for rows.Next() {
		var wid, failure, msg string
		var trace []byte
		if e = rows.Scan(&wid, &failure, &msg, &trace); e != nil {
			rows.Close()
			return e
		}
		var items []wp.Trace
		_ = json.Unmarshal(trace, &items)
		traceError := msg
		if failure != "" {
			traceError = failure + ": " + msg
		}
		if wid == worker {
			traceError = ""
		} // recovery errors are not worker acquisition failures
		out.Trace = append(out.Trace, downloader.Attempt{Strategy: "worker:" + wid, Error: truncate(traceError, 2048)})
		for _, t := range items {
			out.Trace = append(out.Trace, downloader.Attempt{Strategy: "worker:" + wid + ":" + t.Strategy, URL: t.URL, Error: t.Error, Millis: t.Millis})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, t := range meta.Trace {
		out.Trace = append(out.Trace, downloader.Attempt{Strategy: "worker:" + worker + ":" + truncate(t.Strategy, 128), URL: truncate(t.URL, 2048), Error: truncate(t.Error, 2048), Millis: t.Millis})
	}
	callbackCtx, e := s.callbackContext(ctx, tx, task)
	if e != nil {
		return e
	}
	if e = archive(callbackCtx, ref, out); e != nil {
		return fmt.Errorf("archive worker PDF: %w", e)
	}
	// Body is intentionally not serialized. Done means Archive has succeeded;
	// hooks are an independent durable at-least-once outbox.
	if out.Result != nil {
		out.Result.Body = nil
	}
	out.Archived = true
	raw, e := json.Marshal(out)
	if e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE download_fleet_attempts SET state='done',error='',updated_at=clock_timestamp() WHERE id=$1`, id); e != nil {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE download_fleet_tasks SET state='done',outcome=$2,error='',hook_pending=$3,updated_at=clock_timestamp() WHERE id=$1`, task, raw, hook != nil); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	return s.cleanFile(ctx, id, path)
}
func (s *Service) cleanFile(ctx context.Context, id, path string) error {
	if filepath.Dir(path) != s.cfg.SpoolDir {
		return errors.New("invalid spool path")
	}
	if e := os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	_, e := s.pool.Exec(ctx, `UPDATE download_fleet_attempts SET spool_path='',size=size WHERE id=$1 AND state IN ('done','failed','expired')`, id)
	return e
}

// Start runs maintenance until cancellation. Run once per service; multiple
// coordinator processes are safe with a shared spool directory. Callbacks must
// honor cancellation. Failures retry on later ticks; staged bytes are retained
// until recovery succeeds or the configured retention deadline expires.
func (s *Service) Start(ctx context.Context) {
	if !s.Enabled() {
		return
	}
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		c, cancel := context.WithTimeout(ctx, 2*time.Minute)
		if e := s.Maintain(c); e != nil && ctx.Err() == nil {
			slog.Warn("download fleet maintenance", "error", e)
		}
		cancel()
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
