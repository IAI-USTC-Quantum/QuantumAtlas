package downloader

import (
	"net/url"
	"regexp"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// IdentifierKind classifies a parsed downloader input line.
type IdentifierKind string

const (
	KindDOI   IdentifierKind = "doi"
	KindArxiv IdentifierKind = "arxiv"
	KindURL   IdentifierKind = "url"
	KindBad   IdentifierKind = "invalid"
)

// Identifier is one parsed input line: what the user pasted, what kind
// it is, and the registry ref it maps to (exactly one of DOI / ArxivID
// set for doi/arxiv kinds; URL inputs are normalized to a DOI or arXiv
// id whenever the link shape allows it, else ErrUnsupportedURL).
type Identifier struct {
	Input string
	Kind  IdentifierKind
	Ref   registry.PaperRef
}

// doiRE matches bare DOIs (10.<digits>/<suffix>). Case-insensitive on
// the prefix per the DOI handbook.
var doiRE = regexp.MustCompile(`(?i)\b(10\.\d{4,9}/[^\s"<>]+)`)

// arxivNewRE / arxivOldRE match new-style (2401.12345) and old-style
// (quant-ph/9508027) arXiv ids with optional vN; arxivPrefixRE strips a
// leading "arXiv:" decoration from a bare input line.
var (
	arxivNewRE    = regexp.MustCompile(`(?i)\b(\d{4}\.\d{4,5})(v\d+)?\b`)
	arxivOldRE    = regexp.MustCompile(`(?i)\b([a-z-]+(?:\.[a-z]{2})?/\d{7})(v\d+)?\b`)
	arxivPrefixRE = regexp.MustCompile(`(?i)^arxiv:\s*(.+)$`)
)

// Supported URL shapes beyond doi.org/arxiv.org (biorxiv, pmc, etc.)
// carry a DOI or arXiv id in the path we can extract; everything else
// is politely rejected with a hint.
var (
	doiHostRE   = regexp.MustCompile(`^(www\.)?(doi\.org|dx\.doi\.org)$`)
	arxivHostRE = regexp.MustCompile(`^(www\.)?(arxiv\.org|export\.arxiv\.org)$`)
	biorxivRE   = regexp.MustCompile(`(?i)/(?:content/)?(10\.1101/[^\s?#]+)`)
	pmcURLRE    = regexp.MustCompile(`(?i)pmc\.ncbi\.nlm\.nih\.gov/articles/(PMC\d+)`)
)

// ParseIdentifier classifies one input line. Whitespace and common
// decorations (surrounding quotes, commas, trailing periods) are
// tolerated; unknown URL shapes return KindBad with the reason in
// Detail (via the error). It never performs network lookups.
func ParseIdentifier(input string) (Identifier, error) {
	s := strings.TrimSpace(input)
	s = strings.Trim(s, "\"'`")
	s = strings.TrimRight(s, ",;")
	out := Identifier{Input: input, Kind: KindBad}

	// URL input?
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return out, errUnsupported("unparseable URL")
		}
		out.Kind = KindURL
		host := strings.ToLower(u.Host)
		path := u.EscapedPath()
		switch {
		case doiHostRE.MatchString(host):
			id := strings.TrimPrefix(path, "/")
			id = strings.TrimPrefix(id, "doi:") // rare Humanitatis style
			if doi, ok := paperassets.ValidateDOI(id); ok {
				out.Ref.DOI = doi
				return out, nil
			}
			// Some doi.org links append fragments/suffixes; fall back to
			// a regex sweep of the whole URL.
			if m := doiRE.FindString(s); m != "" {
				if doi, ok := paperassets.ValidateDOI(strings.TrimSuffix(m, ".")); ok {
					out.Ref.DOI = doi
					return out, nil
				}
			}
			return out, errUnsupported("doi.org URL without a valid DOI")
		case arxivHostRE.MatchString(host):
			id := extractArxivFromPath(path, u.Query().Get("id_list"))
			if id == "" {
				return out, errUnsupported("arxiv.org URL without an id")
			}
			out.Ref.ArxivID = id
			return out, nil
		case strings.Contains(host, "biorxiv.org"), strings.Contains(host, "medrxiv.org"):
			if m := biorxivRE.FindStringSubmatch(s); m != nil {
				if doi, ok := paperassets.ValidateDOI(m[1]); ok {
					out.Ref.DOI = doi
					return out, nil
				}
			}
			return out, errUnsupported("biorxiv/medrxiv URL without a DOI")
		case pmcURLRE.MatchString(s):
			// PMCID alone is not a registry identity; the DOI strategy
			// chain starts from the EPMC resolver which accepts PMC ids,
			// so surface it as a URL-kind input with the PMCID stashed
			// in the ref's DOI field prefixed "pmc:" (resolved later by
			// the EPMC strategy, never written to the registry as-is).
			m := pmcURLRE.FindStringSubmatch(s)
			out.Ref.DOI = "pmc:" + m[1]
			return out, nil
		}
		// Generic URL: maybe it embeds a DOI anywhere (publisher links
		// like dl.acm.org/doi/10.1145/... or nature.com/articles/s41586-...).
		if m := doiRE.FindString(s); m != "" {
			if doi, ok := paperassets.ValidateDOI(strings.TrimSuffix(m, ".")); ok {
				out.Ref.DOI = doi
				out.Kind = KindDOI
				return out, nil
			}
		}
		return out, errUnsupported("unsupported URL shape (paste the DOI or arXiv id instead)")
	}

	// Bare DOI?
	if m := doiRE.FindString(s); m != "" {
		doi := strings.TrimSuffix(m, ".")
		if doi2, ok := paperassets.ValidateDOI(doi); ok {
			out.Kind = KindDOI
			out.Ref.DOI = doi2
			return out, nil
		}
	}

	// Bare arXiv id (possibly with an "arXiv:" prefix)?
	id := s
	if m := arxivPrefixRE.FindStringSubmatch(s); m != nil {
		id = m[1]
	}
	id = strings.TrimSpace(id)
	if plausibleArxivID(id) {
		out.Kind = KindArxiv
		out.Ref.ArxivID = id
		return out, nil
	}

	return out, errUnsupported("not a recognized DOI, arXiv id, or paper URL")
}

