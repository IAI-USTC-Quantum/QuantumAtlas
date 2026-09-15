package mineru

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// tokenstore.go: the PostgreSQL-backed MinerU API token pool.
//
// The config list (paper_access.mineru.api_tokens) is a bootstrap
// seed: at boot SyncFromConfig inserts any config token the table
// hasn't seen before (first-seen timestamp = boot time) and the table
// then becomes the authoritative pool the KeyRing is built from. The
// admin surface (GET/POST/DELETE /api/admin/mineru/tokens) mutates
// the table AND the live ring, so rotations take effect without a
// restart.
//
// Rows survive restarts and are shared by every edge pointing at the
// same registry database. Caveat: the live KeyRing is per-process, so
// a token added on one edge is only picked up by other edges at their
// next restart. A config token that an admin deleted comes back at
// the next boot for the same reason — remove retired tokens from the
// config file too.
type TokenStore struct {
	pool *pgxpool.Pool
}

// ErrTokenNotFound is returned when a delete targets a token the
// table doesn't know (already removed, or never added).
var ErrTokenNotFound = errors.New("mineru token not found")

// TokenRow is one persisted pool entry.
type TokenRow struct {
	Token     string
	RotatedAt time.Time
	CreatedAt time.Time
}

// NewTokenStore wraps a registry pool. Returns nil for a nil pool —
// callers treat that as "token management not available" and the
// converter falls back to the config-only list.
func NewTokenStore(pool *pgxpool.Pool) *TokenStore {
	if pool == nil {
		return nil
	}
	return &TokenStore{pool: pool}
}

// Configured reports whether the store has a pool to talk to.
func (s *TokenStore) Configured() bool { return s != nil && s.pool != nil }

// SyncFromConfig seeds tokens that the table hasn't seen before.
// Existing rows are left untouched: their rotated_at is admin-managed
// state and must not be clobbered by every boot. Empty / duplicate
// config entries are ignored.
func (s *TokenStore) SyncFromConfig(ctx context.Context, tokens []string) error {
	if !s.Configured() {
		return fmt.Errorf("mineru token store: not configured")
	}
	seen := map[string]struct{}{}
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		if _, dup := seen[tok]; dup {
			continue
		}
		seen[tok] = struct{}{}
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO mineru_tokens (token) VALUES ($1) ON CONFLICT (token) DO NOTHING`,
			tok,
		); err != nil {
			return fmt.Errorf("mineru token store: seed %s: %w", TokenID(tok), err)
		}
	}
	return nil
}

// List returns the whole pool, oldest rotation first (a stable order
// for the admin UI that also matches the boot ring construction).
func (s *TokenStore) List(ctx context.Context) ([]TokenRow, error) {
	if !s.Configured() {
		return nil, fmt.Errorf("mineru token store: not configured")
	}
	rows, err := s.pool.Query(ctx,
		`SELECT token, rotated_at, created_at FROM mineru_tokens ORDER BY rotated_at, token`,
	)
	if err != nil {
		return nil, fmt.Errorf("mineru token store: list: %w", err)
	}
	defer rows.Close()
	var out []TokenRow
	for rows.Next() {
		var r TokenRow
		if err := rows.Scan(&r.Token, &r.RotatedAt, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("mineru token store: scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mineru token store: list: %w", err)
	}
	return out, nil
}

// Upsert inserts the token or refreshes its rotated_at when it
// already exists (re-adding a token counts as rotating it).
func (s *TokenStore) Upsert(ctx context.Context, token string, rotatedAt time.Time) error {
	if !s.Configured() {
		return fmt.Errorf("mineru token store: not configured")
	}
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO mineru_tokens (token, rotated_at) VALUES ($1, $2)
		 ON CONFLICT (token) DO UPDATE SET rotated_at = EXCLUDED.rotated_at`,
		token, rotatedAt,
	); err != nil {
		return fmt.Errorf("mineru token store: upsert %s: %w", TokenID(token), err)
	}
	return nil
}

// Delete removes a token row. Returns ErrTokenNotFound when the table
// doesn't have it (the caller decides whether a ring-only removal is
// still acceptable).
func (s *TokenStore) Delete(ctx context.Context, token string) error {
	if !s.Configured() {
		return fmt.Errorf("mineru token store: not configured")
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM mineru_tokens WHERE token = $1`, token)
	if err != nil {
		return fmt.Errorf("mineru token store: delete %s: %w", TokenID(token), err)
	}
	if tag.RowsAffected() == 0 {
		return ErrTokenNotFound
	}
	return nil
}

// TokenID derives the stable, non-reversible identifier the admin API
// uses to address a token (DELETE /api/admin/mineru/tokens/{id}).
// Full tokens never leave the process; masked previews do.
func TokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])[:16]
}

// MaskToken renders a token safe for display: a short head + tail
// around an ellipsis, enough for an operator to tell pool entries
// apart without exposing a usable secret. Fully masked (still
// length-hinted) for short values.
func MaskToken(token string) string {
	const head, tail = 6, 4
	if len(token) <= head+tail+2 {
		if len(token) == 0 {
			return ""
		}
		return fmt.Sprintf("***(%d chars)", len(token))
	}
	return token[:head] + "…" + token[len(token)-tail:]
}

// TokenEntriesFromStrings is the config-only fallback shape: strings
// with no rotation timestamp.
func TokenEntriesFromStrings(tokens []string) []TokenEntry {
	entries := make([]TokenEntry, 0, len(tokens))
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		entries = append(entries, TokenEntry{Token: tok})
	}
	return entries
}

// BootSync seeds the config tokens into the store and returns the
// resulting full pool for KeyRing construction. One round-trip-sized
// helper so main.go stays declarative.
func (s *TokenStore) BootSync(ctx context.Context, cfgTokens []string) ([]TokenEntry, error) {
	if !s.Configured() {
		return nil, fmt.Errorf("mineru token store: not configured")
	}
	if err := s.SyncFromConfig(ctx, cfgTokens); err != nil {
		return nil, err
	}
	rows, err := s.List(ctx)
	if err != nil {
		return nil, err
	}
	entries := make([]TokenEntry, 0, len(rows))
	for _, r := range rows {
		entries = append(entries, TokenEntry{Token: r.Token, RotatedAt: r.RotatedAt})
	}
	return entries, nil
}
