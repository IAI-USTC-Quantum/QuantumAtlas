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

	mu      sync.Mutex
	entries []*ringEntry
	cursor  int // round-robin pointer
}

type ringEntry struct {
	client        *Client
	cooldownUntil time.Time // zero when free
}

// NewKeyRing builds a ring from N tokens. baseURL + httpClient are
// shared across every key (NewClient call); the only per-key state is
// the token string and the cooldown timestamp.
//
// At least one non-empty token is required — the caller is expected
// to have already validated this against the master switch and
// emitted the appropriate "cache-only" disabled message.
func NewKeyRing(tokens []string, baseURL string, now func() time.Time) *KeyRing {
	if now == nil {
		now = time.Now
	}
	entries := make([]*ringEntry, 0, len(tokens))
	for _, tok := range tokens {
		if tok == "" {
			continue
		}
		entries = append(entries, &ringEntry{client: NewClient(tok, baseURL, nil)})
	}
	return &KeyRing{now: now, entries: entries}
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
