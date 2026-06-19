package paperassets

// doi.go adds DOI as a SECOND first-class asset identity alongside arXiv.
//
// Motivation: a contributor may hold the *published* version of a paper
// (e.g. the Physical Review / Nature PDF) which is a different artifact
// from any arXiv preprint — and many published papers have no arXiv
// presence at all. arXiv-keyed storage (StorageKey / AssetKey, sharded
// by YYMM) cannot address these, so DOI-indexed assets get their own
// namespace.
//
// Layout (never collides with arXiv's "<kind>/<yymm>/..." shards):
//
//	<kind>/doi/<registrant>/<safe-suffix>.<ext>
//	e.g. pdf/doi/10.1103/physrevlett.123.070501.pdf
//
// The DOI is the sole, unique index for these assets (matching the
// "one extra index, still unique" requirement). DOIs are case-
// insensitive per the DOI Handbook, so we lower-case before keying.

import (
	"regexp"
	"strings"
)

// MaxDOILen caps DOI input length before validation, guarding path
// building and header construction against pathological input. Mirrors
// openalex.DefaultMaxDOILen so both layers agree on the bound.
const MaxDOILen = 256

// doiShapeRE recognizes a bare DOI: "10.<registrant>/<suffix>" where the
// suffix is any run of non-space characters. Intentionally permissive on
// the suffix (the DOI grammar is essentially "any URL-safe string");
// ValidateDOI layers on the length + control-char checks.
var doiShapeRE = regexp.MustCompile(`^10\.\d{4,9}/\S+$`)

// DOIURLPrefixes are the scheme/host prefixes contributors commonly paste
// in front of a bare DOI. Stripped (longest-first within each form) by
// NormalizeDOI so "https://doi.org/10.x/y" and "doi:10.x/y" both
// normalize to "10.x/y".
//
// This is the canonical DOI URL prefix list for the whole codebase.
// All other call sites that need to detect or strip a DOI URL prefix
// (internal/openalex/lookup.go, internal/openalex/parse.go,
// internal/routes/papers.go) import this slice — there are no other
// inline copies. If you add a new prefix (e.g. "hdl:"), add it here
// and every consumer picks it up automatically.
var DOIURLPrefixes = []string{
	"https://doi.org/",
	"http://doi.org/",
	"https://dx.doi.org/",
	"http://dx.doi.org/",
	"doi.org/",
	"dx.doi.org/",
	"doi:",
}

// NormalizeDOI lower-cases, trims whitespace, and strips a leading
// doi.org / dx.doi.org URL prefix or "doi:" scheme, returning the bare
// DOI. It does NOT percent-decode — callers pass already-decoded path
// segments. The result is not guaranteed valid; pair with ValidateDOI.
func NormalizeDOI(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	for _, p := range DOIURLPrefixes {
		if strings.HasPrefix(v, p) {
			v = strings.TrimPrefix(v, p)
			break
		}
	}
	return strings.TrimSpace(v)
}

// ValidateDOI normalizes input and reports whether it is a syntactically
// valid DOI. Returns the normalized bare DOI and true on success, or
// ("", false) for invalid input (the caller emits the 400). Rejects
// over-length input, control characters, and non-ASCII bytes that could
// break path / header construction.
func ValidateDOI(v string) (string, bool) {
	norm := NormalizeDOI(v)
	if norm == "" || len(norm) > MaxDOILen {
		return "", false
	}
	if !doiShapeRE.MatchString(norm) {
		return "", false
	}
	// Reject DOIs whose suffix contains a literal "__": the storage
	// stem encoding in DOISafeStem maps "/" → "__", so suffixes that
	// already carry "__" cannot round-trip through DOIDecodeStem and
	// would manifest the same phantom-node bug that PR #19 review-2
	// (commit 5b84111) fixed for nested-slash DOIs. Concretely, the
	// suffix "foo__bar" would store as "foo__bar" and decode back to
	// "foo/bar", desynchronising the storage key from the node key
	// the upsert path actually wrote. Rejecting upfront is safer than
	// silently corrupting the catalog graph; the registrant
	// ("10.\d{4,9}") cannot contain "__" per doiShapeRE so we only
	// guard the suffix.
	if i := strings.IndexByte(norm, '/'); i >= 0 && strings.Contains(norm[i+1:], "__") {
		return "", false
	}
	for _, r := range norm {
		if r < 0x20 || r == 0x7f {
			return "", false
		}
		if r > 0x7f {
			// Real DOIs per the DOI Handbook are ASCII; non-ASCII
			// runelets (U+00AD soft-hyphen, U+FEFF BOM, etc.) pass
			// the control-char check but risk URL building + header
			// injection.
			return "", false
		}
	}
	return norm, true
}

