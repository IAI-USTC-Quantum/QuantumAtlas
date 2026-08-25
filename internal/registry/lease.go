package registry

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Lease TTL bounds (seconds).
const (
	DefaultTTLSeconds = 1800 // 30 minutes
	MinTTLSeconds     = 60
	MaxTTLSeconds     = 7200 // 2 hours
)

// LeaseGrant is the MinerU processing lease returned by Lease. It
// reserves a paper's default asset for conversion so contributors don't
// collide. PDFSha256 lets the contributor verify byte equality before
// running MinerU (re-checked server-side on upload).
type LeaseGrant struct {
	LeaseID    string
	PaperID    string
	AssetID    int64
	ArxivID    string // full versioned id; "" for DOI-only papers
	Source     string // 'arxiv' | 'published'
	PDFPath    string
	PDFSha256  string
	Holder     string
	CreatedAt  time.Time
	ExpiresAt  time.Time
	TTLSeconds int
}

// ErrAlreadyLeased is returned when an active lease (held by anyone)
// blocks a new lease. Carries the conflicting lease so the caller can
// surface holder + expiry.
type ErrAlreadyLeased struct {
	Existing *LeaseGrant
}

func (e *ErrAlreadyLeased) Error() string {
	return fmt.Sprintf("registry: paper %s is already leased", e.Existing.PaperID)
}

// ErrIDMismatch is returned by ReleaseLease when the caller's lease id
// doesn't match the active lease.
var ErrIDMismatch = errors.New("registry: lease_id does not match the active lease")

// ErrNotLeasable is returned when the paper can't be leased because it
// has no default PDF asset, that asset already has markdown, or the
// paper isn't in the registry at all.
var ErrNotLeasable = errors.New("registry: paper has no PDF asset or already has markdown")

