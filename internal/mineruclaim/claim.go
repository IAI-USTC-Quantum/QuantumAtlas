// Package mineruclaim implements the short-lived "claim" lease used by
// contributors to avoid burning their MinerU quota twice on the same
// paper. It's a direct port of the claim helpers under
// atlas/server/routers/api.py (search for `_claim_path` etc.).
//
// Storage: one JSON file per arxiv id under DATA_DIR/mineru-claims/.
// Atomicity: O_EXCL create for the happy path, tmp + rename for the
// "overwrite expired claim" path. Concurrent-process safe.
package mineruclaim

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// TTL bounds for an outstanding claim, in seconds. Match the Python
// `_CLAIM_*_TTL_SECONDS` constants.
const (
	DefaultTTLSeconds = 1800 // 30 minutes
	MinTTLSeconds     = 60
	MaxTTLSeconds     = 7200 // 2 hours
)

// Claim is the persisted lease record.
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

// Store is the dir-backed claim store.
type Store struct {
	BaseDir string
}

// NewStore initializes the claims directory and returns a handle.
func NewStore(baseDir string) (*Store, error) {
	if err := os.MkdirAll(baseDir, 0o755); err != nil {
		return nil, fmt.Errorf("mineruclaim: mkdir %s: %w", baseDir, err)
	}
	return &Store{BaseDir: baseDir}, nil
}

// Path is the per-paper claim file path.
func (s *Store) Path(arxivID string) string {
	return filepath.Join(s.BaseDir, paperassets.StorageKey(arxivID)+".json")
}

// Read returns the current claim for arxivID, or (nil, nil) if no claim.
// Corrupt or unparseable files return (nil, nil) — the caller will then
// overwrite with a new claim.
func (s *Store) Read(arxivID string) (*Claim, error) {
	data, err := os.ReadFile(s.Path(arxivID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var c Claim
	if json.Unmarshal(data, &c) != nil {
		return nil, nil
	}
	return &c, nil
}

// IsActive reports whether c is non-nil and not yet expired.
func IsActive(c *Claim, now time.Time) bool {
	if c == nil || c.ExpiresAt == "" {
		return false
	}
	t, err := time.Parse(time.RFC3339, c.ExpiresAt)
	if err != nil {
		return false
	}
	return t.After(now)
}

// CreateOptions parameterizes Create.
type CreateOptions struct {
	ArxivID    string
	Requester  string
	TTLSeconds int
	PDFURL     string
}

// ErrAlreadyClaimed is returned by Create when an active claim is held
// by someone else. Carries the existing claim so the route handler can
// surface it.
type ErrAlreadyClaimed struct {
	Existing *Claim
}

func (e *ErrAlreadyClaimed) Error() string {
	return fmt.Sprintf("%s is already claimed", e.Existing.ArxivID)
}

// Create atomically grants a new claim, overwriting any expired one.
// Returns *ErrAlreadyClaimed when an active lease blocks the grant.
func (s *Store) Create(opts CreateOptions) (*Claim, error) {
	ttl := opts.TTLSeconds
	if ttl < MinTTLSeconds {
		ttl = MinTTLSeconds
	}
	if ttl > MaxTTLSeconds {
		ttl = MaxTTLSeconds
	}
	if ttl == 0 {
		ttl = DefaultTTLSeconds
	}

	now := time.Now().UTC()
	claim := &Claim{
		ClaimID:    newClaimID(),
		ArxivID:    opts.ArxivID,
		Key:        paperassets.StorageKey(opts.ArxivID),
		Requester:  opts.Requester,
		CreatedAt:  now.Format(time.RFC3339),
		ExpiresAt:  now.Add(time.Duration(ttl) * time.Second).Format(time.RFC3339),
		TTLSeconds: ttl,
		PDFURL:     opts.PDFURL,
	}

	payload, err := json.Marshal(claim)
	if err != nil {
		return nil, err
	}
	target := s.Path(opts.ArxivID)
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return nil, err
	}

	// Happy path: O_EXCL atomic create.
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err == nil {
		_, werr := f.Write(payload)
		_ = f.Close()
		if werr != nil {
			_ = os.Remove(target)
			return nil, werr
		}
		return claim, nil
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, err
	}

	// File exists — only overwrite if the existing claim has expired.
	existing, _ := s.Read(opts.ArxivID)
	if IsActive(existing, now) {
		return nil, &ErrAlreadyClaimed{Existing: existing}
	}

	tmp := target + ".tmp"
	if err := os.WriteFile(tmp, payload, 0o644); err != nil {
		return nil, err
	}
	if err := os.Rename(tmp, target); err != nil {
		_ = os.Remove(tmp)
		return nil, err
	}
	return claim, nil
}

// Release removes the claim file unconditionally. Used by upload-markdown
// to free the lease on successful upload. Best-effort — IO errors are
// returned but the caller may ignore them.
func (s *Store) Release(arxivID string) error {
	err := os.Remove(s.Path(arxivID))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// ReleaseWithID is the operator-facing release that refuses to delete
// when claim_id doesn't match the active claim. Returns:
//
//	(true, nil)             on success (file removed or never existed)
//	(false, ErrIDMismatch)  when a still-active claim has a different id
//	(false, err)            on IO error
func (s *Store) ReleaseWithID(arxivID, claimID string) (bool, error) {
	existing, _ := s.Read(arxivID)
	if existing == nil {
		return true, nil // idempotent: already released
	}
	if existing.ClaimID != claimID && IsActive(existing, time.Now().UTC()) {
		return false, ErrIDMismatch
	}
	if err := s.Release(arxivID); err != nil {
		return false, err
	}
	return true, nil
}

// ErrIDMismatch is returned by ReleaseWithID when the caller's claim id
// doesn't match the active lease.
var ErrIDMismatch = errors.New("claim_id does not match the active claim")

// newClaimID generates a 32-char hex id (16 random bytes).
func newClaimID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
