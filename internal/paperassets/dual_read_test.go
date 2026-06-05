package paperassets_test

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// TestLocateAsset_NewLayoutHit: when an object lives at the post-A1
// per-category path, LocateAsset finds it without probing the legacy
// path and the dual-read counter doesn't move.
func TestLocateAsset_NewLayoutHit(t *testing.T) {
	// serial: tests below assert on the package-global legacy-hit counter
	ctx := context.Background()
	store := mustLocalStore(t)
	p := paperassets.MustParse("quant-ph/9508027v1")

	newKey := paperassets.AssetKeyFor("pdf", p)
	if newKey == "" {
		t.Fatalf("AssetKeyFor returned empty")
	}
	mustPut(t, ctx, store, newKey, []byte("%PDF-new-layout"))

	before := paperassets.LegacyLayoutReads()["pdf"]
	key, _, exists, err := paperassets.LocateAsset(ctx, store, "pdf", p)
	if err != nil || !exists {
		t.Fatalf("LocateAsset(new layout hit): exists=%v err=%v", exists, err)
	}
	if key != newKey {
		t.Errorf("key = %q, want %q (new layout)", key, newKey)
	}
	if got := paperassets.LegacyLayoutReads()["pdf"]; got != before {
		t.Errorf("legacy counter incremented unexpectedly: %d -> %d", before, got)
	}
}

// TestLocateAsset_LegacyFallback: pre-A1 buckets store old-style ids
// without the category subdirectory; LocateAsset must transparently
// find them when the new layout is empty, and the counter must bump
// so operators can observe migration progress.
func TestLocateAsset_LegacyFallback(t *testing.T) {
	// serial: tests below assert on the package-global legacy-hit counter
	ctx := context.Background()
	store := mustLocalStore(t)
	p := paperassets.MustParse("quant-ph/9508027v1")

	legacyKey := paperassets.LegacyAssetKeyFor("pdf", p)
	if legacyKey == "" {
		t.Fatalf("LegacyAssetKeyFor returned empty for old-style canonical id")
	}
	mustPut(t, ctx, store, legacyKey, []byte("%PDF-legacy"))

	before := paperassets.LegacyLayoutReads()["pdf"]
	key, _, exists, err := paperassets.LocateAsset(ctx, store, "pdf", p)
	if err != nil || !exists {
		t.Fatalf("LocateAsset(legacy fallback): exists=%v err=%v", exists, err)
	}
	if key != legacyKey {
		t.Errorf("key = %q, want %q (legacy fallback)", key, legacyKey)
	}
	if got := paperassets.LegacyLayoutReads()["pdf"]; got != before+1 {
		t.Errorf("legacy counter: got %d, want %d", got, before+1)
	}
}

// TestLocateAsset_NewLayoutWinsOverLegacy: when BOTH layouts have the
// object (which happens during the migration window when copy-phase
// has run but cleanup hasn't), the new layout MUST win so reads see
// the post-migration state.
func TestLocateAsset_NewLayoutWinsOverLegacy(t *testing.T) {
	// serial: tests below assert on the package-global legacy-hit counter
	ctx := context.Background()
	store := mustLocalStore(t)
	p := paperassets.MustParse("quant-ph/9508027v1")

	mustPut(t, ctx, store, paperassets.AssetKeyFor("pdf", p), []byte("%PDF-new"))
	mustPut(t, ctx, store, paperassets.LegacyAssetKeyFor("pdf", p), []byte("%PDF-legacy"))

	before := paperassets.LegacyLayoutReads()["pdf"]
	key, _, exists, err := paperassets.LocateAsset(ctx, store, "pdf", p)
	if err != nil || !exists {
		t.Fatalf("LocateAsset(both present): exists=%v err=%v", exists, err)
	}
	if key != paperassets.AssetKeyFor("pdf", p) {
		t.Errorf("key = %q, want new layout %q", key, paperassets.AssetKeyFor("pdf", p))
	}
	if got := paperassets.LegacyLayoutReads()["pdf"]; got != before {
		t.Errorf("legacy counter incremented despite new-layout hit: %d -> %d", before, got)
	}
}

