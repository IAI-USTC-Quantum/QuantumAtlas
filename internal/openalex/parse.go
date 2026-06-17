// Package openalex ingests the OpenAlex "档 B" works snapshot (quant +
// first-order citations, ~10M works) into the Neo4j :PaperWork layer.
//
// SCOPE: in v0.7.0 the *execution* of the full bootstrap is decoupled
// (see handoff.md) — this package provides the compiling, unit-tested
// building blocks (arxiv-id extraction, work→node mapping, batched
// MERGE ingest, gzip jsonl streaming) that the `qatlasd openalex`
// subcommand drives. Nothing here runs at server boot.
package openalex

import (
	"regexp"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// Work is the subset of an OpenAlex Work record we consume. OpenAlex
// records have ~40 top-level fields; we deliberately decode only what
// maps onto :PaperWork properties + citation edges.
type Work struct {
	ID              string       `json:"id"`  // "https://openalex.org/W2741809807"
	DOI             string       `json:"doi"` // "https://doi.org/10.7717/peerj.4375"
	Title           string       `json:"title"`
	PublicationDate string       `json:"publication_date"`
	Locations       []Location   `json:"locations"`
	Authorships     []Authorship `json:"authorships"`
	ReferencedWorks []string     `json:"referenced_works"`
	CitedByCount    int          `json:"cited_by_count"`
}

// Location is one OpenAlex location (landing page / pdf host). arxiv ids
// are mined from landing_page_url / pdf_url.
type Location struct {
	LandingPageURL string `json:"landing_page_url"`
	PDFURL         string `json:"pdf_url"`
}

// Authorship is one author slot on a Work. OpenAlex gives both a
// disambiguated author object and the raw byline string; we keep both so
// AuthorNames can fall back when disambiguation is missing.
type Authorship struct {
	Author        Author `json:"author"`
	RawAuthorName string `json:"raw_author_name"`
}

// Author is the disambiguated author entity (subset).
type Author struct {
	DisplayName string `json:"display_name"`
}

// AuthorNames returns a Work's author display names in byline order,
// falling back to raw_author_name when the disambiguated author has no
// display name. Blank names are skipped. Used for upload-time author
// verification on DOI contributions.
func AuthorNames(w Work) []string {
	out := make([]string, 0, len(w.Authorships))
	for _, a := range w.Authorships {
		name := strings.TrimSpace(a.Author.DisplayName)
		if name == "" {
			name = strings.TrimSpace(a.RawAuthorName)
		}
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// arxivURLRE captures the arxiv id out of an arxiv.org URL in any of the
// forms OpenAlex emits:
//
//	https://arxiv.org/abs/2401.12345
//	https://arxiv.org/abs/2401.12345v2
//	http://arxiv.org/pdf/quant-ph/9508027
//	https://arxiv.org/abs/quant-ph/9508027v1
var arxivURLRE = regexp.MustCompile(
	`arxiv\.org/(?:abs|pdf)/((?:[a-z-]+(?:\.[A-Z]{2})?/)?\d{4,7}(?:\.\d{4,5})?)(v\d+)?`)

// ExtractArxivID returns the canonical (version-stripped, category-
// preserving) arxiv id mined from a Work's locations, or "" when the
// work has no arxiv presence.
func ExtractArxivID(w Work) string {
	for _, loc := range w.Locations {
		for _, u := range []string{loc.LandingPageURL, loc.PDFURL} {
			if id := arxivIDFromURL(u); id != "" {
				return id
			}
		}
	}
	return ""
}

func arxivIDFromURL(u string) string {
	if u == "" {
		return ""
	}
	m := arxivURLRE.FindStringSubmatch(u)
	if m == nil {
		return ""
	}
	// m[1] is the id body, m[2] the optional version — drop version for
	// the canonical id (matches paperassets.StripVersion semantics).
	return paperassets.StripVersion(m[1])
}

// shortID strips the OpenAlex URL prefix from a W/A/S/T id, leaving the
// bare "W2741809807" form used as the node key.
func shortID(openalexURL string) string {
	if i := strings.LastIndexByte(openalexURL, '/'); i >= 0 {
		return openalexURL[i+1:]
	}
	return openalexURL
}

// shortDOI strips the doi.org URL prefix, leaving the bare DOI.
func shortDOI(doiURL string) string {
	s := strings.TrimPrefix(doiURL, "https://doi.org/")
	s = strings.TrimPrefix(s, "http://doi.org/")
	return s
}
