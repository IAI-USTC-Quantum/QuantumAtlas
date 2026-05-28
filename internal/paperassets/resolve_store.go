package paperassets

import (
	"context"
	"path"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/objstore"
)

// ResolveAssetsViaStore is the objstore-aware counterpart of
// ResolveAssets. It mirrors the same candidate-stem matching logic but
// drives I/O through the Store interface, so a single implementation
// works against both LocalStore and S3Store.
//
// Round-trip budget: at most ~8 ListPrefix calls per resolve (2 probes
// per kind × 4 kinds). Each probe uses a stem-level prefix so the
// returned listing is small (typically 0–3 objects per probe) — this
// matters on S3 where each ListObjectsV2 is one HTTP RTT and each byte
// shipped costs egress. The HeadObject-per-stem alternative would
// balloon to 40+ RTT per resolve.
func ResolveAssetsViaStore(ctx context.Context, store objstore.Store, arxivID string) ResolvedAssets {
	canonical := NormalizeIdentifier(arxivID)
	pdfKey := findAssetKey(ctx, store, "pdf", "pdf", canonical)
	mdKey := findAssetKey(ctx, store, "markdown", "md", canonical)
	jsKey := findAssetKey(ctx, store, "json", "json", canonical)

	// The "Key" field on ResolvedAssets is the bare stem (no extension,
	// no shard, no dir) derived from whichever asset we actually found.
	// Falls back to StorageKey(canonical) when nothing exists on the
	// store yet — keeps the legacy semantics from the local-fs version.
	keySource := pdfKey
	if keySource == "" {
		keySource = mdKey
	}
	if keySource == "" {
		keySource = jsKey
	}
	var key string
	if keySource != "" {
		base := path.Base(keySource)
		key = strings.TrimSuffix(base, path.Ext(base))
	} else {
		key = StorageKey(canonical)
	}

	imagesKey := findImagesKey(ctx, store, canonical, key)

	return ResolvedAssets{
		ArxivID:      canonical,
		Key:          key,
		PDFPath:      pdfKey,
		MarkdownPath: mdKey,
		JSONPath:     jsKey,
		ImagesDir:    imagesKey,
	}
}

