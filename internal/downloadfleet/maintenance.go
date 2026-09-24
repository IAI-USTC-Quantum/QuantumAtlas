package downloadfleet

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/jackc/pgx/v5"
)

// Maintain performs bounded recovery, lease expiry, outbox delivery and retention.
// It is also exported for operations/tests; Start calls it periodically.
func (s *Service) Maintain(ctx context.Context) error {
	if !s.Enabled() {
		return ErrDisabled
	}
	if e := s.expire(ctx); e != nil {
		return e
	}
	rows, e := s.pool.Query(ctx, `SELECT id FROM download_fleet_attempts WHERE state='staged' ORDER BY updated_at LIMIT 20`)
	if e != nil {
		return e
	}
	var ids []string
	for rows.Next() {
		var id string
		if e = rows.Scan(&id); e != nil {
			rows.Close()
			return e
		}
		ids = append(ids, id)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var errs []error
	for _, id := range ids {
		if e = s.commitStaged(ctx, id); e != nil {
			errs = append(errs, e)
			// Rotate failing commits without extending task retention, and surface the
			// failure to administrators. Staged bytes remain available for recovery.
			_, recordErr := s.pool.Exec(ctx, `UPDATE download_fleet_tasks SET error=$2 WHERE current_attempt=$1 AND state='staged'`, id, truncate(e.Error(), 2048))
			if recordErr != nil {
				errs = append(errs, recordErr)
			}
			_, recordErr = s.pool.Exec(ctx, `UPDATE download_fleet_attempts SET updated_at=clock_timestamp(),error=$2 WHERE id=$1 AND state='staged'`, id, truncate(e.Error(), 2048))
			if recordErr != nil {
				errs = append(errs, recordErr)
			}
		}
	}
	if e = s.deliverHooks(ctx); e != nil {
		errs = append(errs, e)
	}
	rows, e = s.pool.Query(ctx, `SELECT id,spool_path FROM download_fleet_attempts WHERE state IN ('done','expired','failed') AND spool_path<>'' LIMIT 100`)
	if e != nil {
		return e
	}
	type file struct{ id, path string }
	var files []file
	for rows.Next() {
		var f file
		if e = rows.Scan(&f.id, &f.path); e != nil {
			rows.Close()
			return e
		}
		files = append(files, f)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, f := range files {
		if e = s.cleanFile(ctx, f.id, f.path); e != nil {
			errs = append(errs, e)
		}
	}
	_, e = s.pool.Exec(ctx, `DELETE FROM download_fleet_enrollments WHERE expires_at<clock_timestamp()-interval '1 day'`)
	if e != nil {
		errs = append(errs, e)
	}
	_, e = s.pool.Exec(ctx, `DELETE FROM download_fleet_tasks WHERE id IN (SELECT t.id FROM download_fleet_tasks t WHERE t.state IN ('done','failed') AND NOT t.hook_pending AND t.updated_at<clock_timestamp()-$1::interval AND NOT EXISTS(SELECT 1 FROM download_fleet_attempts a WHERE a.task_id=t.id AND a.spool_path<>'') ORDER BY t.updated_at LIMIT 1000)`, interval(s.cfg.Retention))
	if e != nil {
		errs = append(errs, e)
	}
	_, e = s.pool.Exec(ctx, `DELETE FROM download_fleet_nodes n WHERE n.status IN ('pending','rejected','revoked') AND n.created_at<clock_timestamp()-$1::interval AND NOT EXISTS(SELECT 1 FROM download_fleet_attempts a WHERE a.worker_id=n.id) AND NOT EXISTS(SELECT 1 FROM download_fleet_enrollments e WHERE e.used_by=n.id)`, interval(s.cfg.Retention))
	if e != nil {
		errs = append(errs, e)
	}
	// Remove only old orphan files. Every live transfer has a committed DB path
	// reservation BEFORE file creation, so this cannot race transfer/TTL cleanup.
	entries, e := os.ReadDir(s.cfg.SpoolDir)
	if e != nil {
		errs = append(errs, e)
	} else {
		count := 0
		for _, entry := range entries {
			if count >= 100 {
				break
			}
			if entry.IsDir() || filepath.Ext(entry.Name()) != ".pdf" {
				continue
			}
			info, e := entry.Info()
			if e != nil || time.Since(info.ModTime()) < s.cfg.Retention {
				continue
			}
			path := filepath.Join(s.cfg.SpoolDir, entry.Name())
			var exists bool
			if e = s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM download_fleet_attempts WHERE spool_path=$1)`, path).Scan(&exists); e != nil {
				errs = append(errs, e)
				break
			}
			if !exists {
				count++
				if e = os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
					errs = append(errs, e)
				}
			}
		}
	}
	return errors.Join(errs...)
}
func (s *Service) expire(ctx context.Context) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = schedulerLock(ctx, tx); e != nil {
		return e
	}
	rows, e := tx.Query(ctx, `SELECT t.id,t.current_attempt,t.state,t.error,t.deadline<=clock_timestamp(),t.attempt_count >= $1 FROM download_fleet_tasks t LEFT JOIN download_fleet_attempts a ON a.id=t.current_attempt WHERE (t.state='queued' AND (t.deadline<=clock_timestamp() OR t.attempt_count >= $1)) OR (t.state='running' AND (a.lease_expires<=clock_timestamp() OR (a.state='running' AND a.deadline<=clock_timestamp()) OR t.deadline<=clock_timestamp())) OR (t.state='staged' AND t.updated_at<clock_timestamp()-$2::interval) ORDER BY t.created_at FOR UPDATE OF t SKIP LOCKED LIMIT 100`, s.cfg.MaxWorkerAttempts, interval(s.cfg.Retention))
	if e != nil {
		return e
	}
	type expired struct {
		id                string
		attempt           *string
		state             string
		err               string
		deadlineExpired   bool
		attemptsExhausted bool
	}
	var tasks []expired
	for rows.Next() {
		var t expired
		if e = rows.Scan(&t.id, &t.attempt, &t.state, &t.err, &t.deadlineExpired, &t.attemptsExhausted); e != nil {
			rows.Close()
			return e
		}
		tasks = append(tasks, t)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	for _, t := range tasks {
		msg := "worker lease/execution deadline expired"
		if t.state == "staged" {
			msg = "staged archive recovery exceeded retention"
		} else if t.state == "queued" {
			// A queued task has no executing worker. Its current_attempt may be
			// NULL, or may refer to a later lease expiry that hid a worker report.
			// Recover the latest nonempty worker failure without rewriting the
			// immutable report/receipt; fall back to the task's existing error.
			if e = tx.QueryRow(ctx, `SELECT coalesce((SELECT error FROM download_fleet_attempts WHERE task_id=$1 AND state='failed' AND error<>'' ORDER BY created_at DESC,id DESC LIMIT 1),$2::text)`, t.id, t.err).Scan(&msg); e != nil {
				return e
			}
			msg = queuedExpiryError(msg, t.deadlineExpired, t.attemptsExhausted)
		}
		if t.attempt != nil && t.state != "queued" {
			if _, e = tx.Exec(ctx, `UPDATE download_fleet_attempts SET state='expired',failure='timeout',error=$2,updated_at=clock_timestamp() WHERE id=$1 AND state IN ('running','uploading','staged')`, *t.attempt, msg); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `UPDATE download_fleet_tasks SET state=CASE WHEN deadline<=clock_timestamp() OR attempt_count >= $2 OR state='staged' THEN 'failed' ELSE 'queued' END,error=$3,updated_at=clock_timestamp() WHERE id=$1`, t.id, s.cfg.MaxWorkerAttempts, msg); e != nil {
			return e
		}
	}
	return tx.Commit(ctx)
}

// queuedExpiryError keeps the original bounded worker error intact and adds a
// separate task-level final reason, rather than inventing an execution timeout.
func queuedExpiryError(previous string, deadlineExpired, attemptsExhausted bool) string {
	var reasons []string
	if deadlineExpired {
		reasons = append(reasons, "queued timeout (task deadline expired while queued)")
	}
	if attemptsExhausted {
		reasons = append(reasons, "attempt limit reached while queued")
	}
	if len(reasons) == 0 {
		return previous
	}
	final := "fleet final reason: " + strings.Join(reasons, "; ")
	if previous == "" || previous == final {
		return final
	}
	suffix := "; " + final
	if strings.HasSuffix(previous, suffix) {
		return previous
	}
	// Reports are already capped at 2048 bytes. Reserve additional space for
	// the fixed final reason so neither it nor the original report is lost.
	return truncate(previous, 2048) + suffix
}

func (s *Service) deliverHooks(ctx context.Context) error {
	_, hook := s.callbacks()
	if hook == nil {
		return nil
	}
	for i := 0; i < 20; i++ {
		tx, e := s.pool.Begin(ctx)
		if e != nil {
			return e
		}
		var id string
		var refraw, outRaw []byte
		var tries int
		e = tx.QueryRow(ctx, `SELECT id,ref,outcome,hook_tries FROM download_fleet_tasks WHERE state='done' AND hook_pending AND hook_next_at<=clock_timestamp() ORDER BY hook_next_at FOR UPDATE SKIP LOCKED LIMIT 1`).Scan(&id, &refraw, &outRaw, &tries)
		if errors.Is(e, pgx.ErrNoRows) {
			tx.Rollback(ctx)
			return nil
		}
		if e != nil {
			tx.Rollback(ctx)
			return e
		}
		var ref registry.PaperRef
		var out downloader.FetchOutcome
		if e = json.Unmarshal(refraw, &ref); e == nil {
			e = json.Unmarshal(outRaw, &out)
		}
		if e == nil {
			var callbackCtx context.Context
			callbackCtx, e = s.callbackContext(ctx, tx, id)
			if e == nil {
				// Preserve maintenance transaction time to record timeout/backoff even
				// when the external hook consumes its own entire callback budget.
				hookCtx, cancel := context.WithTimeout(callbackCtx, 30*time.Second)
				e = hook(hookCtx, ref, &out)
				cancel()
			}
		}
		if e == nil {
			_, e = tx.Exec(ctx, `UPDATE download_fleet_tasks SET hook_pending=false WHERE id=$1`, id)
		} else {
			_, e = tx.Exec(ctx, `UPDATE download_fleet_tasks SET hook_tries=hook_tries+1,hook_pending=(hook_tries+1<20),hook_next_at=clock_timestamp()+$2::interval,error=$3 WHERE id=$1`, id, interval(time.Duration(1<<min(tries, 10))*time.Minute), truncate("post-archive hook: "+e.Error(), 2048))
		}
		if e != nil {
			tx.Rollback(ctx)
			return e
		}
		if e = tx.Commit(ctx); e != nil {
			return e
		}
	}
	return nil
}
