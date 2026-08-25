// batch.go: batch metadata lookup against the OpenAlex works API.
//
// OpenAlex supports an OR filter on multiple DOIs in ONE request
// (filter=doi:<doi1>|<doi2>|..., per-page up to 200), which makes it the
// fast backfill source for DOI-bearing untitled papers: the polite pool
// (mailto param) allows ~10 req/s where arXiv 429s aggressively. The
// trade-off: OpenAlex cannot filter by arXiv id, so this only covers
// papers with a DOI.
//
// Driven by `papers backfill-metadata --source=openalex`. The response
// shapes reuse this package's Authorship / Location types so author and
// arXiv-id extraction share one implementation with the resolver, the
// search provider, and the snapshot ingest.

package openalex

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
)

// BatchAPIURL is the OpenAlex works list endpoint (the same one
// internal/search/openalex.go queries).
const BatchAPIURL = "https://api.openalex.org/works"

// WorkMetadata is the per-work projection a DOI batch lookup returns.
type WorkMetadata struct {
	// DOI is the normalized bare doi (also the map key).
	DOI     string
	Title   string
	Authors []string
	// PublicationDate is the work's publication_date (zero when absent
	// or unparsable).
	PublicationDate time.Time
	// Abstract is plain text reconstructed from abstract_inverted_index
	// ("" when the work has none).
	Abstract string
	// ArxivID is the canonical version-stripped arXiv id mined from the
	// work's locations ("" when no arXiv presence).
	ArxivID string
}

// FetchWorksByDOIBatch queries OpenAlex for a batch of DOIs (any form —
// each is normalized to the bare 10.xxxx/yyyy form before the request)
// and returns the matching works keyed by normalized DOI. DOIs OpenAlex
// doesn't know are simply absent from the map (not an error). Callers
// own rate limiting; one call = one HTTP request. mailto joins the
// polite pool (mailto param + User-Agent); empty omits it.
func FetchWorksByDOIBatch(ctx context.Context, client *http.Client, mailto string, dois []string) (map[string]WorkMetadata, error) {
	return fetchWorksByDOIBatch(ctx, client, BatchAPIURL, mailto, dois)
}

// fetchWorksByDOIBatch is FetchWorksByDOIBatch with an overridable base
// URL so tests can point at an httptest server.
func fetchWorksByDOIBatch(ctx context.Context, client *http.Client, baseURL, mailto string, dois []string) (map[string]WorkMetadata, error) {
	if len(dois) == 0 {
		return map[string]WorkMetadata{}, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	bare := make([]string, 0, len(dois))
	for _, d := range dois {
		if b := shortDOI(d); b != "" {
			bare = append(bare, b)
		}
	}
	if len(bare) == 0 {
		return map[string]WorkMetadata{}, nil
	}

	params := url.Values{
		"filter":   {"doi:" + strings.Join(bare, "|")},
		"per-page": {strconv.Itoa(len(bare))},
	}
	mailto = strings.TrimSpace(mailto)
	if mailto != "" {
		params.Set("mailto", mailto)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("openalex: build batch request: %w", err)
	}
	ua := "qatlasd-backfill"
	if mailto != "" {
		ua = "qatlasd-backfill (mailto:" + mailto + ")"
	}
	req.Header.Set("User-Agent", ua)
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("openalex: batch request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openalex: batch http %d", resp.StatusCode)
	}

	var list oaBatchResponse
	// Bounded read so a misbehaving upstream can't blow up memory.
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("openalex: decode batch body: %w", err)
	}

	out := make(map[string]WorkMetadata, len(list.Results))
	for _, w := range list.Results {
		doi := shortDOI(w.DOI)
		if doi == "" {
			continue
		}
		pubDate, _ := time.Parse("2006-01-02", w.PublicationDate)
		out[doi] = WorkMetadata{
			DOI:             doi,
			Title:           strings.TrimSpace(w.Title),
			Authors:         AuthorNames(Work{Authorships: w.Authorships}),
			PublicationDate: pubDate,
			Abstract:        reconstructAbstract(w.AbstractInvertedIndex),
			ArxivID:         ExtractArxivID(Work{Locations: w.Locations}),
		}
	}
	return out, nil
}

// oaBatchResponse / oaBatchWork decode the OpenAlex subset we consume.
type oaBatchResponse struct {
	Results []oaBatchWork `json:"results"`
}

type oaBatchWork struct {
	DOI             string `json:"doi"` // "https://doi.org/10.xxxx/yyyy"
	Title           string `json:"title"`
	PublicationDate string `json:"publication_date"`
	// AbstractInvertedIndex maps each abstract word to the positions it
	// occurs at (OpenAlex's compact abstract encoding).
	AbstractInvertedIndex map[string][]int `json:"abstract_inverted_index"`
	Authorships           []Authorship     `json:"authorships"`
	Locations             []Location       `json:"locations"`
}

// reconstructAbstract inverts OpenAlex's word→positions map back into
// plain text by placing each word at its positions.
func reconstructAbstract(index map[string][]int) string {
	if len(index) == 0 {
		return ""
	}
	n := 0
	for _, positions := range index {
		for _, p := range positions {
			if p+1 > n {
				n = p + 1
			}
		}
	}
	words := make([]string, n)
	for word, positions := range index {
		for _, p := range positions {
			if p >= 0 && p < n {
				words[p] = word
			}
		}
	}
	return strings.Join(words, " ")
}
