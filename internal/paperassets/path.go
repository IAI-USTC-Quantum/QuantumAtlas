// Package paperassets resolves on-disk paths for paper assets (PDF,
// markdown, JSON metadata, image directories) under RAW_DIR.
//
// It is a direct Go port of atlas/paper_assets.py; the layout, sharding
// rules, and arXiv id normalization MUST match the Python implementation
// exactly so a single RAW_DIR can be read/written by either server during
// the FastAPI -> Go transition.
package paperassets

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var (
	arxivPrefixRE   = regexp.MustCompile(`(?i)^arxiv:\s*`)
	arxivVersionRE  = regexp.MustCompile(`(?i)v\d+$`)
	oldStyleIDRE    = regexp.MustCompile(`(?i)^\d{7}(?:v\d+)?$`)
	shardedKeyRE    = regexp.MustCompile(`^\d{4}`)
	newStyleArxivRE = regexp.MustCompile(`^\d{4}\.\d{4,6}v\d+$`)
	oldStyleArxivRE = regexp.MustCompile(`^[a-z][a-z\-]*(?:\.[A-Z]{2})?/\d{7}v\d+$`)
)

// Asset upload size caps. Wire equivalents of atlas/server/routers/api.py:
//
//	_UPLOAD_MAX_PDF_BYTES, _UPLOAD_MAX_MARKDOWN_BYTES, _UPLOAD_MAX_METADATA_BYTES
const (
	MaxPDFBytes      = 100 * 1024 * 1024
	MaxMarkdownBytes = 25 * 1024 * 1024
	MaxMetadataBytes = 2 * 1024 * 1024
)

// NormalizeIdentifier strips a leading "arXiv:" prefix and trims
// whitespace, preserving the category and version exactly.
func NormalizeIdentifier(v string) string {
	return arxivPrefixRE.ReplaceAllString(strings.TrimSpace(v), "")
}

// StripVersion removes the trailing vN suffix, useful when grouping
// different versions of the same paper.
func StripVersion(v string) string {
	return arxivVersionRE.ReplaceAllString(NormalizeIdentifier(v), "")
}

// SafeKey returns a filesystem-safe key (replaces "/" with "__") for use
// in paths that don't tolerate slashes — old-style arXiv ids like
// "quant-ph/9508027" become "quant-ph__9508027".
func SafeKey(arxivID string) string {
	return strings.ReplaceAll(NormalizeIdentifier(arxivID), "/", "__")
}

// StorageKey returns the canonical raw-storage key for an arXiv id.
//
// Old-style ids (with category prefix) drop the category when the part
// after the slash is the pure 7-digit form, mirroring the historical
// RAW_DIR layout where "quant-ph/9508027v1" stores as "9508027v1.pdf".
func StorageKey(arxivID string) string {
	canonical := NormalizeIdentifier(arxivID)
	if strings.Contains(canonical, "/") {
		parts := strings.SplitN(canonical, "/", 2)
		categoryless := parts[1]
		if oldStyleIDRE.MatchString(categoryless) {
			return categoryless
		}
	}
	return SafeKey(canonical)
}

// Shard returns the four-digit shard prefix for a key, or empty string
// when the key doesn't start with four digits.
func Shard(key string) string {
	if shardedKeyRE.MatchString(key) {
		return key[:4]
	}
	return ""
}

// AssetPath returns the canonical sharded path for an asset of kind
// "pdf" | "markdown" | "json" | "images". Returns empty string for
// unknown kinds (callers must validate).
func AssetPath(rawRoot, kind, arxivID string) string {
	key := AssetKey(kind, arxivID)
	if key == "" {
		return ""
	}
	return filepath.Join(rawRoot, filepath.FromSlash(key))
}

