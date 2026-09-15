package mineru

import (
	"sync"
	"time"
)

// KeyRing holds N MinerU API tokens (each wrapped in its own Client)
// and round-robins requests across them. When a key hits the daily
// quota, the ring puts ONLY that key on cooldown until the next
// midnight reset; other keys keep serving until they too exhaust.
//
// Why this matters: a single MinerU account caps at ~200 free
// extractions per day. With three accounts pooled here, a self-hosted
// operator gets ~600/day without any manual intervention and without
// the painful "whole server stops at lunch" UX of a single-token
// deployment.
//
// Concurrency: Acquire / MarkDailyLimit / state queries take the
// internal mutex; the *Client they hand out is safe for concurrent
// use (it's a thin http wrapper), so multiple converter goroutines
// can share the ring.
type KeyRing struct {
	now func() time.Time

	baseURL string // every client is built against this (see AddToken / SetEntries)

	mu      sync.Mutex
	entries []*ringEntry
	cursor  int // round-robin pointer
}

type ringEntry struct {
	token         string
	rotatedAt     time.Time // when the token was last added / re-added; zero for config-only boot
	client        *Client
	cooldownUntil time.Time // zero when free
}

// TokenEntry pairs a token string with its last-rotation timestamp,
// the shape the DB-backed pool (TokenStore) feeds the ring with.
type TokenEntry struct {
	Token     string
	RotatedAt time.Time
}

// NewKeyRing builds a ring from N tokens, recording no rotation
// timestamps (config-only boot). See NewKeyRingFromEntries for the
// DB-backed variant.
func NewKeyRing(tokens []string, baseURL string, now func() time.Time) *KeyRing {
	entries := make([]TokenEntry, 0, len(tokens))
	for _, tok := range tokens {
		entries = append(entries, TokenEntry{Token: tok})
	}
	return NewKeyRingFromEntries(entries, baseURL, now)
}

// NewKeyRingFromEntries builds a ring from N token entries (token +
// rotated_at). baseURL + httpClient are shared across every key
// (NewClient call); the only per-key state is the token string, the
// rotation timestamp, and the cooldown.
//
// At least one non-empty token is required — the caller is expected
// to have already validated this against the master switch and
// emitted the appropriate "cache-only" disabled message.
func NewKeyRingFromEntries(entries []TokenEntry, baseURL string, now func() time.Time) *KeyRing {
	if now == nil {
		now = time.Now
	}
	ring := make([]*ringEntry, 0, len(entries))
	for _, e := range entries {
		if e.Token == "" {
			continue
		}
		ring = append(ring, &ringEntry{
			token:     e.Token,
			rotatedAt: e.RotatedAt,
			client:    NewClient(e.Token, baseURL, nil),
		})
	}
	return &KeyRing{now: now, baseURL: baseURL, entries: ring}
}

