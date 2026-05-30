package shares

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// neoOpTimeout caps a single share Neo4j operation. Shares are off the
// hot path (created/listed by humans), so a generous timeout is fine.
const neoOpTimeout = 5 * time.Second

// neoSave upserts rec as a :PaperShareToken node. created_at/expires_at
// are stored as the original RFC3339 strings so Record round-trips
// byte-identically to the on-disk format (and Record.IsExpired keeps
// working unchanged).
func (s *Store) neoSave(rec *Record) error {
	ctx, cancel := context.WithTimeout(context.Background(), neoOpTimeout)
	defer cancel()
	_, err := s.neo.ExecuteWrite(ctx, `
		MERGE (s:PaperShareToken {token: $token})
		SET s.paths = $paths,
		    s.created_by = $created_by,
		    s.created_at = $created_at,
		    s.expires_at = $expires_at,
		    s.label = $label`,
		map[string]any{
			"token":      rec.Token,
			"paths":      toAnySlice(rec.Paths),
			"created_by": rec.CreatedBy,
			"created_at": rec.CreatedAt,
			"expires_at": rec.ExpiresAt,
			"label":      rec.Label,
		})
	if err != nil {
		return fmt.Errorf("shares: neo save %s: %w", rec.Token, err)
	}
	return nil
}

// neoGet loads a single share token node.
func (s *Store) neoGet(token string) (*Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), neoOpTimeout)
	defer cancel()
	rows, err := s.neo.ExecuteReadParams(ctx, `
		MATCH (s:PaperShareToken {token: $token})
		RETURN s.token AS token, s.paths AS paths,
		       s.created_by AS created_by, s.created_at AS created_at,
		       s.expires_at AS expires_at, s.label AS label`,
		map[string]any{"token": token})
	if err != nil {
		return nil, fmt.Errorf("shares: neo get %s: %w", token, err)
	}
	if len(rows) == 0 {
		return nil, nil
	}
	return recordFromRow(rows[0]), nil
}

// neoDelete removes a share token node. Returns (false,nil) when the
// token doesn't exist.
func (s *Store) neoDelete(token string) (bool, error) {
	ctx, cancel := context.WithTimeout(context.Background(), neoOpTimeout)
	defer cancel()
	rows, err := s.neo.ExecuteWrite(ctx, `
		MATCH (s:PaperShareToken {token: $token})
		WITH s, s.token AS t
		DELETE s
		RETURN t`,
		map[string]any{"token": token})
	if err != nil {
		return false, fmt.Errorf("shares: neo delete %s: %w", token, err)
	}
	return len(rows) > 0, nil
}

// neoListAll returns every share token node, newest first.
func (s *Store) neoListAll() ([]*Record, error) {
	ctx, cancel := context.WithTimeout(context.Background(), neoOpTimeout)
	defer cancel()
	rows, err := s.neo.ExecuteReadParams(ctx, `
		MATCH (s:PaperShareToken)
		RETURN s.token AS token, s.paths AS paths,
		       s.created_by AS created_by, s.created_at AS created_at,
		       s.expires_at AS expires_at, s.label AS label`, nil)
	if err != nil {
		return nil, fmt.Errorf("shares: neo list: %w", err)
	}
	out := make([]*Record, 0, len(rows))
	for _, r := range rows {
		out = append(out, recordFromRow(r))
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt > out[j].CreatedAt
	})
	return out, nil
}

// GCExpired deletes all expired share tokens (Neo4j backend only; the
// on-disk backend is GC'd lazily on access). No-op when Neo4j isn't the
// active backend. Returns the number deleted.
func (s *Store) GCExpired() (int, error) {
	if !s.useNeo() {
		return 0, nil
	}
	all, err := s.neoListAll()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, rec := range all {
		if rec.IsExpired() {
			if ok, _ := s.neoDelete(rec.Token); ok {
				n++
			}
		}
	}
	return n, nil
}

// recordFromRow reconstructs a Record from a Neo4j result row.
func recordFromRow(r map[string]any) *Record {
	return &Record{
		Token:     asStr(r["token"]),
		Paths:     toStringSlice(r["paths"]),
		CreatedBy: asStr(r["created_by"]),
		CreatedAt: asStr(r["created_at"]),
		ExpiresAt: asStr(r["expires_at"]),
		Label:     asStr(r["label"]),
	}
}

func asStr(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func toAnySlice(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

func toStringSlice(v any) []string {
	switch arr := v.(type) {
	case []any:
		out := make([]string, 0, len(arr))
		for _, e := range arr {
			if s, ok := e.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case []string:
		return arr
	default:
		return nil
	}
}
