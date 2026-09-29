package comments

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// Idempotency implements the plan §12.2 replay contract for POST
// create/reply:
//
//	key absent             → execute, remember (key, requestHash, result)
//	same key, same hash    → replay the remembered result verbatim
//	same key, other hash   → ErrIdempotencyMismatch (409)
//
// requestHash = SHA-256(method + path + body) — the caller supplies the
// exact method/path/body it saw; the key itself is the raw
// Idempotency-Key header value.
//
// Scope/retention decisions (recorded in the Q2 commit): the cache is
// process-local and bounded (oldest-evicted); replays do not survive a
// server restart, and a restart between two retries of the same key
// simply creates a second discussion — the same trade-off GitHub-style
// APIs make for non-persisted idempotency windows. Persisting to PG is
// a TODO if Q5 acceptance demands cross-restart replay.
type Idempotency struct {
	mu    sync.Mutex
	cache map[string]idempotencyEntry
	order []string
	cap   int
}

type idempotencyEntry struct {
	requestHash string
	status      int
	body        []byte
}

// NewIdempotency returns a cache holding at most capacity entries
// (oldest evicted; capacity<=0 → 1024).
func NewIdempotency(capacity int) *Idempotency {
	if capacity <= 0 {
		capacity = 1024
	}
	return &Idempotency{cache: make(map[string]idempotencyEntry), cap: capacity}
}

// RequestHash fingerprints one create/reply attempt.
func RequestHash(method, path string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(method))
	h.Write([]byte{0})
	h.Write([]byte(path))
	h.Write([]byte{0})
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

// Result returns the remembered (status, body) for key when the stored
// request hash matches. Errors: ErrIdempotencyMismatch on hash
// mismatch; a plain "miss" is reported as ok=false.
func (i *Idempotency) Result(key, requestHash string) (status int, body []byte, ok bool, err error) {
	if key == "" {
		return 0, nil, false, nil
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	e, exists := i.cache[key]
	if !exists {
		return 0, nil, false, nil
	}
	if e.requestHash != requestHash {
		return 0, nil, false, ErrIdempotencyMismatch
	}
	return e.status, e.body, true, nil
}

// Remember stores the outcome for key. Empty keys are ignored (no
// Idempotency-Key header → no replay semantics, plain create).
func (i *Idempotency) Remember(key, requestHash string, status int, body []byte) {
	if key == "" {
		return
	}
	i.mu.Lock()
	defer i.mu.Unlock()
	if _, exists := i.cache[key]; !exists {
		i.order = append(i.order, key)
		if len(i.order) > i.cap {
			evict := i.order[0]
			i.order = i.order[1:]
			delete(i.cache, evict)
		}
	}
	i.cache[key] = idempotencyEntry{requestHash: requestHash, status: status, body: append([]byte(nil), body...)}
}
