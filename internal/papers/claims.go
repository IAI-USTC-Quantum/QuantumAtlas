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

// Claim TTL bounds (seconds), matching the legacy mineruclaim constants
// so client expectations are unchanged.
const (
	DefaultTTLSeconds = 1800 // 30 minutes
	MinTTLSeconds     = 60
	MaxTTLSeconds     = 7200 // 2 hours
)

// Claim is the lease record returned to the API.
//
// Contract changed in v0.9.0: PDFURL is now always the canonical
// arxiv.org versioned URL (https://arxiv.org/pdf/<id>v<n>), never a
// presigned link to our RustFS bucket — the contributor MinerU path
// does not depend on the server handing back PDF bytes. Contributors
// fetch the PDF from arxiv themselves and verify it matches our
// catalog via PDFSha256 (the
// sha256 of the canonical PDF currently stored in RustFS, populated
// from object metadata when available). On upload-mineru the server
// re-checks the contributor's reported sha256 against this same
// metadata — mismatch → 400.
//
// PDFSha256 may be empty when the stored PDF has no sha256 metadata
// (legacy upload before P15 sidecar metadata, or any backend that
// doesn't surface metadata). In that case the contributor is free
// to proceed but the server can't enforce byte equality.
type Claim struct {
	ClaimID    string `json:"claim_id"`
	ArxivID    string `json:"arxiv_id"`
	Key        string `json:"key"`
	Requester  string `json:"requester,omitempty"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
	TTLSeconds int    `json:"ttl_seconds"`
	PDFURL     string `json:"pdf_url,omitempty"`
	PDFSha256  string `json:"pdf_sha256,omitempty"`
}

// CreateOptions parameterizes Claim.
type CreateOptions struct {
	ArxivID    string
	Requester  string
	TTLSeconds int
	PDFURL     string
	PDFSha256  string
}

// ErrAlreadyClaimed is returned when an active lease (held by anyone)
// blocks a new claim. Carries the conflicting lease for the 409 body.
type ErrAlreadyClaimed struct {
	Existing *Claim
}

func (e *ErrAlreadyClaimed) Error() string {
	return fmt.Sprintf("%s is already claimed", e.Existing.ArxivID)
}

// ErrIDMismatch is returned by ReleaseClaim when the caller's claim id
// doesn't match the active lease.
var ErrIDMismatch = errors.New("claim_id does not match the active claim")

// ErrNotClaimable is returned when the paper can't be claimed because it
// has no PDF, already has markdown, or isn't in the catalog at all.
var ErrNotClaimable = errors.New("paper has no PDF or already has markdown")

// Claim atomically grants a MinerU lease inside one PostgreSQL
// transaction. SELECT ... FOR UPDATE serializes contenders for the same
// paper row; the first live transaction to set claim_id wins. Returns:
//
//	(*Claim, nil)              lease granted
//	(nil, *ErrAlreadyClaimed)  active lease held by someone
//	(nil, ErrNotClaimable)     no PDF / already has MD / not in catalog
//	(nil, ErrCatalogUnavailable) PostgreSQL down
func (s *Store) Claim(ctx context.Context, opts CreateOptions) (*Claim, error) {
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
	id := deriveIDs(opts.ArxivID)
	claimID := newClaimID()
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, catalogUnavailable(fmt.Sprintf("papers: claim begin %s", id.ArxivID), err)
	}
	defer tx.Rollback(ctx)

	var (
		hasPDF, hasMD bool
		existingID    *string
		requester     *string
		expiresAt     *time.Time
	)
	err = tx.QueryRow(ctx, `
		SELECT has_pdf, has_md, claim_id, claimed_by_login, claim_expires_at
		FROM paper_works
		WHERE arxiv_id = $1 AND identifier_scheme <> 'doi'
		FOR UPDATE`, id.ArxivID,
	).Scan(&hasPDF, &hasMD, &existingID, &requester, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotClaimable
		}
		return nil, catalogUnavailable(fmt.Sprintf("papers: claim load %s", id.ArxivID), err)
	}
	now := time.Now().UTC()
	if expiresAt != nil && !expiresAt.Before(now) {
		return nil, &ErrAlreadyClaimed{Existing: &Claim{
			ClaimID:   derefString(existingID),
			ArxivID:   id.ArxivID,
			Requester: derefString(requester),
			ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
		}}
	}
	if !hasPDF || hasMD {
		return nil, ErrNotClaimable
	}
	var dbExpiresAt time.Time
	err = tx.QueryRow(ctx, `
		UPDATE paper_works
		SET claimed_by_login = $2,
		    claim_expires_at = now() + make_interval(secs => $3),
		    claim_id = $4
		WHERE arxiv_id = $1 AND identifier_scheme <> 'doi'
		RETURN claim_expires_at`,
		id.ArxivID, opts.Requester, ttl, claimID,
	).Scan(&dbExpiresAt)
	if err != nil {
		return nil, catalogUnavailable(fmt.Sprintf("papers: claim update %s", id.ArxivID), err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, catalogUnavailable(fmt.Sprintf("papers: claim commit %s", id.ArxivID), err)
	}
	return &Claim{
		ClaimID:    claimID,
		ArxivID:    id.ArxivID,
		Key:        paperassets.StorageKey(id.ArxivID),
		Requester:  opts.Requester,
		CreatedAt:  now.Format(time.RFC3339),
		ExpiresAt:  dbExpiresAt.UTC().Format(time.RFC3339),
		TTLSeconds: ttl,
		PDFURL:     opts.PDFURL,
		PDFSha256:  opts.PDFSha256,
	}, nil
}

// classifyClaimFailure inspects why a Claim matched 0 rows so the
// handler can return a precise 409 (already claimed, with details) vs a
// 404/409 (no PDF / has MD / unknown paper).
func (s *Store) classifyClaimFailure(ctx context.Context, arxivID string) error {
	var (
		existingID *string
		requester  *string
		expiresAt  *time.Time
	)
	err := s.pool.QueryRow(ctx, `
		SELECT claim_id, claimed_by_login, claim_expires_at
		FROM paper_works
		WHERE arxiv_id = $1
		  AND identifier_scheme <> 'doi'
		  AND claim_id IS NOT NULL
		  AND claim_expires_at >= now()`,
		arxivID,
	).Scan(&existingID, &requester, &expiresAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotClaimable
		}
		return catalogUnavailable("papers: classify claim failure", err)
	}
	claim := &Claim{
		ClaimID:   derefString(existingID),
		ArxivID:   arxivID,
		Requester: derefString(requester),
	}
	if expiresAt != nil {
		claim.ExpiresAt = expiresAt.UTC().Format(time.RFC3339)
	}
	return &ErrAlreadyClaimed{Existing: claim}
}

// ReleaseClaim removes a lease, refusing when claim_id doesn't match the
// active one. Returns (true,nil) when released or already gone;
// (false, ErrIDMismatch) when a different active lease holds the node.
func (s *Store) ReleaseClaim(ctx context.Context, arxivID, claimID string) (bool, error) {
	if !s.ensure(ctx) {
		return false, ErrCatalogUnavailable
	}
	id := deriveIDs(arxivID)
	tag, err := s.pool.Exec(ctx, `
		UPDATE paper_works
		SET claimed_by_login = NULL,
		    claim_expires_at = NULL,
		    claim_id = NULL
		WHERE arxiv_id = $1
		  AND claim_id = $2
		  AND identifier_scheme <> 'doi'`,
		id.ArxivID, claimID)
	if err != nil {
		return false, catalogUnavailable(fmt.Sprintf("papers: release claim %s", id.ArxivID), err)
	}
	if tag.RowsAffected() > 0 {
		return true, nil
	}
	// Nothing removed — is there a *different* active lease, or just
	// nothing to release (idempotent)?
	var activeID string
	err = s.pool.QueryRow(ctx, `
		SELECT claim_id
		FROM paper_works
		WHERE arxiv_id = $1
		  AND claim_id IS NOT NULL
		  AND claim_expires_at >= now()
		  AND identifier_scheme <> 'doi'
		LIMIT 1`,
		id.ArxivID,
	).Scan(&activeID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return true, nil // idempotent: already released / expired
		}
		return false, catalogUnavailable(fmt.Sprintf("papers: release claim check %s", id.ArxivID), err)
	}
	return false, ErrIDMismatch
}

// GCExpiredClaims removes all expired leases in one pass. Idempotent and
// safe to run from both edges. Returns the number of claims cleared.
func (s *Store) GCExpiredClaims(ctx context.Context) (int, error) {
	if !s.ensure(ctx) {
		return 0, ErrCatalogUnavailable
	}
	tag, err := s.pool.Exec(ctx, `
		UPDATE paper_works
		SET claimed_by_login = NULL,
		    claim_expires_at = NULL,
		    claim_id = NULL
		WHERE claim_expires_at IS NOT NULL
		  AND claim_expires_at < now()`)
	if err != nil {
		return 0, catalogUnavailable("papers: gc claims", err)
	}
	return int(tag.RowsAffected()), nil
}

// newClaimID generates a 32-char hex id (16 random bytes).
func newClaimID() string {
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
