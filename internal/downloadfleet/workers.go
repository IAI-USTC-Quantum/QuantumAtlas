package downloadfleet

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"regexp"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/jackc/pgx/v5"
)

var validID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (s *Service) register(ctx context.Context, r wp.RegisterRequest) (wp.Node, error) {
	if !validID.MatchString(r.ID) || len(r.Secret) < 32 || len(r.Secret) > 256 || len(r.EnrollmentToken) > 256 || len(r.Name) > 200 {
		return wp.Node{}, errors.New("invalid registration fields")
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return wp.Node{}, e
	}
	defer tx.Rollback(ctx)
	if e = schedulerLock(ctx, tx); e != nil {
		return wp.Node{}, e
	}
	var stored []byte
	e = tx.QueryRow(ctx, `SELECT secret_hash FROM download_fleet_nodes WHERE id=$1`, r.ID).Scan(&stored)
	if e == nil {
		if subtle.ConstantTimeCompare(stored, hash(r.Secret)) != 1 {
			return wp.Node{}, ErrUnauthorized
		}
		n, e := scanNode(tx.QueryRow(ctx, `SELECT `+nodeColumns+` FROM download_fleet_nodes n WHERE id=$1`, r.ID))
		if e != nil {
			return n, e
		}
		if n.Status == "revoked" || n.Status == "rejected" {
			return wp.Node{}, ErrForbidden
		}
		return n, tx.Commit(ctx)
	}
	if !errors.Is(e, pgx.ErrNoRows) {
		return wp.Node{}, e
	}
	var expires time.Time
	var used *string
	e = tx.QueryRow(ctx, `SELECT expires_at,used_by FROM download_fleet_enrollments WHERE token_hash=$1 FOR UPDATE`, hash(r.EnrollmentToken)).Scan(&expires, &used)
	if errors.Is(e, pgx.ErrNoRows) {
		return wp.Node{}, ErrUnauthorized
	}
	if e != nil {
		return wp.Node{}, e
	}
	if used != nil || !expires.After(time.Now()) {
		return wp.Node{}, ErrUnauthorized
	}
	_, e = tx.Exec(ctx, `INSERT INTO download_fleet_nodes(id,name,secret_hash,last_seen) VALUES($1,$2,$3,clock_timestamp())`, r.ID, r.Name, hash(r.Secret))
	if e != nil {
		return wp.Node{}, e
	}
	_, e = tx.Exec(ctx, `UPDATE download_fleet_enrollments SET used_by=$2 WHERE token_hash=$1`, hash(r.EnrollmentToken), r.ID)
	if e != nil {
		return wp.Node{}, e
	}
	n, e := scanNode(tx.QueryRow(ctx, `SELECT `+nodeColumns+` FROM download_fleet_nodes n WHERE id=$1`, r.ID))
	if e != nil {
		return n, e
	}
	return n, tx.Commit(ctx)
}
func (s *Service) authenticate(ctx context.Context, secret string) (wp.Node, error) {
	if len(secret) < 32 || len(secret) > 256 {
		return wp.Node{}, ErrUnauthorized
	}
	n, e := scanNode(s.pool.QueryRow(ctx, `SELECT `+nodeColumns+` FROM download_fleet_nodes n WHERE secret_hash=$1`, hash(secret)))
	if errors.Is(e, pgx.ErrNoRows) {
		return n, ErrUnauthorized
	}
	if e != nil {
		return n, e
	}
	if n.Status == "rejected" || n.Status == "revoked" {
		return n, ErrForbidden
	}
	return n, nil
}
func (s *Service) heartbeat(ctx context.Context, n wp.Node, r wp.HeartbeatRequest) (wp.HeartbeatResponse, error) {
	out := wp.HeartbeatResponse{LeaseExpires: map[string]time.Time{}}
	if r.Capacity < 0 || r.DiskFreeBytes < 0 || r.SpoolBytes < 0 || len(r.RunningAttemptIDs) > 128 {
		return out, errors.New("invalid heartbeat")
	}
	if r.Capacity > s.cfg.MaxWorkerInFlight {
		r.Capacity = s.cfg.MaxWorkerInFlight
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	e = tx.QueryRow(ctx, `UPDATE download_fleet_nodes SET last_seen=clock_timestamp(),capacity=$2,browser_ok=$3,disk_free_bytes=$4,spool_bytes=$5,last_error=$6 WHERE id=$1 AND status IN ('approved','draining') RETURNING status`, n.ID, r.Capacity, r.BrowserOK, r.DiskFreeBytes, r.SpoolBytes, truncate(r.LastError, 2048)).Scan(&out.Status)
	if errors.Is(e, pgx.ErrNoRows) {
		return out, ErrForbidden
	}
	if e != nil {
		return out, e
	}
	rows, e := tx.Query(ctx, `UPDATE download_fleet_attempts a SET lease_expires=least(clock_timestamp()+$3::interval,a.deadline,t.deadline),updated_at=clock_timestamp() FROM download_fleet_tasks t WHERE a.task_id=t.id AND t.current_attempt=a.id AND a.worker_id=$1 AND a.id=ANY($2::text[]) AND a.state='running' AND a.lease_expires>clock_timestamp() AND a.deadline>clock_timestamp() AND t.deadline>clock_timestamp() RETURNING a.id,a.lease_expires`, n.ID, r.RunningAttemptIDs, interval(s.cfg.LeaseDuration))
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var id string
		var expiry time.Time
		if e = rows.Scan(&id, &expiry); e != nil {
			rows.Close()
			return out, e
		}
		out.LeaseExpires[id] = expiry
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	// Uploading rows are deliberately not UPDATE-locked: the active transfer
	// owns them. Its fixed upload-phase lease is readable and cannot be renewed
	// forever, and a slow upload cannot starve another attempt's heartbeat.
	rows, e = tx.Query(ctx, `SELECT a.id,a.lease_expires FROM download_fleet_attempts a JOIN download_fleet_tasks t ON t.id=a.task_id WHERE a.worker_id=$1 AND a.id=ANY($2::text[]) AND a.state='uploading' AND t.current_attempt=a.id AND a.lease_expires>clock_timestamp() AND t.deadline>clock_timestamp()`, n.ID, r.RunningAttemptIDs)
	if e != nil {
		return out, e
	}
	for rows.Next() {
		var id string
		var expires time.Time
		if e = rows.Scan(&id, &expires); e != nil {
			rows.Close()
			return out, e
		}
		out.LeaseExpires[id] = expires
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return out, e
	}
	return out, tx.Commit(ctx)
}
func (s *Service) claim(ctx context.Context, n wp.Node, req wp.ClaimRequest) (wp.ClaimResponse, error) {
	out := wp.ClaimResponse{Attempts: []wp.Assignment{}}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return out, e
	}
	defer tx.Rollback(ctx)
	if e = schedulerLock(ctx, tx); e != nil {
		return out, e
	}
	var status string
	var capacity int
	var seen *time.Time
	if e = tx.QueryRow(ctx, `SELECT status,capacity,last_seen FROM download_fleet_nodes WHERE id=$1 FOR UPDATE`, n.ID).Scan(&status, &capacity, &seen); e != nil {
		return out, e
	}
	if status == "draining" {
		return out, nil
	}
	if status != "approved" {
		return out, ErrForbidden
	}
	if seen == nil || time.Since(*seen) > s.cfg.LeaseDuration {
		return out, nil
	}
	var global, local int
	if e = tx.QueryRow(ctx, `SELECT count(*),count(*) FILTER(WHERE worker_id=$1) FROM download_fleet_attempts WHERE state IN ('running','uploading','staged')`, n.ID).Scan(&global, &local); e != nil {
		return out, e
	}
	limit := req.Limit
	if limit <= 0 {
		limit = 1
	}
	limit = min(limit, s.cfg.MaxInFlight-global, s.cfg.MaxWorkerInFlight-local, capacity-local)
	for i := 0; i < limit; i++ {
		var task string
		var raw []byte
		var deadline time.Time
		e = tx.QueryRow(ctx, `SELECT t.id,t.ref,least(t.deadline,clock_timestamp()+$3::interval) FROM download_fleet_tasks t WHERE t.state='queued' AND t.deadline>clock_timestamp() AND t.attempt_count<$2 AND NOT EXISTS(SELECT 1 FROM download_fleet_attempts a WHERE a.task_id=t.id AND a.worker_id=$1) ORDER BY t.created_at FOR UPDATE SKIP LOCKED LIMIT 1`, n.ID, s.cfg.MaxWorkerAttempts, interval(s.cfg.WorkerTimeout)).Scan(&task, &raw, &deadline)
		if errors.Is(e, pgx.ErrNoRows) {
			break
		}
		if e != nil {
			return out, e
		}
		var ref registry.PaperRef
		if e = json.Unmarshal(raw, &ref); e != nil {
			return out, e
		}
		a := wp.Assignment{TaskID: task, AttemptID: randomID(), Ref: wireRef(ref), Deadline: deadline, MaxPDFBytes: s.cfg.MaxPDFBytes}
		e = tx.QueryRow(ctx, `INSERT INTO download_fleet_attempts(id,task_id,worker_id,state,lease_expires,deadline) VALUES($1,$2,$3,'running',least(clock_timestamp()+$4::interval,$5),$5) RETURNING lease_expires`, a.AttemptID, task, n.ID, interval(s.cfg.LeaseDuration), deadline).Scan(&a.LeaseExpires)
		if e != nil {
			return out, e
		}
		if _, e = tx.Exec(ctx, `UPDATE download_fleet_tasks SET state='running',current_attempt=$2,attempt_count=attempt_count+1,updated_at=clock_timestamp() WHERE id=$1`, task, a.AttemptID); e != nil {
			return out, e
		}
		out.Attempts = append(out.Attempts, a)
	}
	return out, tx.Commit(ctx)
}
func validFailure(s string) bool {
	switch s {
	case wp.FailureNetwork, wp.FailureTimeout, wp.FailureNotFound, wp.FailurePaywall, wp.FailureChallenge, wp.FailureInvalidPDF, wp.FailureDisk, wp.FailureInternal, wp.FailureInvalidIdentifier, wp.FailureCancelled:
		return true
	}
	return false
}
func (s *Service) report(ctx context.Context, n wp.Node, r wp.ReportRequest) (wp.Receipt, error) {
	if !validFailure(r.Failure) || len(r.Trace) > 64 {
		return wp.Receipt{}, errors.New("invalid failure report")
	}
	for i := range r.Trace {
		r.Trace[i].Error = truncate(r.Trace[i].Error, 2048)
		r.Trace[i].URL = truncate(r.Trace[i].URL, 2048)
		r.Trace[i].Strategy = truncate(r.Trace[i].Strategy, 128)
	}
	trace, e := json.Marshal(r.Trace)
	if e != nil {
		return wp.Receipt{}, e
	}
	tx, e := s.pool.Begin(ctx)
	if e != nil {
		return wp.Receipt{}, e
	}
	defer tx.Rollback(ctx)
	// Same ordering as upload/maintenance: task first, attempt second.
	var task string
	e = tx.QueryRow(ctx, `SELECT t.id FROM download_fleet_tasks t JOIN download_fleet_attempts a ON a.task_id=t.id WHERE a.id=$1 AND a.worker_id=$2 FOR UPDATE OF t`, r.AttemptID, n.ID).Scan(&task)
	if errors.Is(e, pgx.ErrNoRows) {
		return wp.Receipt{}, ErrNotFound
	}
	if e != nil {
		return wp.Receipt{}, e
	}
	receipt, e := scanReceipt(tx.QueryRow(ctx, `SELECT task_id,id,state,sha256,size,error FROM download_fleet_attempts WHERE id=$1 FOR UPDATE`, r.AttemptID))
	if e != nil {
		return receipt, e
	}
	if receipt.State == "failed" || receipt.State == "expired" || receipt.State == "done" {
		return receipt, tx.Commit(ctx)
	}
	if receipt.State != "running" {
		return receipt, ErrConflict
	}
	tag, e := tx.Exec(ctx, `UPDATE download_fleet_attempts a SET state='failed',failure=$3,error=$4,trace=$5,updated_at=clock_timestamp() FROM download_fleet_tasks t, download_fleet_nodes n WHERE a.id=$1 AND a.worker_id=$2 AND t.id=a.task_id AND t.current_attempt=a.id AND a.lease_expires>clock_timestamp() AND (a.state='uploading' OR a.deadline>clock_timestamp()) AND t.deadline>clock_timestamp() AND n.id=a.worker_id AND n.status IN ('approved','draining')`, r.AttemptID, n.ID, r.Failure, truncate(r.Error, 2048), trace)
	if e != nil {
		return receipt, e
	}
	if tag.RowsAffected() != 1 {
		return receipt, ErrConflict
	}
	terminal := r.Failure == wp.FailureInvalidIdentifier || r.Failure == wp.FailureCancelled
	_, e = tx.Exec(ctx, `UPDATE download_fleet_tasks SET state=CASE WHEN $2 OR attempt_count >= $3 OR deadline<=clock_timestamp() THEN 'failed' ELSE 'queued' END,error=$4,updated_at=clock_timestamp() WHERE id=$1`, task, terminal, s.cfg.MaxWorkerAttempts, truncate(r.Error, 2048))
	if e != nil {
		return receipt, e
	}
	receipt.State = "failed"
	receipt.Error = truncate(r.Error, 2048)
	return receipt, tx.Commit(ctx)
}
func scanReceipt(row pgx.Row) (wp.Receipt, error) {
	var r wp.Receipt
	e := row.Scan(&r.TaskID, &r.AttemptID, &r.State, &r.SHA256, &r.Size, &r.Error)
	if r.State == "uploading" {
		r.State = "running"
	}
	return r, e
}
func (s *Service) receipt(ctx context.Context, worker, id string) (wp.Receipt, error) {
	r, e := scanReceipt(s.pool.QueryRow(ctx, `SELECT task_id,id,state,sha256,size,error FROM download_fleet_attempts WHERE id=$1 AND worker_id=$2`, id, worker))
	if errors.Is(e, pgx.ErrNoRows) {
		e = ErrNotFound
	}
	return r, e
}
