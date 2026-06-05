// Dual-read fallback for the A1 storage-layout migration.
//
// Pre-A1, old-style arXiv papers were stored bare-stem
// (`pdf/0207/0207065v1.pdf`). Post-A1 the new canonical layout is
// per-category (`pdf/0207/quant-ph/0207065v1.pdf`). The migration that
// physically moves objects from the legacy location to the new one is
// a separate operator step (cmd/qatlasd storage migrate-category-prefix
// in plan §4 Phase E), so until it has run for a given bucket the
// catalog has rows whose pdf_path points at the bare location while
// AssetKeyFor now produces the per-category location.
//
// LocateAsset bridges the two by probing the new layout first and
// falling back to the legacy bucket key on (exists=false, err=nil)
// only when the id is an old-style canonical form (where a meaningful
// legacy variant exists). New-style ids and bare old-style ids never
// have a legacy variant — for them LocateAsset reduces to a single
// store.Stat.
//
// Use LocateAsset on every READ path that might surface a pre-A1
// object: markdown cache hits, PDF existence checks, PDF presign URL
// signing, sha256 metadata lookups. WRITE paths must use AssetKeyFor
// directly — every new write goes to the new layout unconditionally,
// and the migration tooling is responsible for moving the legacy
// objects forward. Mixing dual-read into the write path would silently
// re-create the per-category collision bug A1 was designed to fix.
//
// Once migration completes for every active bucket the legacy probe
// becomes dead code on the hot path; it stays for safety until removed
// in a subsequent release.

package paperassets

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// legacyLayoutReadsByKind counts dual-read fallback hits per kind so an
// operator (or healthz line) can observe progress of the migration —
// non-zero means there are still pre-A1 objects in the bucket.
var legacyLayoutReadsByKind = map[string]*atomic.Uint64{
	"pdf":      {},
	"markdown": {},
	"json":     {},
	"images":   {},
}

// LegacyLayoutReads returns a snapshot of dual-read fallback counters,
// one entry per kind. Intended for health endpoints and tests.
func LegacyLayoutReads() map[string]uint64 {
	out := make(map[string]uint64, len(legacyLayoutReadsByKind))
	for k, v := range legacyLayoutReadsByKind {
		out[k] = v.Load()
	}
	return out
}

// recordLegacyHit increments the per-kind counter and logs a Debug
// line (Info would be too noisy on a half-migrated bucket).
func recordLegacyHit(kind, legacyKey, newKey string) {
	if c, ok := legacyLayoutReadsByKind[kind]; ok {
		c.Add(1)
	}
	slog.Debug("paperassets: dual-read served legacy layout key",
		"kind", kind, "legacy_key", legacyKey, "new_key", newKey)
}

// LocateAsset finds the existing object key for an asset, probing the
// post-A1 layout first and falling back to the pre-A1 (legacy) bare
// layout for old-style canonical ids when the new key is absent.
//
// Returns:
//   - key: the bucket key where the object was found (new or legacy);
//     equals AssetKeyFor(kind, p) when the new layout had it, equals
//     LegacyAssetKeyFor(kind, p) when the legacy fallback fired. Empty
//     string when exists is false.
//   - info: the ObjectInfo for the found key (zero value when absent).
//   - exists: true iff the object was found in either layout.
//   - err: only non-nil when Stat itself fails (network / permission).
//     Absence is signalled via exists=false, err=nil — same contract
//     as objstore.Store.Stat.
//
// New-style ids and bare old-style ids skip the fallback entirely
// (LegacyAssetKeyFor returns "" for them, so there's nothing to probe).
func LocateAsset(ctx context.Context, store objstore.Store, kind string, p ParsedArxivID) (key string, info objstore.ObjectInfo, exists bool, err error) {
	newKey := AssetKeyFor(kind, p)
	if newKey == "" {
		return "", objstore.ObjectInfo{}, false, nil
	}
	info, exists, err = store.Stat(ctx, newKey)
	if err != nil {
		return newKey, info, exists, err
	}
	if exists {
		return newKey, info, true, nil
	}
	legacy := LegacyAssetKeyFor(kind, p)
	if legacy == "" {
		return newKey, info, false, nil
	}
	info2, exists2, err2 := store.Stat(ctx, legacy)
	if err2 != nil {
		// New-layout probe succeeded with absent; legacy probe failed
		// with a transport error. Surface the legacy error so the
		// caller doesn't silently treat the asset as absent when the
		// store is actually misbehaving.
		return legacy, info2, false, err2
	}
	if exists2 {
		recordLegacyHit(kind, legacy, newKey)
		return legacy, info2, true, nil
	}
	// Absent in both layouts.
	return newKey, objstore.ObjectInfo{}, false, nil
}

// LocateAssetByID is the string-in convenience wrapper around
// LocateAsset for call sites that don't already hold a ParsedArxivID.
// Returns exists=false, err=nil on unparseable input (the asset cannot
// exist if the id is malformed).
func LocateAssetByID(ctx context.Context, store objstore.Store, kind, arxivID string) (key string, info objstore.ObjectInfo, exists bool, err error) {
	p, perr := Parse(arxivID)
	if perr != nil {
		return "", objstore.ObjectInfo{}, false, nil
	}
	return LocateAsset(ctx, store, kind, p)
}
