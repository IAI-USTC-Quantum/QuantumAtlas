package papers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// Claim TTL bounds (seconds), matching the legacy mineruclaim constants
// so client expectations are unchanged.
const (
	DefaultTTLSeconds = 1800 // 30 minutes
	MinTTLSeconds     = 60
	MaxTTLSeconds     = 7200 // 2 hours
)

// Claim is the lease record returned to the API. JSON tags mirror the
// v0.6.0 mineruclaim.Claim so the /mineru-claim response shape is stable.
type Claim struct {
	ClaimID    string `json:"claim_id"`
	ArxivID    string `json:"arxiv_id"`
	Key        string `json:"key"`
	Requester  string `json:"requester,omitempty"`
	CreatedAt  string `json:"created_at"`
	ExpiresAt  string `json:"expires_at"`
	TTLSeconds int    `json:"ttl_seconds"`
	PDFURL     string `json:"pdf_url,omitempty"`
}

// CreateOptions parameterizes Claim.
type CreateOptions struct {
	ArxivID    string
	Requester  string
	TTLSeconds int
	PDFURL     string
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

// Claim atomically grants a MinerU lease via a single MERGE/SET that
// only matches when the paper has a PDF, lacks markdown, and has no
// unexpired claim. The MERGE node lock guarantees two concurrent
// transactions can't both win. Returns:
//
//	(*Claim, nil)              lease granted
//	(nil, *ErrAlreadyClaimed)  active lease held by someone
//	(nil, ErrNotClaimable)     no PDF / already has MD / not in catalog
//	(nil, ErrCatalogUnavailable) Neo4j down
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
	rows, err := s.nc.ExecuteWrite(ctx, `
		MATCH (p:PaperWork {arxiv_id: $arxiv_id})
		WHERE p.has_pdf = true AND coalesce(p.has_md, false) <> true
		  AND (p.claim_expires_at IS NULL OR p.claim_expires_at < datetime())
		SET p.claimed_by_login = $login,
		    p.claim_expires_at  = datetime() + duration({seconds: $ttl}),
		    p.claim_id          = $claim_id
		RETURN p.claim_id AS claim_id, p.claimed_by_login AS requester,
		       toString(p.claim_expires_at) AS expires_at`,
		map[string]any{
			"arxiv_id": id.ArxivID,
			"login":    opts.Requester,
			"ttl":      int64(ttl),
			"claim_id": claimID,
		})
	if err != nil {
		return nil, fmt.Errorf("papers: claim %s: %w", id.ArxivID, err)
	}
	if len(rows) == 0 {
		// Distinguish "already claimed" from "not claimable" for a
		// useful 409 body.
		return nil, s.classifyClaimFailure(ctx, id.ArxivID)
	}
	now := time.Now().UTC()
	return &Claim{
		ClaimID:    claimID,
		ArxivID:    id.ArxivID,
		Key:        paperassets.StorageKey(id.ArxivID),
		Requester:  opts.Requester,
		CreatedAt:  now.Format(time.RFC3339),
		ExpiresAt:  now.Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
		TTLSeconds: ttl,
		PDFURL:     opts.PDFURL,
	}, nil
}

// classifyClaimFailure inspects why a Claim matched 0 rows so the
// handler can return a precise 409 (already claimed, with details) vs a
// 404/409 (no PDF / has MD / unknown paper).
func (s *Store) classifyClaimFailure(ctx context.Context, arxivID string) error {
	rows, err := s.nc.ExecuteReadParams(ctx, `
		MATCH (p:PaperWork {arxiv_id: $arxiv_id})
		RETURN coalesce(p.has_pdf, false) AS has_pdf,
		       coalesce(p.has_md, false) AS has_md,
		       p.claim_id AS claim_id,
		       p.claimed_by_login AS requester,
		       toString(p.claim_expires_at) AS expires_at,
		       (p.claim_expires_at IS NOT NULL AND p.claim_expires_at >= datetime()) AS claim_active`,
		map[string]any{"arxiv_id": arxivID})
	if err != nil {
		return fmt.Errorf("papers: classify claim failure: %w", err)
	}
	if len(rows) == 0 {
		return ErrNotClaimable // paper not in catalog
	}
	r := rows[0]
	active, _ := r["claim_active"].(bool)
	if active {
		return &ErrAlreadyClaimed{Existing: &Claim{
			ClaimID:   asString(r["claim_id"]),
			ArxivID:   arxivID,
			Requester: asString(r["requester"]),
			ExpiresAt: asString(r["expires_at"]),
		}}
	}
	return ErrNotClaimable // no PDF or already has MD
}

// ReleaseClaim removes a lease, refusing when claim_id doesn't match the
// active one. Returns (true,nil) when released or already gone;
// (false, ErrIDMismatch) when a different active lease holds the node.
func (s *Store) ReleaseClaim(ctx context.Context, arxivID, claimID string) (bool, error) {
	if !s.ensure(ctx) {
		return false, ErrCatalogUnavailable
	}
	id := deriveIDs(arxivID)
	// First try the matching-id removal.
	rows, err := s.nc.ExecuteWrite(ctx, `
		MATCH (p:PaperWork {arxiv_id: $arxiv_id})
		WHERE p.claim_id = $claim_id
		REMOVE p.claimed_by_login, p.claim_expires_at, p.claim_id
		RETURN p.arxiv_id AS arxiv_id`,
		map[string]any{"arxiv_id": id.ArxivID, "claim_id": claimID})
	if err != nil {
		return false, fmt.Errorf("papers: release claim %s: %w", id.ArxivID, err)
	}
	if len(rows) > 0 {
		return true, nil
	}
	// Nothing removed — is there a *different* active lease, or just
	// nothing to release (idempotent)?
	chk, err := s.nc.ExecuteReadParams(ctx, `
		MATCH (p:PaperWork {arxiv_id: $arxiv_id})
		WHERE p.claim_id IS NOT NULL
		  AND p.claim_expires_at >= datetime()
		RETURN p.claim_id AS claim_id`,
		map[string]any{"arxiv_id": id.ArxivID})
	if err != nil {
		return false, fmt.Errorf("papers: release claim check %s: %w", id.ArxivID, err)
	}
	if len(chk) > 0 {
		return false, ErrIDMismatch
	}
	return true, nil // idempotent: already released / expired
}

// GCExpiredClaims removes all expired leases in one pass. Idempotent and
// safe to run from both edges. Returns the number of claims cleared.
func (s *Store) GCExpiredClaims(ctx context.Context) (int, error) {
	if !s.ensure(ctx) {
		return 0, ErrCatalogUnavailable
	}
	rows, err := s.nc.ExecuteWrite(ctx, `
		MATCH (p:PaperWork)
		WHERE p.claim_expires_at IS NOT NULL AND p.claim_expires_at < datetime()
		REMOVE p.claimed_by_login, p.claim_expires_at, p.claim_id
		RETURN count(p) AS n`, nil)
	if err != nil {
		return 0, fmt.Errorf("papers: gc claims: %w", err)
	}
	if len(rows) == 0 {
		return 0, nil
	}
	return asInt(rows[0]["n"]), nil
}

// newClaimID generates a 32-char hex id (16 random bytes).
func newClaimID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