// Lease atomically grants a MinerU lease on the paper's default asset
// inside one PostgreSQL transaction. SELECT ... FOR UPDATE serializes
// contenders for the same asset; the first live transaction to set
// lease_id wins. ttl is clamped to [MinTTLSeconds, MaxTTLSeconds], with
// 0 meaning DefaultTTLSeconds. Returns:
//
//	(LeaseGrant, nil)           lease granted
//	(nil, *ErrAlreadyLeased)    active lease held by someone
//	(nil, ErrNotLeasable)       no default PDF asset / already has MD / unknown paper
//	(nil, ErrCatalogUnavailable) PostgreSQL down
func (s *Store) Lease(ctx context.Context, paperID string, holder string, ttlSeconds int) (LeaseGrant, error) {
	if !s.ensure(ctx) {
		return LeaseGrant{}, ErrCatalogUnavailable
	}
	ttl := ttlSeconds
	if ttl == 0 {
		ttl = DefaultTTLSeconds
	}
	if ttl < MinTTLSeconds {
		ttl = MinTTLSeconds
	}
	if ttl > MaxTTLSeconds {
		ttl = MaxTTLSeconds
	}
	leaseID := newLeaseID()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LeaseGrant{}, catalogUnavailable("registry: lease begin "+paperID, err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the paper's default asset. The row is the conversion target;
	// FOR UPDATE serializes contenders.
	var (
		grant      LeaseGrant
		bareArxiv  *string
		version    *int
		sha        *string
		mdPath     *string
		existingID *string
		requester  *string
		expiresAt  *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT a.asset_id, a.source, a.arxiv_version, a.pdf_path, a.pdf_sha256,
		       a.mineru_md_path, a.lease_id, a.lease_holder, a.lease_expires_at,
		       p.arxiv_id
		FROM papers p
		JOIN paper_assets a ON a.asset_id = p.default_asset_id
		WHERE p.paper_id = $1
		FOR UPDATE OF a`, paperID,
	).Scan(&grant.AssetID, &grant.Source, &version, &grant.PDFPath, &sha,
		&mdPath, &existingID, &requester, &expiresAt, &bareArxiv)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return LeaseGrant{}, ErrNotLeasable
		}
		return LeaseGrant{}, catalogUnavailable("registry: lease load "+paperID, err)
	}
	now := time.Now().UTC()
	if expiresAt != nil && !expiresAt.Before(now) {
		return LeaseGrant{}, &ErrAlreadyLeased{Existing: &LeaseGrant{
			LeaseID:   deref(existingID),
			PaperID:   paperID,
			AssetID:   grant.AssetID,
			Holder:    deref(requester),
			ExpiresAt: expiresAt.UTC(),
		}}
	}
	if mdPath != nil {
		return LeaseGrant{}, ErrNotLeasable
	}
	var dbExpiresAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE paper_assets
		SET lease_holder = $2,
		    lease_expires_at = now() + make_interval(secs => $3),
		    lease_id = $4
		WHERE asset_id = $1
		RETURNING lease_expires_at`,
		grant.AssetID, holder, ttl, leaseID,
	).Scan(&dbExpiresAt)
	if err != nil {
		return LeaseGrant{}, catalogUnavailable("registry: lease update "+paperID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return LeaseGrant{}, catalogUnavailable("registry: lease commit "+paperID, err)
	}
	grant.LeaseID = leaseID
	grant.PaperID = paperID
	grant.ArxivID = versionedArxivID(deref(bareArxiv), derefInt(version))
	grant.PDFSha256 = deref(sha)
	grant.Holder = holder
	grant.CreatedAt = now
	grant.ExpiresAt = dbExpiresAt.UTC()
	grant.TTLSeconds = ttl
	return grant, nil
}

// ReleaseLease removes a lease, refusing when leaseID doesn't match the
// active one. Returns nil when released or already gone (idempotent);
// ErrIDMismatch when a different active lease holds the asset.
func (s *Store) ReleaseLease(ctx context.Context, paperID, leaseID string) error {
	if !s.ensure(ctx) {
		return ErrCatalogUnavailable
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE paper_assets a
		SET lease_holder = NULL,
		    lease_expires_at = NULL,
		    lease_id = NULL
		FROM papers p
		WHERE p.paper_id = $1
		  AND a.asset_id = p.default_asset_id
		  AND a.lease_id = $2`,
		paperID, leaseID)
	if err != nil {
		return catalogUnavailable("registry: release lease "+paperID, err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// Nothing removed — is there a *different* active lease, or just
	// nothing to release (idempotent)?
	var activeID string
	err = s.pool.QueryRow(ctx, `
		SELECT a.lease_id
		FROM papers p
		JOIN paper_assets a ON a.asset_id = p.default_asset_id
		WHERE p.paper_id = $1
		  AND a.lease_id IS NOT NULL
		  AND a.lease_expires_at >= now()
		LIMIT 1`,
		paperID,
	).Scan(&activeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // idempotent: already released / expired
		}
		return catalogUnavailable("registry: release lease check "+paperID, err)
	}
	return ErrIDMismatch
}

// GCExpiredLeases clears all expired leases in one pass. Idempotent and
// safe to run from both edges. Returns the number of leases cleared.
func (s *Store) GCExpiredLeases(ctx context.Context) (int64, error) {
	if !s.ensure(ctx) {
		return 0, ErrCatalogUnavailable
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE paper_assets
		SET lease_holder = NULL,
		    lease_expires_at = NULL,
		    lease_id = NULL
		WHERE lease_expires_at IS NOT NULL
		  AND lease_expires_at < now()`)
	if err != nil {
		return 0, catalogUnavailable("registry: gc leases", err)
	}
	return tag.RowsAffected(), nil
}

// newLeaseID generates a 32-char hex id (16 random bytes).
func newLeaseID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// derefInt maps SQL NULL to 0.
func derefInt(n *int) int {
	if n == nil {
		return 0
	}
	return *n
}