// IsDOI reports whether v normalizes to a valid DOI. Convenience wrapper
// over ValidateDOI for call sites that only need the boolean.
func IsDOI(v string) bool {
	_, ok := ValidateDOI(v)
	return ok
}

// doiRegistrant returns the "10.<registrant>" prefix (the part before the
// first slash). doi is assumed already normalized + validated.
func doiRegistrant(doi string) string {
	if i := strings.IndexByte(doi, '/'); i >= 0 {
		return doi[:i]
	}
	return doi
}

// DOISafeStem turns a normalized DOI's suffix into a filesystem-safe
// stem by replacing any "/" with "__" (DOI suffixes can contain nested
// slashes, e.g. "10.1234/foo/bar"). The registrant is dropped — it
// becomes the parent directory in DOIAssetKey.
func DOISafeStem(doi string) string {
	i := strings.IndexByte(doi, '/')
	if i < 0 {
		return strings.ReplaceAll(doi, "/", "__")
	}
	return strings.ReplaceAll(doi[i+1:], "/", "__")
}

// DOIDecodeStem is the inverse of DOISafeStem: restores any "__"
// in a stored stem back to the "/" the DOI originally carried. Used
// by sync's reverse path (storage-key → node-key) so a nested-slash
// DOI like "10.1234/foo/bar" round-trips through:
//
//	DOIAssetKey  : (10.1234, foo/bar)   → "<kind>/doi/10.1234/foo__bar.<ext>"
//	storage list :                       returns parts[3] = "foo__bar.<ext>"
//	strip ext    :                       "foo__bar"
//	DOIDecodeStem:                       "foo/bar"
//	parts[2] + "/" + decoded = "10.1234/foo/bar" → DOINodeKey matches
//
// Without this inverse, sync built synthetic node keys with the "__"
// still in place ("doi:10.1234/foo__bar"), which never matched any
// :PaperWork node UpsertMDByDOI ever wrote ("doi:10.1234/foo/bar"),
// so every sync run created a phantom node per nested-slash DOI.
// safe is the post-extension-strip stem (parts[3] without path.Ext).
func DOIDecodeStem(safe string) string {
	return strings.ReplaceAll(safe, "__", "/")
}

// DOIAssetKey returns the canonical forward-slash object key for a
// DOI-indexed asset of kind "pdf" | "markdown" | "json" | "images":
//
//	<kind>/doi/<registrant>/<safe-suffix>.<ext>
//	pdf/doi/10.1103/physrevlett.123.070501.pdf
//
// Returns "" for an invalid DOI or unknown kind. The "doi/" segment
// keeps these objects in a namespace disjoint from arXiv's numeric YYMM
// shards, so an arXiv id and a DOI can never resolve to the same key.
func DOIAssetKey(kind, doi string) string {
	ext := assetExt(kind)
	if ext == "" {
		return ""
	}
	norm, ok := ValidateDOI(doi)
	if !ok {
		return ""
	}
	return kind + "/doi/" + doiRegistrant(norm) + "/" + DOISafeStem(norm) + ext
}