// plausibleArxivID accepts anything paperassets.Parse accepts, plus the
// shapes its strictness rejects but the community still pastes (e.g.
// old-style ids with mixed case archives).
func plausibleArxivID(id string) bool {
	if id == "" {
		return false
	}
	if _, err := paperassets.Parse(id); err == nil {
		return true
	}
	if m := arxivNewRE.FindStringSubmatch(id); m != nil && m[0] == id {
		return true
	}
	if m := arxivOldRE.FindStringSubmatch(id); m != nil && m[0] == id {
		return true
	}
	return false
}

func errUnsupported(msg string) error { return &UnsupportedInputError{Msg: msg} }

// UnsupportedInputError marks inputs ParseIdentifier could not classify.
type UnsupportedInputError struct{ Msg string }

func (e *UnsupportedInputError) Error() string { return "unsupported input: " + e.Msg }

// extractArxivFromPath pulls the arXiv id out of arxiv.org URL paths:
// /abs/<id>, /pdf/<id>(vN), /pdf/<id>vN, or /?id_list=<id>.
func extractArxivFromPath(path, idList string) string {
	for _, prefix := range []string{"/abs/", "/pdf/", "/format/"} {
		if strings.HasPrefix(path, prefix) {
			return strings.Trim(strings.TrimPrefix(path, prefix), "/")
		}
	}
	if idList != "" {
		return strings.TrimSpace(idList)
	}
	return ""
}

// IsPMCID reports whether a DOI-slot value is actually a PMCID stashed
// by ParseIdentifier ("pmc:PMC1234567").
func IsPMCID(doi string) bool {
	return strings.HasPrefix(strings.ToLower(doi), "pmc:")
}