// findAssetKey probes the store for a kind / suffix combination using
// stem-prefix listings, returning the first match against the candidate
// stem priority list. Empty string means "asset not present in store".
//
// The function performs three matching passes over the union of all
// listings, mirroring the original resolveFile algorithm:
//  1. exact-stem match  (<stem>.<suffix>)
//  2. versionless match (<stem>v*.<suffix>) when exactly one match
//  3. old-style suffix-only (*__<canonical>.<suffix>) when exactly one match
//
// "Exactly one" is load-bearing for passes (2) and (3): ambiguity
// produces no result rather than guessing — matches the Python helper.
func findAssetKey(ctx context.Context, store objstore.Store, kind, suffix, canonical string) string {
	base := StripVersion(canonical)

	// Build a set of stem-prefix probes spanning every candidate's
	// (dir, base-stem) pair so each unique location is listed only once.
	probes := computeProbes(kind, candidateStems(canonical))
	for _, p := range computeProbes(kind, candidateStems(base)) {
		probes = appendUnique(probes, p)
	}

	// Aggregate listings into a basename → key map. We dedupe on the
	// first occurrence so probe order influences which key wins for a
	// duplicate basename across shard / non-shard locations — the
	// candidate-stem ordering above ensures the "preferred" location
	// is probed first.
	found := map[string]string{}
	for _, probe := range probes {
		listed, err := store.ListPrefix(ctx, probe, 0)
		if err != nil {
			continue
		}
		for _, info := range listed {
			b := path.Base(info.Key)
			if _, dup := found[b]; !dup {
				found[b] = info.Key
			}
		}
	}

	// Pass 1: exact-stem match.
	for _, stem := range candidateStems(canonical) {
		if k, ok := found[stem+"."+suffix]; ok {
			return k
		}
	}

	// Pass 2: versionless probe. For each base-stem, look for files
	// like "<base>vN.<suffix>"; if exactly one matches the base, use it.
	for _, stem := range candidateStems(base) {
		var matches []string
		needle := stem + "v"
		for b, k := range found {
			if strings.HasPrefix(b, needle) && strings.HasSuffix(b, "."+suffix) {
				matches = append(matches, k)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
	}

	// Pass 3: old-style suffix-only collisions (e.g. SafeKey'd category
	// prefix stripped). Only kicks in for purely numeric / unprefixed
	// ids, matching the original Python guard.
	if !strings.Contains(canonical, "/") && !strings.Contains(canonical, ".") {
		var matches []string
		for b, k := range found {
			if strings.HasSuffix(b, "__"+canonical+"."+suffix) {
				matches = append(matches, k)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
		// Versioned variant: *__<canonical>v*.<suffix>
		matches = matches[:0]
		for b, k := range found {
			if !strings.HasSuffix(b, "."+suffix) {
				continue
			}
			rest := strings.TrimSuffix(b, "."+suffix)
			i := strings.Index(rest, "__"+canonical+"v")
			if i >= 0 && i+len("__"+canonical+"v") < len(rest) {
				matches = append(matches, k)
			}
		}
		if len(matches) == 1 {
			return matches[0]
		}
	}

	return ""
}

// findImagesKey locates the images "directory" object key for a paper.
// In S3 there are no directories, only prefixes — so the return value
// is a key prefix (no trailing slash, e.g. "images/24/2401.00001v1")
// suitable for ListPrefix-style enumeration of the contained images.
// LocalStore treats the same value as a real directory path.
//
// Existence is established via ListPrefix("<prefix>/", limit=1): a
// match means at least one image lives under that prefix, which is the
// closest analogue to os.Stat's IsDir on the local backend.
func findImagesKey(ctx context.Context, store objstore.Store, canonical, key string) string {
	candidates := []string{}
	if shard := Shard(key); shard != "" {
		candidates = append(candidates, "images/"+shard+"/"+key)
	}
	candidates = append(candidates, "images/"+key)
	for _, stem := range candidateStems(canonical) {
		if shard := Shard(stem); shard != "" {
			candidates = append(candidates, "images/"+shard+"/"+stem)
		}
		candidates = append(candidates, "images/"+stem)
	}
	seen := map[string]struct{}{}
	for _, c := range candidates {
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		listed, err := store.ListPrefix(ctx, c+"/", 1)
		if err != nil {
			continue
		}
		if len(listed) > 0 {
			return c
		}
	}
	return ""
}

// computeProbes turns a slice of candidate stems into a deduplicated
// list of "<kind>/<maybe-shard>/<base>" prefix strings suitable for
// ListPrefix probing. The base is the versionless stem so a single
// probe per stem catches every version.
func computeProbes(kind string, stems []string) []string {
	var out []string
	for _, stem := range stems {
		if stem == "" {
			continue
		}
		base := StripVersion(stem)
		if base == "" {
			continue
		}
		var prefix string
		if shard := Shard(base); shard != "" {
			prefix = kind + "/" + shard + "/" + base
		} else {
			prefix = kind + "/" + base
		}
		out = appendUnique(out, prefix)
	}
	return out
}

// appendUnique appends s to out only if it's not already present. O(n²)
// but the slices are tiny (≤8 entries in practice).
func appendUnique(out []string, s string) []string {
	for _, v := range out {
		if v == s {
			return out
		}
	}
	return append(out, s)
}

// ShareRelPathForKey converts an object key returned by AssetKey or
// findAssetKey into the share-relative URL fragment used under
// /share/{token}/. The conversion is simply "papers/" prepended —
// share-fragment scheme: "papers/<kind>/<shard>/<stem>.<ext>".
//
// Callers that need to derive a share path from a full local fs path
// should keep using SharePathForAsset; this helper exists for the
// objstore code path where keys are already in the right shape.
func ShareRelPathForKey(key string) string {
	if key == "" {
		return ""
	}
	return "papers/" + strings.TrimPrefix(key, "/")
}

// ObjectKeyFromSharePath inverts ShareRelPathForKey. Returns ("", false)
// when relPath doesn't begin with one of the recognised "papers/<kind>/"
// prefixes — same set the original ResolveTarget enforced.
func ObjectKeyFromSharePath(relPath string) (string, bool) {
	rel := strings.Trim(relPath, "/")
	for _, kind := range []string{"pdf", "markdown", "json", "images"} {
		prefix := "papers/" + kind
		if rel == prefix {
			return kind, true
		}
		if strings.HasPrefix(rel, prefix+"/") {
			suffix := strings.TrimPrefix(rel[len(prefix):], "/")
			return kind + "/" + suffix, true
		}
	}
	return "", false
}
