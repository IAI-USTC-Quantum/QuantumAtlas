// Structured parser for arXiv identifiers.
//
// Pre-A1 the package only had loose helpers (NormalizeIdentifier,
// StripVersion, SafeKey, StorageKey, …) that took strings in and out;
// every call site re-derived the same fields (category, version, yymm)
// with subtly different regexes. That made it hard to introduce the
// new object-key layout (`<kind>/<yymm>/<category>/<stem>.<ext>` for
// old-style ids) without a flood of one-off string surgery in handlers.
//
// Parse turns an identifier into a single ParsedArxivID value carrying
// every field downstream code needs, so the layout decision lives in
// exactly one place (AssetKeyFor) and call sites stay declarative.
//
// Coverage of arXiv category formats:
//   - new-style:           "2501.00010v1"            → IsOldStyle=false
//   - old-style canonical: "quant-ph/9508027v1"      → IsOldStyle=true, Category="quant-ph"
//   - old-style bare:      "9508027v1"               → IsOldStyle=true, IsBare=true
//
// Old-style categories accepted include the full archive set with
// subcategories of either upper-case form (cs.AI, q-bio.NC, math.AG)
// or hyphenated lower-case form (physics.atom-ph, cond-mat.stat-mech,
// nlin.CD); see oldStyleCategoryRE for the exact grammar.

package paperassets

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// ParsedArxivID is the decomposed canonical form of an arXiv identifier.
//
// Zero value is invalid; callers must check the error returned from Parse.
type ParsedArxivID struct {
	// Raw is the input with `arXiv:` prefix stripped and surrounding
	// whitespace trimmed, but no other transformation. Preserved for
	// log lines so the original caller intent is visible.
	Raw string

	// Canonical is the best canonical render of this identifier:
	//   - new-style:           "2501.00010v1"
	//   - old-style w/ cat:    "quant-ph/9508027v1"
	//   - old-style bare:      "9508027v1"   (subject category unknown)
	//
	// Equal to Stem when the identifier has no category prefix.
	Canonical string

	// Category is the old-style subject classification ("quant-ph",
	// "cs.AI", "physics.atom-ph", …). Empty for new-style ids and for
	// bare old-style ids (where the category is unknown).
	Category string

	// Stem is the numeric body + version, without category prefix:
	//   - new-style: "2501.00010v1"
	//   - old-style: "9508027v1"
	//
	// This is the per-kind bucket filename stem; the on-disk file is
	// always `<Stem>.<ext>`.
	Stem string

	// StemBase is Stem without the version suffix. Useful when
	// grouping versions of the same paper.
	StemBase string

	// Version is the trailing "v<N>" suffix (lower-case), e.g. "v1",
	// "v12". Empty when the input had no version. Most write-path
	// callers require a non-empty Version (see ValidateUploadID); read
	// paths may resolve "latest" when Version is empty.
	Version string

	// YYMM is the 4-digit year-month shard prefix, e.g. "9508", "2501".
	// Always non-empty for valid arXiv ids; used for object-store
	// sharding so a single shard never grows above ~10k objects.
	YYMM string

	// IsOldStyle is true for pre-April-2007 ids (whether or not the
	// subject category was supplied). New-style ids set this false.
	IsOldStyle bool

	// IsBare is true when an old-style id was supplied without its
	// subject category — i.e. just the 7-digit numeric body. Bare ids
	// are AMBIGUOUS in the general case (e.g. "0207065" exists as
	// quant-ph, hep-th, math, gr-qc all with different content) so
	// callers that need to write to per-category storage layouts must
	// refuse IsBare inputs (or resolve them through a catalog lookup
	// upstream). Always false for new-style ids.
	IsBare bool
}

// IsValid reports whether the parsed value was populated successfully.
// A zero ParsedArxivID returns false; values produced by a successful
// Parse return true.
func (p ParsedArxivID) IsValid() bool {
	return p.Stem != "" && p.YYMM != ""
}

// String returns the canonical form, suitable for logging / display.
// Equivalent to .Canonical.
func (p ParsedArxivID) String() string {
	return p.Canonical
}

// oldStyleCategoryRE is the grammar for old-style subject categories.
// Two flavours of subcategory are accepted:
//   - upper-case 2-letter (cs.AI, q-bio.NC, math.AG)
//   - lower-case with hyphens (physics.atom-ph, cond-mat.stat-mech)
//
// The pre-A1 regex `\.[A-Z]{2}` only covered the first form and
// rejected real ids like `physics.atom-ph/0001001v1`.
var oldStyleCategoryRE = `[a-z][a-z\-]*(?:\.[A-Za-z][A-Za-z\-]*)?`

var (
	// newStyleStemRE matches a post-April-2007 stem.
	//
	// Groups: 1=YYMM, 2=sequence, 3=optional version.
	newStyleStemRE = regexp.MustCompile(`^(\d{4})\.(\d{4,6})(v\d+)?$`)

	// oldStyleStemRE matches the 7-digit body (without category).
	//
	// Groups: 1=YYMM, 2=sequence, 3=optional version.
	oldStyleStemRE = regexp.MustCompile(`^(\d{4})(\d{3})(v\d+)?$`)

	// oldStyleFullRE matches a canonical old-style id with category.
	//
	// Groups: 1=category, 2=YYMM, 3=sequence, 4=optional version.
	oldStyleFullRE = regexp.MustCompile(
		`^(` + oldStyleCategoryRE + `)/(\d{4})(\d{3})(v\d+)?$`)

	// versionSuffixLowerRE is used to lower-case a trailing "V<n>"
	// from sloppy callers before matching. arXiv always uses lower-case
	// "v"; the existing helpers (arxivVersionRE) accept either case for
	// back-compat, and Parse preserves that intent without making the
	// category-bearing regexes case-insensitive (which would
	// canonicalize "QUANT-PH" wrong).
	versionSuffixLowerRE = regexp.MustCompile(`V(\d+)$`)
)

