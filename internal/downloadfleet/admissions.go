package downloadfleet

import (
	"context"
	"errors"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/jackc/pgx/v5"
)

// enqueue resolves a durable parent admission before identity coalescing. A
// replay is pinned to the original task even after done/failed, while a new
// admission may explicitly retry a terminal task with a new attempt budget.
func (s *Service) enqueue(ctx context.Context, key string, raw []byte) (id string, deadline time.Time, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", deadline, err
	}
	defer tx.Rollback(ctx)
	if err = schedulerLock(ctx, tx); err != nil {
		return "", deadline, err
	}
	requestID := downloader.AdmissionID(ctx)
	if requestID != "" {
		err = tx.QueryRow(ctx, `SELECT t.id,t.deadline FROM download_fleet_admissions a JOIN download_fleet_tasks t ON t.id=a.task_id WHERE a.request_id=$1`, requestID).Scan(&id, &deadline)
		if err == nil {
			return id, deadline, tx.Commit(ctx)
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return id, deadline, err
		}
	}
	id = randomID()
	err = tx.QueryRow(ctx, `INSERT INTO download_fleet_tasks(id,identity,ref,deadline) VALUES($1,$2,$3,clock_timestamp()+$4::interval)
 ON CONFLICT(identity) WHERE state IN ('queued','running','staged') DO UPDATE SET identity=excluded.identity RETURNING id,deadline`, id, key, raw, interval(s.cfg.TaskTimeout)).Scan(&id, &deadline)
	if err != nil {
		return id, deadline, err
	}
	if requestID != "" {
		if _, err = tx.Exec(ctx, `INSERT INTO download_fleet_admissions(request_id,task_id) VALUES($1,$2)`, requestID, id); err != nil {
			return id, deadline, err
		}
	}
	return id, deadline, tx.Commit(ctx)
}

// callbackContext carries the newest linked parent admission. Normal journal
// admission dedup has one link per active paper; newest is also correct when an
// explicit admission attaches to a task first created by a direct fetch.
func (s *Service) callbackContext(ctx context.Context, tx pgx.Tx, task string) (context.Context, error) {
	var requestID string
	err := tx.QueryRow(ctx, `SELECT request_id FROM download_fleet_admissions WHERE task_id=$1 ORDER BY created_at DESC,request_id DESC LIMIT 1`, task).Scan(&requestID)
	if errors.Is(err, pgx.ErrNoRows) {
		return ctx, nil
	}
	if err != nil {
		return ctx, err
	}
	return downloader.WithAdmissionID(ctx, requestID), nil
}
func remotePending(id string, cause error) (*downloader.FetchOutcome, error) {
	return &downloader.FetchOutcome{Pending: true, RemoteTaskID: id}, errors.Join(downloader.ErrRemotePending, cause)
}
