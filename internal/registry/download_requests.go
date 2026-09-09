package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// DownloadRequest journals a local-first admission and its stable generation.
// Explicit retries after completion mint a new RequestID; restart recovery never
// does, allowing the fleet to reattach even a terminal remote attempt.
type DownloadRequest struct {
	RequestID string
	PaperID   string
	Input     string
	Kind      string
	Ref       PaperRef
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (s *Store) SaveDownloadRequest(ctx context.Context, paperID, input, kind string, ref PaperRef) (string, error) {
	if !s.ensure(ctx) {
		return "", ErrCatalogUnavailable
	}
	if len(paperID) > 128 || len(input) > 8192 || len(kind) > 32 {
		return "", fmt.Errorf("download request too large")
	}
	raw, err := json.Marshal(ref)
	if err != nil {
		return "", err
	}
	if len(raw) > 32768 {
		return "", fmt.Errorf("download reference too large")
	}
	var id string
	err = s.pool.QueryRow(ctx, `INSERT INTO downloader_requests AS t(paper_id,input,kind,ref) VALUES($1,$2,$3,$4)
 ON CONFLICT(paper_id) DO UPDATE SET
 request_id=CASE WHEN t.state='queued' THEN t.request_id ELSE excluded.request_id END,
 input=CASE WHEN t.state='queued' THEN t.input ELSE excluded.input END,
 kind=CASE WHEN t.state='queued' THEN t.kind ELSE excluded.kind END,
 ref=CASE WHEN t.state='queued' THEN t.ref ELSE excluded.ref END,
 created_at=CASE WHEN t.state='queued' THEN t.created_at ELSE excluded.created_at END,
 updated_at=CASE WHEN t.state='queued' THEN t.updated_at ELSE excluded.updated_at END,
 state='queued',error='' RETURNING request_id`, paperID, input, kind, raw).Scan(&id)
	return id, err
}
func (s *Store) FinishDownloadRequest(ctx context.Context, paperID, requestID, state, detail string) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	if state != "done" && state != "failed" {
		return fmt.Errorf("invalid terminal download state")
	}
	if requestID == "" {
		return nil
	} // a legacy/direct fetch must not finish another admission
	if len(detail) > 2048 {
		detail = strings.ToValidUTF8(detail[:2048], "")
	}
	_, err := s.pool.Exec(ctx, `UPDATE downloader_requests SET state=$3,error=$4,updated_at=now() WHERE paper_id=$1 AND request_id=$2`, paperID, requestID, state, detail)
	return err
}
func (s *Store) PendingDownloadRequests(ctx context.Context, limit int) ([]DownloadRequest, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	if limit < 1 || limit > 512 {
		limit = 128
	}
	rows, err := s.pool.Query(ctx, `SELECT request_id,paper_id,input,kind,ref,created_at,updated_at FROM downloader_requests WHERE state='queued' ORDER BY created_at LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DownloadRequest{}
	for rows.Next() {
		var r DownloadRequest
		var raw []byte
		if err := rows.Scan(&r.RequestID, &r.PaperID, &r.Input, &r.Kind, &raw, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(raw, &r.Ref); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
func (s *Store) PruneDownloadRequests(ctx context.Context, retention time.Duration) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	if retention < 24*time.Hour {
		retention = 7 * 24 * time.Hour
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM downloader_requests WHERE paper_id IN (SELECT paper_id FROM downloader_requests WHERE state<>'queued' AND updated_at<$1 ORDER BY updated_at LIMIT 1000)`, time.Now().Add(-retention))
	return err
}