// TestLocateAsset_AbsentInBoth: clean miss returns exists=false, err=nil.
func TestLocateAsset_AbsentInBoth(t *testing.T) {
	// serial: tests below assert on the package-global legacy-hit counter
	ctx := context.Background()
	store := mustLocalStore(t)
	p := paperassets.MustParse("quant-ph/9999999v1")

	key, _, exists, err := paperassets.LocateAsset(ctx, store, "pdf", p)
	if err != nil {
		t.Fatalf("LocateAsset(absent): unexpected err: %v", err)
	}
	if exists {
		t.Fatalf("LocateAsset(absent): exists=true, want false")
	}
	if key == "" {
		t.Errorf("key should still surface the probed new-layout key on absence; got empty")
	}
}

// TestLocateAsset_NewStyleNoFallback: new-style ids have always lived
// at the same path, so LocateAsset must not waste a Stat probing a
// legacy variant.
func TestLocateAsset_NewStyleNoFallback(t *testing.T) {
	// serial: tests below assert on the package-global legacy-hit counter
	ctx := context.Background()
	store := mustLocalStore(t)
	p := paperassets.MustParse("2501.00010v1")

	mustPut(t, ctx, store, paperassets.AssetKeyFor("pdf", p), []byte("%PDF-new-style"))

	before := paperassets.LegacyLayoutReads()["pdf"]
	_, _, exists, err := paperassets.LocateAsset(ctx, store, "pdf", p)
	if err != nil || !exists {
		t.Fatalf("LocateAsset(new-style): exists=%v err=%v", exists, err)
	}
	if got := paperassets.LegacyLayoutReads()["pdf"]; got != before {
		t.Errorf("legacy counter must not bump for new-style ids: %d -> %d", before, got)
	}
}

// TestLocateAsset_BareNoFallback: bare old-style ids ARE the legacy
// form; there's no further fallback. The single probe is the new
// (= legacy) layout key.
func TestLocateAsset_BareNoFallback(t *testing.T) {
	// serial: tests below assert on the package-global legacy-hit counter
	ctx := context.Background()
	store := mustLocalStore(t)
	p := paperassets.MustParse("9508027v1")

	mustPut(t, ctx, store, paperassets.AssetKeyFor("pdf", p), []byte("%PDF-bare"))

	before := paperassets.LegacyLayoutReads()["pdf"]
	_, _, exists, err := paperassets.LocateAsset(ctx, store, "pdf", p)
	if err != nil || !exists {
		t.Fatalf("LocateAsset(bare): exists=%v err=%v", exists, err)
	}
	if got := paperassets.LegacyLayoutReads()["pdf"]; got != before {
		t.Errorf("legacy counter must not bump for bare ids: %d -> %d", before, got)
	}
}

// TestLocateAssetByID_InvalidID: malformed input cannot have a
// matching object — return absent without error or panic.
func TestLocateAssetByID_InvalidID(t *testing.T) {
	// serial: tests below assert on the package-global legacy-hit counter
	ctx := context.Background()
	store := mustLocalStore(t)
	_, _, exists, err := paperassets.LocateAssetByID(ctx, store, "pdf", "not an id")
	if err != nil {
		t.Errorf("LocateAssetByID(invalid): unexpected err: %v", err)
	}
	if exists {
		t.Errorf("LocateAssetByID(invalid): exists=true, want false")
	}
}

// --- helpers ---

func mustLocalStore(t *testing.T) objstore.Store {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "store")
	s, err := objstore.NewLocalStore(dir)
	if err != nil {
		t.Fatalf("NewLocalStore: %v", err)
	}
	return s
}

func mustPut(t *testing.T, ctx context.Context, store objstore.Store, key string, body []byte) {
	t.Helper()
	_, err := store.Put(ctx, key, bytes.NewReader(body), int64(len(body)), "application/octet-stream")
	if err != nil {
		t.Fatalf("store.Put(%q): %v", key, err)
	}
}