// Size returns how many keys are loaded (non-empty after trimming).
func (k *KeyRing) Size() int {
	if k == nil {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	return len(k.entries)
}

// Acquire returns a client whose token is not currently in daily-limit
// cooldown, and the slot index so the caller can MarkDailyLimit on
// failure. ok=false when every loaded key is on cooldown.
//
// Picks slots round-robin starting from the cursor so concurrent
// callers tend to spread across keys rather than all hammering #0.
func (k *KeyRing) Acquire() (client *Client, slot int, ok bool) {
	if k == nil {
		return nil, -1, false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	n := len(k.entries)
	if n == 0 {
		return nil, -1, false
	}
	now := k.now()
	for i := 0; i < n; i++ {
		idx := (k.cursor + i) % n
		e := k.entries[idx]
		if e.cooldownUntil.IsZero() || !now.Before(e.cooldownUntil) {
			k.cursor = (idx + 1) % n
			return e.client, idx, true
		}
	}
	return nil, -1, false
}

// MarkDailyLimit places slot on cooldown until `until`. Idempotent;
// only extends the cooldown, never shortens it.
func (k *KeyRing) MarkDailyLimit(slot int, until time.Time) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if slot < 0 || slot >= len(k.entries) {
		return
	}
	e := k.entries[slot]
	if until.After(e.cooldownUntil) {
		e.cooldownUntil = until
	}
}

// SoonestRecovery returns the earliest cooldownUntil across all keys.
// Zero time when at least one key is free *right now* (so the caller
// can keep submitting). Used by the converter to set the paper-level
// CooldownUntil when every key is exhausted.
func (k *KeyRing) SoonestRecovery() time.Time {
	if k == nil {
		return time.Time{}
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	var earliest time.Time
	for _, e := range k.entries {
		if e.cooldownUntil.IsZero() || !now.Before(e.cooldownUntil) {
			return time.Time{} // some key is free
		}
		if earliest.IsZero() || e.cooldownUntil.Before(earliest) {
			earliest = e.cooldownUntil
		}
	}
	return earliest
}

// AvailableSlots returns the count of keys NOT currently in cooldown.
// Used for the converter Snapshot and the startup log line so
// operators can see how many keys are usable right now.
func (k *KeyRing) AvailableSlots() int {
	if k == nil {
		return 0
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	free := 0
	for _, e := range k.entries {
		if e.cooldownUntil.IsZero() || !now.Before(e.cooldownUntil) {
			free++
		}
	}
	return free
}

// KeyState is the admin-facing view of one ring entry. Token is the
// raw secret — mask it (MaskToken) before crossing an API boundary.
type KeyState struct {
	Token         string
	RotatedAt     time.Time
	CooldownUntil time.Time
}

// AddToken upserts a token into the ring: an existing entry only gets
// its rotatedAt refreshed (cooldown survives — re-adding a quota-shot
// key must not reset its cooldown), a new entry is appended. Returns
// whether the token was already present.
func (k *KeyRing) AddToken(token string, rotatedAt time.Time) bool {
	if k == nil || token == "" {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for _, e := range k.entries {
		if e.token == token {
			e.rotatedAt = rotatedAt
			return true
		}
	}
	entry := &ringEntry{
		token:     token,
		rotatedAt: rotatedAt,
		client:    NewClient(token, k.baseURL, nil),
	}
	k.entries = append(k.entries, entry)
	return false
}

// RemoveToken drops the entry with the exact token string. The cursor
// is re-modulo'd so it keeps pointing inside the (now smaller) ring.
// Returns whether a matching entry existed.
func (k *KeyRing) RemoveToken(token string) bool {
	if k == nil {
		return false
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	for i, e := range k.entries {
		if e.token != token {
			continue
		}
		k.entries = append(k.entries[:i], k.entries[i+1:]...)
		if len(k.entries) == 0 {
			k.cursor = 0
		} else if k.cursor >= len(k.entries) {
			k.cursor = k.cursor % len(k.entries)
		}
		return true
	}
	return false
}

// States copies the per-entry identity + cooldown state, in ring
// order. The *Client pointers stay private; callers get values only.
func (k *KeyRing) States() []KeyState {
	if k == nil {
		return nil
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	out := make([]KeyState, 0, len(k.entries))
	for _, e := range k.entries {
		out = append(out, KeyState{
			Token:         e.token,
			RotatedAt:     e.rotatedAt,
			CooldownUntil: e.cooldownUntil,
		})
	}
	return out
}

// SetEntries atomically replaces the whole pool, preserving the
// cooldown of every token that survives the swap (a boot-time
// reconcile must not reset today's quota cooldowns). Used by the
// post-migration hook: the converter boots with the config list, then
// the registry DB becomes authoritative once its schema exists.
func (k *KeyRing) SetEntries(entries []TokenEntry) {
	if k == nil {
		return
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	prev := make(map[string]time.Time, len(k.entries))
	for _, e := range k.entries {
		prev[e.token] = e.cooldownUntil
	}
	ring := make([]*ringEntry, 0, len(entries))
	for _, en := range entries {
		if en.Token == "" {
			continue
		}
		e := &ringEntry{
			token:     en.Token,
			rotatedAt: en.RotatedAt,
			client:    NewClient(en.Token, k.baseURL, nil),
		}
		if cd, ok := prev[en.Token]; ok {
			e.cooldownUntil = cd
		}
		ring = append(ring, e)
	}
	k.entries = ring
	if len(ring) == 0 {
		k.cursor = 0
	} else if k.cursor >= len(ring) {
		k.cursor %= len(ring)
	}
}
