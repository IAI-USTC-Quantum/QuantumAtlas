package search

// Hit is one search result, normalized across providers. Source is the
// producing provider's name; after merging, hits found by several
// providers carry the comma-joined source names (mirroring the Python
// side). Score is the provider's own relevance score; merged hits keep
// the max.
type Hit struct {
	ArxivID string   `json:"arxiv_id"`
	DOI     string   `json:"doi"`
	Title   string   `json:"title"`
	Authors []string `json:"authors,omitempty"`
	Year    int      `json:"year,omitempty"`
	Score   float64  `json:"score"`
	Source  string   `json:"source"`
}
