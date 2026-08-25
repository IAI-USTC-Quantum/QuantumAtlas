// Package search is the multi-paradigm pluggable search engine: one
// SearchEntry is fanned out to every registered Provider, the hits are
// merged by paper identity (DOI > arXiv > title hash), and merged hits
// carrying an authoritative identity are resolved-or-minted against the
// PostgreSQL registry. Title-only hits stay un-minted candidates.
//
// The package mirrors the Python qatlas_search conventions
// (search/qatlas_search): same entry JSON shape, same identity-key
// priority, and the same provider failure contract — one backend failing
// never sinks the whole search.
package search

import "strings"

// DefaultMaxResults applies when the entry carries MaxResults <= 0.
const DefaultMaxResults = 10

// MaxResultsCap bounds fan-out cost no matter what the caller asks for.
const MaxResultsCap = 50

// MaxCandidates caps title-only (un-minted) candidates per response.
const MaxCandidates = 5

// SearchEntry is a single search request. The JSON tags match the Python
// SearchQuery / entry format so both stacks accept the same payload.
type SearchEntry struct {
	Text            string   `json:"text"`
	Title           string   `json:"title"`
	DOI             string   `json:"doi"`
	ArxivID         string   `json:"arxiv_id"`
	MaxResults      int      `json:"max_results"`
	RequiredPhrases []string `json:"required_phrases"`
}

// Normalize trims whitespace, applies the MaxResults default and cap, and
// drops empty required phrases. Call it once before fan-out.
func (e *SearchEntry) Normalize() {
	e.Text = strings.TrimSpace(e.Text)
	e.Title = strings.TrimSpace(e.Title)
	e.DOI = strings.TrimSpace(e.DOI)
	e.ArxivID = strings.TrimSpace(e.ArxivID)
	if e.MaxResults <= 0 {
		e.MaxResults = DefaultMaxResults
	}
	if e.MaxResults > MaxResultsCap {
		e.MaxResults = MaxResultsCap
	}
	phrases := e.RequiredPhrases[:0]
	for _, p := range e.RequiredPhrases {
		if p = strings.TrimSpace(p); p != "" {
			phrases = append(phrases, p)
		}
	}
	e.RequiredPhrases = phrases
}