// ErrInvalidArxivID is returned by Parse for inputs that don't match
// any recognized arXiv id grammar.
var ErrInvalidArxivID = errors.New("not a recognized arxiv id")

// Parse turns an arXiv identifier string into a ParsedArxivID,
// recognizing all three accepted forms (new-style, old-style canonical,
// old-style bare).
//
// Input is normalized first (NormalizeIdentifier semantics: trim
// whitespace, strip `arXiv:` prefix case-insensitively). The version
// suffix is OPTIONAL at this layer; callers that require it (upload /
// claim handlers) should call ValidateUploadID instead.
//
// Returns the zero value + ErrInvalidArxivID-wrapping error on
// unrecognized input.
func Parse(input string) (ParsedArxivID, error) {
	raw := NormalizeIdentifier(input)
	if raw == "" {
		return ParsedArxivID{}, fmt.Errorf("%w: empty input", ErrInvalidArxivID)
	}
	// arXiv canonical version suffix is lower-case "v"; tolerate
	// upper-case "V" from sloppy callers (consistent with the legacy
	// arxivVersionRE which was case-insensitive). Done at the input
	// level so the category-bearing regexes can stay strict — making
	// THEM case-insensitive would mis-canonicalize "QUANT-PH" etc.
	raw = versionSuffixLowerRE.ReplaceAllString(raw, "v$1")

	p := ParsedArxivID{Raw: raw}

	// 1) new-style: 2501.00010v1
	if m := newStyleStemRE.FindStringSubmatch(raw); m != nil {
		p.YYMM = m[1]
		p.StemBase = m[1] + "." + m[2]
		p.Version = strings.ToLower(m[3])
		p.Stem = p.StemBase + p.Version
		p.Canonical = p.Stem
		return p, nil
	}

	// 2) old-style canonical: quant-ph/9508027v1
	if m := oldStyleFullRE.FindStringSubmatch(raw); m != nil {
		p.IsOldStyle = true
		p.Category = m[1]
		p.YYMM = m[2]
		p.StemBase = m[2] + m[3]
		p.Version = strings.ToLower(m[4])
		p.Stem = p.StemBase + p.Version
		p.Canonical = p.Category + "/" + p.Stem
		return p, nil
	}

	// 3) old-style bare: 9508027v1
	if m := oldStyleStemRE.FindStringSubmatch(raw); m != nil {
		p.IsOldStyle = true
		p.IsBare = true
		p.YYMM = m[1]
		p.StemBase = m[1] + m[2]
		p.Version = strings.ToLower(m[3])
		p.Stem = p.StemBase + p.Version
		p.Canonical = p.Stem
		return p, nil
	}

	return ParsedArxivID{}, fmt.Errorf("%w: %q", ErrInvalidArxivID, input)
}

// MustParse returns Parse's value or panics on error. Intended for
// test fixtures and package-level constants; never use in production
// code paths.
func MustParse(input string) ParsedArxivID {
	p, err := Parse(input)
	if err != nil {
		panic(err)
	}
	return p
}

// AssetKeyFor returns the canonical forward-slash object key for an
// asset of kind "pdf" | "markdown" | "json" | "images" given a parsed
// arXiv id.
//
// Layout matrix:
//
//	new-style:           <kind>/<yymm>/<stem>.<ext>
//	                     e.g. "pdf/2501/2501.00010v1.pdf"
//	old-style canonical: <kind>/<yymm>/<category>/<stem>.<ext>
//	                     e.g. "pdf/9508/quant-ph/9508027v1.pdf"
//	old-style bare:      <kind>/<yymm>/<stem>.<ext>     [LEGACY]
//	                     e.g. "pdf/9508/9508027v1.pdf"
//
// IsBare inputs return the LEGACY layout because the category is
// unknown — that's the only thing we can address. Callers that need
// the new layout should disambiguate bare ids upstream (Neo4j lookup)
// and call again with the resolved canonical form.
//
// Returns "" for unrecognized kinds.
func AssetKeyFor(kind string, p ParsedArxivID) string {
	ext := assetExt(kind)
	if ext == "" || !p.IsValid() {
		return ""
	}
	if p.IsOldStyle && !p.IsBare {
		return kind + "/" + p.YYMM + "/" + p.Category + "/" + p.Stem + ext
	}
	return kind + "/" + p.YYMM + "/" + p.Stem + ext
}

// LegacyAssetKeyFor returns the pre-A1 object key (no category
// subdirectory) for old-style ids. Used by dual-read fallback to look
// up objects that haven't been migrated to the new layout yet.
//
// Returns "" for new-style ids (there is no legacy variant — the
// new-style layout never changed) and for bare ids (which are already
// the legacy form). Also returns "" for unrecognized kinds.
func LegacyAssetKeyFor(kind string, p ParsedArxivID) string {
	if !p.IsOldStyle || p.IsBare {
		return ""
	}
	ext := assetExt(kind)
	if ext == "" || !p.IsValid() {
		return ""
	}
	return kind + "/" + p.YYMM + "/" + p.Stem + ext
}

// assetExt maps a known kind to its file extension (including the
// leading dot). Returns "" for unrecognized kinds.
func assetExt(kind string) string {
	switch kind {
	case "pdf":
		return ".pdf"
	case "markdown":
		return ".md"
	case "json":
		return ".json"
	case "images":
		return ".zip"
	default:
		return ""
	}
}