// AssetKey returns the canonical forward-slash object key for an asset
// of kind "pdf" | "markdown" | "json" | "images". This is AssetPath
// without the RawDir prefix — i.e. the key suitable for direct use
// against the objstore.Store interface (S3 object name OR local path
// suffix). Returns empty string for unknown kinds.
//
// The exact string layout (with shard) matches the on-disk layout from
// the Python era so a single bucket / RawDir can be read by either
// implementation during the transition.
func AssetKey(kind, arxivID string) string {
	key := StorageKey(arxivID)
	shard := Shard(key)
	var dir string
	if shard != "" {
		dir = kind + "/" + shard
	} else {
		dir = kind
	}
	switch kind {
	case "pdf":
		return dir + "/" + key + ".pdf"
	case "markdown":
		return dir + "/" + key + ".md"
	case "json":
		return dir + "/" + key + ".json"
	case "images":
		return dir + "/" + key
	default:
		return ""
	}
}

// WikiSourcePageID returns the canonical wiki page id for a paper source
// page, e.g. "paper-arxiv-1112.3333" or "paper-arxiv-quant-ph-9508027".
func WikiSourcePageID(arxivID string) string {
	canonical := NormalizeIdentifier(arxivID)
	return "paper-arxiv-" + strings.ReplaceAll(canonical, "/", "-")
}

// candidateStems enumerates all the on-disk file stem variants we try
// when resolving an existing asset by arXiv id (with / without version,
// with / without category prefix, safe-key form, etc.). Mirrors the
// Python _candidate_stems helper.
func candidateStems(arxivID string) []string {
	canonical := NormalizeIdentifier(arxivID)
	base := StripVersion(canonical)
	stems := []string{
		StorageKey(canonical),
		canonical,
		SafeKey(canonical),
		base,
		SafeKey(base),
	}
	if strings.Contains(canonical, "/") {
		parts := strings.SplitN(canonical, "/", 2)
		categoryless := parts[1]
		categorylessBase := StripVersion(categoryless)
		stems = append(stems,
			categoryless,
			SafeKey(categoryless),
			categorylessBase,
			SafeKey(categorylessBase),
		)
	}
	seen := map[string]struct{}{}
	out := stems[:0]
	for _, s := range stems {
		if s == "" {
			continue
		}
		if _, dup := seen[s]; dup {
			continue
		}
		seen[s] = struct{}{}
		out = append(out, s)
	}
	return out
}

// ResolvedAssets is the result of ResolveAssets; empty paths mean
// "not present on disk".
type ResolvedAssets struct {
	ArxivID      string
	Key          string
	PDFPath      string
	MarkdownPath string
	JSONPath     string
	ImagesDir    string
}

// ResolveAssets walks RAW_DIR/{pdf,markdown,json,images} trying every
// candidate stem to find what's actually on disk for arxivID. Returns
// the best-known canonical paths (or empty strings for missing assets).
func ResolveAssets(rawRoot, arxivID string) ResolvedAssets {
	canonical := NormalizeIdentifier(arxivID)
	pdf := resolveFile(filepath.Join(rawRoot, "pdf"), canonical, "pdf")
	md := resolveFile(filepath.Join(rawRoot, "markdown"), canonical, "md")
	js := resolveFile(filepath.Join(rawRoot, "json"), canonical, "json")
	keySource := pdf
	if keySource == "" {
		keySource = md
	}
	if keySource == "" {
		keySource = js
	}
	var key string
	if keySource != "" {
		key = strings.TrimSuffix(filepath.Base(keySource), filepath.Ext(keySource))
	} else {
		key = StorageKey(canonical)
	}
	images := resolveDir(filepath.Join(rawRoot, "images"), key, canonical)
	return ResolvedAssets{
		ArxivID:      canonical,
		Key:          key,
		PDFPath:      pdf,
		MarkdownPath: md,
		JSONPath:     js,
		ImagesDir:    images,
	}
}

// resolveFile tries each candidate stem + shard combination, returns
// the first existing file path or empty string.
func resolveFile(dir, arxivID, suffix string) string {
	for _, stem := range candidateStems(arxivID) {
		for _, p := range candidateFilePaths(dir, stem, suffix) {
			if isRegularFile(p) {
				return p
			}
		}
	}
	// Versionless probing: if the unversioned stem matches exactly one
	// versioned file on disk, use it. Mirrors _versioned_file_matches.
	canonical := NormalizeIdentifier(arxivID)
	base := StripVersion(canonical)
	for _, stem := range candidateStems(base) {
		if m := versionedMatches(dir, stem, suffix); len(m) == 1 {
			return m[0]
		}
	}
	// Old-style suffix-only lookup (e.g. shared SafeKey collisions).
	if !strings.Contains(canonical, "/") && !strings.Contains(canonical, ".") {
		if m := globMatches(dir, "*__"+canonical+"."+suffix); len(m) == 1 {
			return m[0]
		}
		if m := globMatches(dir, "*__"+canonical+"v*."+suffix); len(m) == 1 {
			return m[0]
		}
	}
	return ""
}

