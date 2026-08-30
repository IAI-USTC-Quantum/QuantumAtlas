package search

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/openalex"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// openalexAPIURL is the OpenAlex works search endpoint.
const openalexAPIURL = "https://api.openalex.org/works"

// openalexMaxPerPage bounds per-page; the engine caps MaxResults at 50
// but one OpenAlex page of 25 is plenty for a metadata provider.
const openalexMaxPerPage = 25

// OpenAlexProvider searches the OpenAlex works API. Identity precedence
// mirrors the other providers: DOI (filter=doi:…) > title / text (search
// relevance endpoint). An arXiv-id-only entry contributes no hits:
// OpenAlex's public API has no arXiv-id lookup — /works/{id} accepts
// OpenAlex ids, DOIs, MAG, PMID, but not arXiv (internal/openalex/
// lookup.go FetchWorkRecord documents exactly this: "arxiv" is not a
// /works/{id} key) — so with no DOI or title to anchor on there is
// nothing reliable to query.
//
// Hits carry the OpenAlex relevance_score normalized to [0,1] when
// present, else 0.5. Backend failures follow the provider contract:
// recorded via BaseProvider, reported as (nil, nil).
type OpenAlexProvider struct {
	BaseProvider
	client  *http.Client
	mailto  string
	baseURL string // overridable in tests
}

// NewOpenAlexProvider builds an OpenAlexProvider. mailto joins the
// OpenAlex polite pool (mailto param + User-Agent); empty omits it. A
// nil client falls back to a default http.Client with a 10s timeout.
func NewOpenAlexProvider(client *http.Client, mailto string) *OpenAlexProvider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &OpenAlexProvider{
		client:  client,
		mailto:  strings.TrimSpace(mailto),
		baseURL: openalexAPIURL,
	}
}

// Name implements Provider.
func (p *OpenAlexProvider) Name() string { return "openalex" }

// Search implements Provider.
func (p *OpenAlexProvider) Search(ctx context.Context, e SearchEntry) ([]Hit, error) {
	perPage := e.MaxResults
	if perPage > openalexMaxPerPage {
		perPage = openalexMaxPerPage
	}
	params := url.Values{"per-page": {strconv.Itoa(perPage)}}
	if p.mailto != "" {
		params.Set("mailto", p.mailto)
	}

	switch {
	case e.DOI != "":
		params.Set("filter", "doi:"+registry.NormalizeDOI(e.DOI))
	case e.Title != "" || e.Text != "":
		q := e.Title
		if q == "" {
			q = e.Text
		}
		params.Set("search", q)
	default:
		// arXiv-id-only entry: unsupported upstream (see type doc).
		return nil, nil
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return p.RecordFailure(fmt.Errorf("search: openalex build request: %w", err))
	}
	ua := "qatlasd-search"
	if p.mailto != "" {
		ua = "qatlasd-search (mailto:" + p.mailto + ")"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")

	resp, err := p.client.Do(req)
	if err != nil {
		return p.RecordFailure(fmt.Errorf("search: openalex request: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p.RecordFailure(fmt.Errorf("search: openalex http %d", resp.StatusCode))
	}

	var list oaListResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&list); err != nil {
		return p.RecordFailure(fmt.Errorf("search: openalex decode body: %w", err))
	}

	hits := make([]Hit, 0, len(list.Results))
	for _, w := range list.Results {
		title := strings.TrimSpace(w.Title)
		if title == "" {
			continue
		}
		hits = append(hits, Hit{
			ArxivID:  openalex.ExtractArxivID(openalex.Work{Locations: w.Locations}),
			DOI:      registry.NormalizeDOI(w.DOI),
			Title:    title,
			Abstract: w.abstract(),
			Authors:  openalex.AuthorNames(openalex.Work{Authorships: w.Authorships}),
			Year:     w.PublicationYear,
			Score:    w.score(),
			Source:   p.Name(),
		})
	}
	return hits, nil
}

// oaListResponse / oaWork decode the OpenAlex subset we consume. The
// authorship / location shapes reuse internal/openalex's types so the
// arXiv-id and author extraction share one implementation with the
// resolver and the snapshot ingest.
type oaListResponse struct {
	Results []oaWork `json:"results"`
}

type oaWork struct {
	Title           string                `json:"title"`
	PublicationYear int                   `json:"publication_year"`
	DOI             string                `json:"doi"` // "https://doi.org/10.xxxx/yyyy"
	RelevanceScore  float64               `json:"relevance_score"`
	Authorships     []openalex.Authorship `json:"authorships"`
	Locations       []openalex.Location   `json:"locations"`
	AbstractIndex   map[string][]int      `json:"abstract_inverted_index"`
}

func (w oaWork) abstract() string {
	max := -1
	for _, positions := range w.AbstractIndex {
		for _, pos := range positions {
			if pos > max {
				max = pos
			}
		}
	}
	if max < 0 {
		return ""
	}
	words := make([]string, max+1)
	for word, positions := range w.AbstractIndex {
		for _, pos := range positions {
			if pos >= 0 && pos < len(words) {
				words[pos] = word
			}
		}
	}
	return strings.TrimSpace(strings.Join(words, " "))
}

// score normalizes the OpenAlex relevance score (roughly 0–100) to
// [0,1]; works without one (e.g. filter lookups) score 0.5.
func (w oaWork) score() float64 {
	if w.RelevanceScore <= 0 {
		return 0.5
	}
	if score := w.RelevanceScore / 100; score < 1.0 {
		return score
	}
	return 1.0
}
