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
	arxivPrefixRE  = regexp.MustCompile(`(?i)^arxiv:\s*`)
	arxivVersionRE = regexp.MustCompile(`(?i)v\d+$`)
	oldStyleIDRE   = regexp.MustCompile(`(?i)^\d{7}(?:v\d+)?$`)
	shardedKeyRE   = regexp.MustCompile(`^\d{4}`)

	// newStyleArxivRE / oldStyleArxivRE / oldStyleBareRE are kept for
	// ValidateUploadID's tighter contract (must include version
	// suffix). For general parsing prefer Parse()/MustParse() from
	// parse.go — those drive the structured ParsedArxivID model used
	// by the new per-category storage layout.
	newStyleArxivRE = regexp.MustCompile(`^\d{4}\.\d{4,6}v\d+$`)
	oldStyleArxivRE = regexp.MustCompile(
		`^[a-z][a-z\-]*(?:\.[A-Za-z][A-Za-z\-]*)?/\d{7}v\d+$`)
	// oldStyleBareRE matches an old-style identifier that already has
	// the subject prefix stripped — the form the catalog stored
	// pre-A1. Still accepted by ValidateUploadID during the deprecation
	// cycle (an upstream resolver should disambiguate bare → canonical
	// via Neo4j lookup; once that ships in A3 this can be removed).
	oldStyleBareRE = regexp.MustCompile(`^\d{7}v\d+$`)
)

// Asset upload size caps. Wire equivalents of atlas/server/routers/api.py:
//
//	_UPLOAD_MAX_PDF_BYTES, _UPLOAD_MAX_MARKDOWN_BYTES, _UPLOAD_MAX_METADATA_BYTES
const (
	MaxPDFBytes       = 100 * 1024 * 1024
	MaxMarkdownBytes  = 25 * 1024 * 1024
	MaxMineruZipBytes = 200 * 1024 * 1024 // ~200 MB upper bound for `qatlas upload mineru` zip payloads (markdown + images bundle)
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
// suffix). Returns empty string for unknown kinds OR malformed ids.
//
// Layout (post-A1):
//
//	new-style:           <kind>/<yymm>/<stem>.<ext>
//	old-style canonical: <kind>/<yymm>/<category>/<stem>.<ext>   (NEW)
//	old-style bare:      <kind>/<yymm>/<stem>.<ext>              (LEGACY, dual-read only)
//	unrecognized input:  best-effort flat fallback (no shard)
//
// Pre-A1 every old-style id rendered to the LEGACY layout (the
// category was dropped entirely). That was a latent data integrity bug
// — `0207065` exists as different papers in quant-ph / hep-th / math /
// gr-qc and they would silent-overwrite in the same bucket key. The
// new layout preserves category as a subdirectory so cross-category
// ids never collide.
//
// Internally delegates to AssetKeyFor after Parse; the structured API
// is preferred for new code that already has a ParsedArxivID in hand.
func AssetKey(kind, arxivID string) string {
	p, err := Parse(arxivID)
	if err == nil {
		return AssetKeyFor(kind, p)
	}
	// Unparseable input: fall back to a flat key so the LocalStore dev
	// path can still address arbitrary stems used by older tests /
	// migration probes. Production never hits this branch because
	// every handler runs the id through Parse/ValidateUploadID first.
	ext := assetExt(kind)
	if ext == "" {
		return ""
	}
	key := SafeKey(arxivID)
	if key == "" {
		return ""
	}
	return kind + "/" + key + ext
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

// (SharePathForAsset was removed in v0.9.0 along with the /share/{token}/
// surface; upload handlers no longer surface a share-URL fragment.)

// ValidateUploadID enforces the strict arXiv id format required for
// upload endpoints — version suffix is mandatory so the filename on
// disk unambiguously identifies the revision.
//
// Three forms are accepted:
//
//   - new-style: "2401.12345v1"        (post-2007 papers)
//   - old-style canonical: "quant-ph/9508027v1"
//   - old-style bare: "9508027v1"      (catalog form — subject prefix
//     was stripped at catalog ingest, so refusing this leaves every
//     pre-2007 paper permanently uncliamable via the needs-mineru queue)
//
// All three normalize to the same downstream artifacts: StorageKey
// strips subject prefixes regardless, and per-kind RustFS bucket keys
// use the bare form ("0207/0207065v3.pdf"). The arxiv.org fallback URL
// for old-style bare IDs is broken (arxiv requires the subject prefix),
// so callers should prefer RustFS presign URLs over arxiv URLs.
//
// Returns ("", false) for invalid input; the caller is responsible for
// emitting the appropriate 400 response.
func ValidateUploadID(arxivID string) (string, bool) {
	canonical := NormalizeIdentifier(arxivID)
	if newStyleArxivRE.MatchString(canonical) ||
		oldStyleArxivRE.MatchString(canonical) ||
		oldStyleBareRE.MatchString(canonical) {
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