// resolveDir is the directory counterpart of resolveFile for image dirs.
func resolveDir(dir, key, arxivID string) string {
	candidates := candidateDirPaths(dir, key)
	for _, stem := range candidateStems(arxivID) {
		candidates = append(candidates, candidateDirPaths(dir, stem)...)
	}
	seen := map[string]struct{}{}
	for _, c := range candidates {
		if _, dup := seen[c]; dup {
			continue
		}
		seen[c] = struct{}{}
		if isDir(c) {
			return c
		}
	}
	return ""
}

func candidateFilePaths(dir, stem, suffix string) []string {
	paths := []string{filepath.Join(dir, stem+"."+suffix)}
	if shard := Shard(stem); shard != "" {
		paths = append(paths, filepath.Join(dir, shard, stem+"."+suffix))
	}
	return paths
}

func candidateDirPaths(dir, stem string) []string {
	paths := []string{filepath.Join(dir, stem)}
	if shard := Shard(stem); shard != "" {
		paths = append(paths, filepath.Join(dir, shard, stem))
	}
	return paths
}

func versionedMatches(dir, stem, suffix string) []string {
	out := globMatches(dir, stem+"v*."+suffix)
	if shard := Shard(stem); shard != "" {
		out = append(out, globMatches(filepath.Join(dir, shard), stem+"v*."+suffix)...)
	}
	return out
}

func globMatches(dir, pattern string) []string {
	m, err := filepath.Glob(filepath.Join(dir, pattern))
	if err != nil {
		return nil
	}
	sort.Strings(m)
	return m
}

// SharePathForAsset returns the share-relative URL fragment for an
// asset under the public /share/{token}/ tree. Used by /api/papers/
// uploads to surface a copy-pastable URL on success.
//
// If assetPath is provided (preferred), the result preserves the
// shard subdirectory exactly. Otherwise we synthesize the path from key.
func SharePathForAsset(kind, key, filename, assetPath, rawRoot string) string {
	if assetPath != "" && rawRoot != "" {
		// Strip the rawRoot/kind/ prefix to get the share-relative tail.
		prefix := filepath.Join(rawRoot, kind) + string(filepath.Separator)
		if strings.HasPrefix(assetPath, prefix) {
			rel := filepath.ToSlash(assetPath[len(prefix):])
			return "papers/" + kind + "/" + rel
		}
	}
	var base string
	switch kind {
	case "pdf":
		base = "papers/pdf/" + key + ".pdf"
	case "markdown":
		base = "papers/markdown/" + key + ".md"
	case "json":
		base = "papers/json/" + key + ".json"
	case "images":
		base = "papers/images/" + key
	default:
		return ""
	}
	if filename != "" {
		return base + "/" + filename
	}
	return base
}

// ValidateUploadID enforces the strict arXiv id format required for
// upload endpoints — version suffix is mandatory so the filename on
// disk unambiguously identifies the revision.
//
// Returns ("", false) for invalid input; the caller is responsible for
// emitting the appropriate 400 response.
func ValidateUploadID(arxivID string) (string, bool) {
	canonical := NormalizeIdentifier(arxivID)
	if newStyleArxivRE.MatchString(canonical) || oldStyleArxivRE.MatchString(canonical) {
		return canonical, true
	}
	return "", false
}

// RelativeRawPath returns path with rawRoot stripped (POSIX separator),
// or the original path when not under rawRoot.
func RelativeRawPath(path, rawRoot string) string {
	rel, err := filepath.Rel(rawRoot, path)
	if err != nil || strings.HasPrefix(rel, "..") {
		return path
	}
	return filepath.ToSlash(rel)
}
