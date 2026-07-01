package papers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/jackc/pgx/v5"
)

// Lease TTL bounds (seconds).
const (
	DefaultTTLSeconds = 1800 // 30 minutes
	MinTTLSeconds     = 60
	MaxTTLSeconds     = 7200 // 2 hours
)

// Lease is the MinerU processing lease returned by the API. It reserves a
// paper's default asset for conversion so contributors don't collide.
//
// PDFURL is the arxiv.org versioned URL the contributor fetches; PDFSha256
// is the sha256 of the PDF currently stored, so the contributor can verify
// byte equality before running MinerU (re-checked server-side on upload).
type Lease struct {
	LeaseID    string `json:"lease_id"`
	ArxivID    string `json:"arxiv_id"`
	Key        string `json:"key"`
	Requester  string `json:"requester,omitempty"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
	TTLSeconds int    `json:"ttl_seconds"`
	PDFURL     string `json:"pdf_url,omitempty"`
	PDFSha256  string `json:"pdf_sha256,omitempty"`
}

// CreateOptions parameterizes Lease.
type CreateOptions struct {
	ArxivID    string
	Requester  string
	TTLSeconds int
	PDFURL     string
	PDFSha256  string
}

// ErrAlreadyLeased is returned when an active lease (held by anyone)
// blocks a new lease. Carries the conflicting lease for the 409 body.
type ErrAlreadyLeased struct {
	Existing *Lease
}

func (e *ErrAlreadyLeased) Error() string {
	return fmt.Sprintf("%s is already leased", e.Existing.ArxivID)
}

// ErrIDMismatch is returned by ReleaseLease when the caller's lease id
// doesn't match the active lease.
var ErrIDMismatch = errors.New("lease_id does not match the active lease")

// ErrNotLeasable is returned when the paper can't be leased because it
// has no default PDF asset, that asset already has markdown, or the paper
// isn't in the catalog at all.
var ErrNotLeasable = errors.New("paper has no PDF asset or already has markdown")

// Lease atomically grants a MinerU lease on the paper's default asset
// inside one PostgreSQL transaction. SELECT ... FOR UPDATE serializes
// contenders for the same asset; the first live transaction to set
// lease_id wins. Returns:
//
//	(*Lease, nil)              lease granted
//	(nil, *ErrAlreadyLeased)  active lease held by someone
//	(nil, ErrNotLeasable)     no default PDF asset / already has MD / unknown paper
//	(nil, ErrCatalogUnavailable) PostgreSQL down
func (s *Store) Lease(ctx context.Context, opts CreateOptions) (*Lease, error) {
	if !s.ensure(ctx) {
		return nil, ErrCatalogUnavailable
	}
	ttl := opts.TTLSeconds
	if ttl == 0 {
		ttl = DefaultTTLSeconds
	}
	if ttl < MinTTLSeconds {
		ttl = MinTTLSeconds
	}
	if ttl > MaxTTLSeconds {
		ttl = MaxTTLSeconds
	}
	bare := bareArxivID(opts.ArxivID)
	leaseID := newLeaseID()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, catalogUnavailable(fmt.Sprintf("papers: lease begin %s", bare), err)
	}
	defer tx.Rollback(ctx)

	// Lock the paper's default asset. The row is the conversion target;
	// FOR UPDATE serializes contenders.
	var (
		assetID    int64
		mdPath     *string
		existingID *string
		requester  *string
		expiresAt  *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT a.asset_id, a.mineru_md_path, a.lease_id, a.lease_holder, a.lease_expires_at
		FROM papers p
		JOIN paper_assets a ON a.asset_id = p.paper_default_asset_id
		WHERE p.paper_arxiv_id = $1
		FOR UPDATE OF a`, bare,
	).Scan(&assetID, &mdPath, &existingID, &requester, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotLeasable
		}
		return nil, catalogUnavailable(fmt.Sprintf("papers: lease load %s", bare), err)
	}
	now := time.Now().UTC()
	if expiresAt != nil && !expiresAt.Before(now) {
		return nil, &ErrAlreadyLeased{Existing: &Lease{
			LeaseID:   derefString(existingID),
			ArxivID:   bare,
			Requester: derefString(requester),
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		}}
	}
	if mdPath != nil {
		return nil, ErrNotLeasable
	}
	var dbExpiresAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE paper_assets
		SET lease_holder = $2,
		    lease_expires_at = now() + make_interval(secs => $3),
		    lease_id = $4
		WHERE asset_id = $1
		RETURNING lease_expires_at`,
		assetID, opts.Requester, ttl, leaseID,
	).Scan(&dbExpiresAt)
	if err != nil {
		return nil, catalogUnavailable(fmt.Sprintf("papers: lease update %s", bare), err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, catalogUnavailable(fmt.Sprintf("papers: lease commit %s", bare), err)
	}
	return &Lease{
		LeaseID:    leaseID,
		ArxivID:    opts.ArxivID,
		Key:        paperassets.StorageKey(opts.ArxivID),
		Requester:  opts.Requester,
		CreatedAt:  now.Format(time.RFC3339),
		ExpiresAt:  dbExpiresAt.UTC().Format(time.RFC3339),
		TTLSeconds: ttl,
		PDFURL:     opts.PDFURL,
		PDFSha256:  opts.PDFSha256,
	}, nil
}

// ReleaseLease removes a lease, refusing when lease_id doesn't match the
// active one. Returns (true,nil) when released or already gone;
// (false, ErrIDMismatch) when a different active lease holds the asset.
func (s *Store) ReleaseLease(ctx context.Context, arxivID, leaseID string) (bool, error) {
	if !s.ensure(ctx) {
		return false, ErrCatalogUnavailable
	}
	bare := bareArxivID(arxivID)
	tag, err := s.pool.Exec(ctx, `
		UPDATE paper_assets a
		SET lease_holder = NULL,
		    lease_expires_at = NULL,
		    lease_id = NULL
		FROM papers p
		WHERE p.paper_arxiv_id = $1
		  AND a.asset_id = p.paper_default_asset_id
		  AND a.lease_id = $2`,
		bare, leaseID)
	if err != nil {
		return false, catalogUnavailable(fmt.Sprintf("papers: release lease %s", bare), err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	// Nothing removed — is there a *different* active lease, or just
	// nothing to release (idempotent)?
	var activeID string
	err = s.pool.QueryRow(ctx, `
		SELECT a.lease_id
		FROM papers p
		JOIN paper_assets a ON a.asset_id = p.paper_default_asset_id
		WHERE p.paper_arxiv_id = $1
		  AND a.lease_id IS NOT NULL
		  AND a.lease_expires_at >= now()
		LIMIT 1`,
		bare,
	).Scan(&activeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil // idempotent: already released / expired
		}
		return false, catalogUnavailable(fmt.Sprintf("papers: release lease check %s", bare), err)
	}
	return false, ErrIDMismatch
}

// GCExpiredLeases clears all expired leases in one pass. Idempotent and
// safe to run from both edges. Returns the number of leases cleared.
func (s *Store) GCExpiredLeases(ctx context.Context) (int, error) {
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
		return 0, catalogUnavailable("papers: gc leases", err)
	}
	return int(tag.RowsAffected()), nil
}

// newLeaseID generates a 32-char hex id (16 random bytes).
func newLeaseID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
