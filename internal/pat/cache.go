// PAT verification cache — a TTL-bounded LRU keyed by sha256(plaintext)
// that skips bcrypt + DB lookups on hot tokens.
//
// # Why this exists
//
// `bcrypt.CompareHashAndPassword` at cost 10 (the default we generate
// at) burns ~50–100 ms of single-core CPU per call. Every authenticated
// write to qatlas goes through it, and the Go runtime serialises CPU-
// bound calls on the same OS thread, so a CI pipeline that fires a dozen
// concurrent uploads with the same PAT can pin a whole core and starve
// other handlers (including unrelated read traffic) of scheduler time.
//
// On RackNerd's single 1.4 GB VM a steady-state of ~10 RPS with PATs
// translated to ~50% CPU just on bcrypt — measurably blowing tail
// latency on every other route. The cache collapses repeat verifies
// for the same token down to a map lookup + expiry check.
//
// # Why not just lower bcrypt cost
//
// Cost 10 is the bcrypt default and gives us ~25–35 ms on modern x86
// (cost 8 would be ~6 ms but reduces brute-force resistance against
// stolen hashes from 2^17 to 2^15 attempts — still safe for one-shot
// CLI tokens but a downgrade in defence-in-depth). The cache keeps the
// hash strong while eliminating the per-request cost for hot tokens.
//
// # Security model
//
// 1. Cache key is sha256(plaintext), NOT the plaintext. A snapshot of
//    cache memory (e.g. core dump) can't be replayed against the
//    server — sha256 is one-way, and the per-process cache lifetime
//    is bounded.
//
// 2. TTL bounds how stale a verified result can be. Default 60 s —
//    if a user revokes a PAT, the cache stops accepting it within at
//    most TTL after revocation. The DELETE /api/pat/{id} handler
//    additionally calls Invalidate immediately so revocation is
//    effectively instant on the node that received the request (lag
//    only applies to cross-node revocation in active-active deploys).
//
// 3. LRU cap (default 4096) bounds memory. Each entry is ~150 bytes;
//    at cap we hold ~600 KiB. Tiny next to qatlas's working set.
//
// 4. Cache stores only the identifiers needed to short-circuit Lookup:
//    PAT record id, user id, scopes JSON, expires_at. NOT the bcrypt
//    hash, NOT the plaintext.
//
// 5. Negative results (token doesn't match any record) are NOT cached.
//    Caching "this token is invalid" would let an attacker measure
//    cache occupancy via timing — and the bcrypt cost only applies
//    to tokens with a matching prefix in the DB anyway, which is
//    rare for random brute-force attempts.
//
// # Concurrency
//
// hashicorp/golang-lru/v2 is internally synchronised; our wrapper adds
// no extra locking. Multiple goroutines verifying the same token race
// safely: any of them may end up doing the bcrypt and writing the
// result; subsequent races read the same cached result. The duplicate
// bcrypt work in a thundering-herd is bounded by goroutine count, not
// request rate, because once one writes the result all others read it.

package pat

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	lru "github.com/hashicorp/golang-lru/v2"
)

// VerifyCacheConfig tunes the cache. Defaults are applied when fields
// are zero/empty — see DefaultVerifyCacheConfig.
type VerifyCacheConfig struct {
	// MaxEntries is the LRU capacity. Older entries are evicted when
	// new ones land. 0 → DefaultVerifyCacheConfig.MaxEntries.
	MaxEntries int

	// TTL is how long a verified result is considered fresh.
	// 0 → DefaultVerifyCacheConfig.TTL. Negative disables the cache
	// entirely (Lookup always pays the bcrypt cost).
	TTL time.Duration
}

// DefaultVerifyCacheConfig is the production-tuned default —
// 4096 entries × 60 s TTL ≈ 600 KiB resident memory and at-most-60-s
// revocation lag. Empirically eliminates >99% of bcrypt calls under
// normal CI traffic (the same PAT firing many requests in quick
// succession).
var DefaultVerifyCacheConfig = VerifyCacheConfig{
	MaxEntries: 4096,
	TTL:        60 * time.Second,
}

// verifyCacheEntry is what we persist per token. Deliberately small —
// no bcrypt hash, no plaintext, only the identifiers Lookup needs to
// short-circuit. Embedded by value into the LRU; ~120 bytes per entry.
type verifyCacheEntry struct {
	PATRecordID    string    // pat_tokens.id
	UserID         string    // users.id (linked record)
	ScopesRaw      string    // pat_tokens.scopes (JSON-encoded list)
	PATExpiresAt   time.Time // pat_tokens.expires_at; we re-check on each hit
	CachedAt       time.Time // for TTL comparison
}

// verifyCache is the in-memory LRU+TTL store. It is created by
// initVerifyCache (called lazily on first Lookup) so tests that don't
// exercise PAT auth pay zero setup cost.
type verifyCache struct {
	lru *lru.Cache[[32]byte, verifyCacheEntry]
	ttl time.Duration
}

// Get returns the cached entry for plaintext if and only if all three
// hold: an entry exists, it's not past its TTL window, and the PAT
// itself hasn't expired since we cached it. The third check covers
// PATs that expire during their cached window — the cache must not
// extend a token's lifetime past expires_at.
//
// Returns (zero, false) when no usable entry exists.
func (c *verifyCache) Get(plaintext string) (verifyCacheEntry, bool) {
	if c == nil || c.lru == nil {
		return verifyCacheEntry{}, false
	}
	key := sha256.Sum256([]byte(plaintext))
	e, ok := c.lru.Get(key)
	if !ok {
		return verifyCacheEntry{}, false
	}
	now := time.Now()
	if now.Sub(e.CachedAt) >= c.ttl {
		c.lru.Remove(key) // proactive eviction so the slot is reusable
		return verifyCacheEntry{}, false
	}
	if e.PATExpiresAt.IsZero() || now.After(e.PATExpiresAt) {
		c.lru.Remove(key)
		return verifyCacheEntry{}, false
	}
	return e, true
}

