package downloadfleet

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"

	"github.com/jackc/pgx/v5"
)

// streamStage holds row locks, but never the scheduler or quota advisory locks.
// Heartbeats SELECT the upload lease without attempting to update these rows.
func (s *Service) streamStage(ctx context.Context, task, id, generation, path, sha string, size int64, body io.Reader) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = tx.QueryRow(ctx, `SELECT id FROM download_fleet_tasks WHERE id=$1 FOR UPDATE`, task).Scan(&task); e != nil {
		return e
	}
	var state, current string
	if e = tx.QueryRow(ctx, `SELECT state,upload_id FROM download_fleet_attempts WHERE id=$1 FOR UPDATE`, id).Scan(&state, &current); e != nil {
		return e
	}
	if state != "uploading" || current != generation {
		return ErrConflict
	}
	f, e := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	staged := false
	defer func() {
		f.Close()
		if !staged {
			os.Remove(path)
		}
	}()
	h := sha256.New()
	written, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(body, size+1))
	if e != nil {
		return e
	}
	if written != size {
		return errors.New("PDF size mismatch")
	}
	if hex.EncodeToString(h.Sum(nil)) != sha {
		return errors.New("PDF SHA256 mismatch")
	}
	if e = validatePDF(f, size); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	dir, e := os.Open(s.cfg.SpoolDir)
	if e != nil {
		return e
	}
	e = dir.Sync()
	dir.Close()
	if e != nil {
		return e
	}
	tag, e := tx.Exec(ctx, `UPDATE download_fleet_attempts a SET state='staged',error='',updated_at=clock_timestamp() FROM download_fleet_tasks t,download_fleet_nodes n WHERE a.id=$1 AND a.upload_id=$2 AND t.id=a.task_id AND t.current_attempt=a.id AND a.lease_expires>clock_timestamp() AND t.deadline>clock_timestamp() AND n.id=a.worker_id AND n.status IN ('approved','draining')`, id, generation)
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return ErrConflict
	}
	if _, e = tx.Exec(ctx, `UPDATE download_fleet_tasks SET state='staged',updated_at=clock_timestamp() WHERE id=$1`, task); e != nil {
		return e
	}
	// An ambiguous commit must retain bytes; resetTransfer checks persisted state.
	staged = true
	return tx.Commit(ctx)
}

// resetTransfer releases disk reservation without discarding the bounded upload
// phase. The same worker can retry within its original transfer lease, including
// after coordinator restart. A later generation can never be reset by this one.
func (s *Service) resetTransfer(ctx context.Context, task, id, generation, path string) error {
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	if e = tx.QueryRow(ctx, `SELECT id FROM download_fleet_tasks WHERE id=$1 FOR UPDATE`, task).Scan(&task); errors.Is(e, pgx.ErrNoRows) {
		return nil
	} else if e != nil {
		return e
	}
	var current string
	e = tx.QueryRow(ctx, `SELECT upload_id FROM download_fleet_attempts WHERE id=$1 AND state='uploading' FOR UPDATE`, id).Scan(&current)
	if errors.Is(e, pgx.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	if current != generation {
		return nil
	}
	if e = os.Remove(path); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	if _, e = tx.Exec(ctx, `UPDATE download_fleet_attempts SET spool_path='',upload_id='' WHERE id=$1 AND upload_id=$2 AND state='uploading'`, id, generation); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
