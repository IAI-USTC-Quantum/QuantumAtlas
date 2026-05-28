package pat

import (
	"testing"
	"time"
)

const fakePlaintext = "qat_aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fakePATID = "rec_test_pat_id"
const fakeUserID = "rec_test_user_id"

func freshEntry(t *testing.T) verifyCacheEntry {
	t.Helper()
	return verifyCacheEntry{
		PATRecordID:  fakePATID,
		UserID:       fakeUserID,
		ScopesRaw:    `["papers:write"]`,
		PATExpiresAt: time.Now().Add(24 * time.Hour),
	}
}

func TestVerifyCache_GetReturnsFalseOnMiss(t *testing.T) {
	c, err := newVerifyCache(VerifyCacheConfig{MaxEntries: 8, TTL: time.Minute})
	if err != nil {
		t.Fatalf("newVerifyCache: %v", err)
	}
	if _, ok := c.Get(fakePlaintext); ok {
		t.Errorf("Get on empty cache should miss")
	}
}

func TestVerifyCache_PutThenGetIsHit(t *testing.T) {
	c, err := newVerifyCache(VerifyCacheConfig{MaxEntries: 8, TTL: time.Minute})
	if err != nil {
		t.Fatalf("newVerifyCache: %v", err)
	}
	want := freshEntry(t)
	c.Put(fakePlaintext, want)

	got, ok := c.Get(fakePlaintext)
	if !ok {
		t.Fatal("Get after Put should hit")
	}
	if got.PATRecordID != want.PATRecordID || got.UserID != want.UserID {
		t.Errorf("got %+v, want %+v", got, want)
	}
	// CachedAt is set by Put, so it should be non-zero and recent.
	if got.CachedAt.IsZero() || time.Since(got.CachedAt) > time.Second {
		t.Errorf("CachedAt should be recent, got %v", got.CachedAt)
	}
}

// Hot path correctness: a different plaintext that happens to share
// the same prefix must produce a different cache key (sha256 covers
// the full string, no collisions in practice).
func TestVerifyCache_DifferentPlaintextDifferentKey(t *testing.T) {
	c, err := newVerifyCache(VerifyCacheConfig{MaxEntries: 8, TTL: time.Minute})
	if err != nil {
		t.Fatalf("newVerifyCache: %v", err)
	}
	c.Put(fakePlaintext, freshEntry(t))
	// Same 12-char display prefix, different secret body.
	other := fakePlaintext[:PrefixDisplayLen] + "bbbbbbbbbbbbbbbb"
	if _, ok := c.Get(other); ok {
		t.Error("Get on different plaintext must not hit")
	}
}

func TestVerifyCache_TTLExpiryDropsEntry(t *testing.T) {
	c, err := newVerifyCache(VerifyCacheConfig{MaxEntries: 8, TTL: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("newVerifyCache: %v", err)
	}
	c.Put(fakePlaintext, freshEntry(t))
	if c.Len() != 1 {
		t.Fatalf("Len after Put = %d, want 1", c.Len())
	}

	time.Sleep(30 * time.Millisecond) // exceed TTL
	if _, ok := c.Get(fakePlaintext); ok {
		t.Error("Get after TTL should miss")
	}
	if c.Len() != 0 {
		t.Errorf("Get must proactively evict expired entries; Len = %d, want 0", c.Len())
	}
}

// PAT expires_at must be respected independently of cache TTL — if a
// token expires during its cached window we must drop it on next Get.
func TestVerifyCache_PATExpiryWins(t *testing.T) {
	c, err := newVerifyCache(VerifyCacheConfig{MaxEntries: 8, TTL: time.Hour})
	if err != nil {
		t.Fatalf("newVerifyCache: %v", err)
	}
	e := freshEntry(t)
	e.PATExpiresAt = time.Now().Add(-time.Second) // already expired
	c.Put(fakePlaintext, e)
	if _, ok := c.Get(fakePlaintext); ok {
		t.Error("Get must miss when PAT is past expires_at")
	}
}

func TestVerifyCache_InvalidateByPATIDDropsMatching(t *testing.T) {
	c, err := newVerifyCache(VerifyCacheConfig{MaxEntries: 8, TTL: time.Minute})
	if err != nil {
		t.Fatalf("newVerifyCache: %v", err)
	}

	other := freshEntry(t)
	other.PATRecordID = "rec_other_pat"

	c.Put(fakePlaintext, freshEntry(t))
	c.Put("qat_zzzzzzzzzzzzzzzzzzzzzzzzzzzz", other)

	c.InvalidateByPATID(fakePATID)

	if _, ok := c.Get(fakePlaintext); ok {
		t.Error("entry for invalidated PAT id must be gone")
	}
	if _, ok := c.Get("qat_zzzzzzzzzzzzzzzzzzzzzzzzzzzz"); !ok {
		t.Error("entries for other PAT ids must remain")
	}
}

func TestVerifyCache_DisabledByNegativeTTL(t *testing.T) {
	c, err := newVerifyCache(VerifyCacheConfig{MaxEntries: 8, TTL: -1})
	if err != nil {
		t.Fatalf("newVerifyCache: %v", err)
	}
	c.Put(fakePlaintext, freshEntry(t))
	if _, ok := c.Get(fakePlaintext); ok {
		t.Error("Get on disabled cache must always miss")
	}
	if c.Len() != 0 {
		t.Errorf("disabled cache Len = %d, want 0", c.Len())
	}
}

func TestVerifyCache_NilSafeOnAllMethods(t *testing.T) {
	var c *verifyCache
	// All methods must tolerate nil receiver — useful for tests that
	// construct partial fake stores.
	if _, ok := c.Get(fakePlaintext); ok {
		t.Error("Get on nil cache must miss")
	}
	c.Put(fakePlaintext, freshEntry(t)) // must not panic
	c.InvalidateByPATID(fakePATID)      // must not panic
	c.Purge()                           // must not panic
	if c.Len() != 0 {
		t.Errorf("nil cache Len = %d, want 0", c.Len())
	}
}

func TestVerifyCache_LRUEvictionWhenFull(t *testing.T) {
	c, err := newVerifyCache(VerifyCacheConfig{MaxEntries: 2, TTL: time.Minute})
	if err != nil {
		t.Fatalf("newVerifyCache: %v", err)
	}
	c.Put("qat_aaaaaaaaaaaaaaaaaaaaaaaaaaaa", freshEntry(t))
	c.Put("qat_bbbbbbbbbbbbbbbbbbbbbbbbbbbb", freshEntry(t))
	c.Put("qat_cccccccccccccccccccccccccccc", freshEntry(t)) // evicts the oldest

	if c.Len() != 2 {
		t.Errorf("Len = %d, want 2 (LRU cap)", c.Len())
	}
	if _, ok := c.Get("qat_aaaaaaaaaaaaaaaaaaaaaaaaaaaa"); ok {
		t.Error("LRU should have evicted the oldest entry")
	}
}

func TestHexKey_Deterministic(t *testing.T) {
	a := hexKey(fakePlaintext)
	b := hexKey(fakePlaintext)
	if a != b {
		t.Errorf("hexKey not deterministic: %q vs %q", a, b)
	}
	if len(a) != 64 {
		t.Errorf("hexKey length = %d, want 64", len(a))
	}
}
