package registry

import (
	"crypto/sha1"
	"encoding/hex"
	"strconv"
	"strings"
	"unicode"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// Identity kinds stored in paper_identities.kind.
const (
	KindDOI          = "doi"
	KindArxiv        = "arxiv"
	KindArxivVersion = "arxiv_version"
	KindTitle        = "title"
)

// identityKey is one (key, kind) pair destined for paper_identities.
type identityKey struct {
	key  string
	kind string
}

// NormalizeDOI canonicalizes a raw DOI for storage and identity keys:
// lowercased, any https://doi.org/-style prefix stripped. Returns "" for
// empty input.
func NormalizeDOI(raw string) string {
	return paperassets.NormalizeDOI(raw)
}

// NormalizeArxivID returns the bare, version-stripped normalized arXiv
// id, e.g. "2401.12345" or "quant-ph/9508027". This is the form stored
// in papers.arxiv_id; the version lives per-asset / per arxiv_version
// identity.
func NormalizeArxivID(raw string) string {
	return paperassets.StripVersion(paperassets.NormalizeIdentifier(raw))
}

// ArxivVersionOf returns the integer version suffix of an arXiv id (the
// "N" in "vN"), or 0 when the id carries no version.
func ArxivVersionOf(raw string) int {
	p, err := paperassets.Parse(raw)
	if err != nil || p.Version == "" {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimPrefix(p.Version, "v"))
	if err != nil {
		return 0
	}
	return n
}

// DOIKey builds the identity key for a (normalized) DOI.
func DOIKey(doi string) string { return "doi:" + doi }

// ArxivKey builds the identity key for a bare arXiv id.
func ArxivKey(bare string) string { return "arxiv:" + bare }

// ArxivVersionKey builds the identity key for a full versioned arXiv id,
// e.g. "arxiv_version:2401.12345v2".
func ArxivVersionKey(full string) string { return "arxiv_version:" + full }

// TitleKey builds the identity key for a TitleHash.
func TitleKey(hash string) string { return "title:" + hash }

// TitleHash computes the fuzzy title identity: sha1 hex of
// normalized_title + "|" + first_author_lastname + "|" + year, where
// normalized_title is lowercased with alphanumeric tokens joined by a
// single space and first_author_lastname is the last alphanumeric token
// of the first author's name (lowercased). Two citations of the same
// work almost always agree on this triple even when the exact
// punctuation / author formatting differs.
func TitleHash(title string, authors []string, year int) string {
	norm := normalizeTitle(title)
	last := firstAuthorLastname(authors)
	sum := sha1.Sum([]byte(norm + "|" + last + "|" + strconv.Itoa(year)))
	return hex.EncodeToString(sum[:])
}

// normalizeTitle lowercases and reduces the title to its alphanumeric
// tokens joined by single spaces.
func normalizeTitle(title string) string {
	tokens := strings.FieldsFunc(strings.ToLower(title), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(tokens, " ")
}

// firstAuthorLastname extracts the lowercased, alphanumeric-only last
// token of the first author's name ("" when authors is empty).
func firstAuthorLastname(authors []string) string {
	if len(authors) == 0 {
		return ""
	}
	fields := strings.Fields(authors[0])
	if len(fields) == 0 {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return unicode.ToLower(r)
		}
		return -1
	}, fields[len(fields)-1])
}