// Put records a fresh verification result. Overwrites any previous
// entry for the same plaintext (e.g. when the same PAT was just
// renewed via the admin UI, the new expires_at lands here).
func (c *verifyCache) Put(plaintext string, e verifyCacheEntry) {
	if c == nil || c.lru == nil {
		return
	}
	key := sha256.Sum256([]byte(plaintext))
	e.CachedAt = time.Now()
	c.lru.Add(key, e)
}

// InvalidateByPATID drops any entries that reference the given pat
// record id. O(N) — but N is at most MaxEntries (default 4096) and
// this is called only on PAT delete (rare). Acceptable trade-off vs
// keeping a second pat_id → key index in sync.
func (c *verifyCache) InvalidateByPATID(patID string) {
	if c == nil || c.lru == nil || patID == "" {
		return
	}
	for _, key := range c.lru.Keys() {
		e, ok := c.lru.Peek(key)
		if !ok {
			continue
		}
		if e.PATRecordID == patID {
			c.lru.Remove(key)
		}
	}
}

// Purge drops every entry. Used by tests to start from a known state;
// not normally needed in production.
func (c *verifyCache) Purge() {
	if c == nil || c.lru == nil {
		return
	}
	c.lru.Purge()
}

// Len reports the current cache occupancy. Tests assert this; production
// can ignore it.
func (c *verifyCache) Len() int {
	if c == nil || c.lru == nil {
		return 0
	}
	return c.lru.Len()
}

// -------- package-level singleton --------

// We make the verify cache a package-level singleton because every PAT
// authentication path goes through pat.Lookup, and there is exactly
// one process. Passing the cache as a parameter would force every
// caller (auth.go, future internal call sites, tests) to thread the
// dependency, with no flexibility we'd actually use. The package-level
// singleton mirrors how the bcrypt library itself holds its own
// internal state — same locality of reasoning.

var (
	verifyCacheOnce    sync.Once
	defaultVerifyCache *verifyCache
)

// initVerifyCache lazy-initialises the cache on first use with
// DefaultVerifyCacheConfig. Callers (tests) that need a custom
// configuration must call ConfigureVerifyCache before any Lookup
// fires.
func initVerifyCache() *verifyCache {
	verifyCacheOnce.Do(func() {
		c, err := newVerifyCache(DefaultVerifyCacheConfig)
		if err != nil {
			// LRU construction only fails on nonsensical (≤ 0)
			// capacity, which we sanitise in newVerifyCache. Reaching
			// here means a programming bug, not a runtime condition;
			// fall back to a disabled cache rather than crashing the
			// server.
			defaultVerifyCache = &verifyCache{lru: nil, ttl: 0}
			return
		}
		defaultVerifyCache = c
	})
	return defaultVerifyCache
}

// ConfigureVerifyCache replaces the package singleton with a fresh
// cache built from cfg. Idempotent — calling it twice replaces the
// cache and drops all entries. Intended for tests; production should
// rely on DefaultVerifyCacheConfig.
//
// Pass cfg.TTL < 0 to disable the cache entirely (Lookup will always
// bcrypt). Useful for benchmarking / debugging.
func ConfigureVerifyCache(cfg VerifyCacheConfig) error {
	c, err := newVerifyCache(cfg)
	if err != nil {
		return err
	}
	// Replace the singleton. We reset the Once so a subsequent
	// initVerifyCache call sees the new instance.
	verifyCacheOnce = sync.Once{}
	defaultVerifyCache = c
	return nil
}

// InvalidateVerifyCache drops any cached verifications for the given
// PAT record id. Called by the DELETE /api/pat/{id} handler so a
// revocation takes effect immediately on the node that received the
// request, rather than waiting for the TTL window to elapse.
//
// Cross-node revocation lag is still bounded by TTL — each edge node
// has its own cache, and there's no cross-node invalidation channel.
// The active-active deployment caveats are documented in the OAuth
// section of .github/copilot-instructions.md.
func InvalidateVerifyCache(patID string) {
	c := initVerifyCache()
	c.InvalidateByPATID(patID)
}

// PurgeVerifyCache drops every cached entry. Test helper; never call
// from production code.
func PurgeVerifyCache() {
	c := initVerifyCache()
	c.Purge()
}

// VerifyCacheLen returns the current cache occupancy. Used by tests
// and would-be metrics; never gated on by handler logic.
func VerifyCacheLen() int {
	c := initVerifyCache()
	return c.Len()
}

func newVerifyCache(cfg VerifyCacheConfig) (*verifyCache, error) {
	if cfg.MaxEntries <= 0 {
		cfg.MaxEntries = DefaultVerifyCacheConfig.MaxEntries
	}
	ttl := cfg.TTL
	if ttl == 0 {
		ttl = DefaultVerifyCacheConfig.TTL
	}
	if ttl < 0 {
		// Disabled cache: return a struct with nil lru so all
		// methods short-circuit. Get/Put become no-ops.
		return &verifyCache{lru: nil, ttl: 0}, nil
	}
	l, err := lru.New[[32]byte, verifyCacheEntry](cfg.MaxEntries)
	if err != nil {
		return nil, err
	}
	return &verifyCache{lru: l, ttl: ttl}, nil
}

// hexKey is a tiny convenience used by tests / logging that want a
// human-readable representation of the cache key. Never used by the
// hot path — Get/Put use the raw [32]byte directly.
func hexKey(plaintext string) string {
	h := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(h[:])
}
