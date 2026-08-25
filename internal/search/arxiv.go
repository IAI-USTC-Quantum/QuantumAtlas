package search

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
)

// arxivAPIURL is the arXiv Atom query endpoint (same one the Python
// qatlas_search arxiv backend queries).
const arxivAPIURL = "https://export.arxiv.org/api/query"

// arxivUserAgent identifies us to the arXiv API, which rate-limits
// anonymous / unidentified clients aggressively.
const arxivUserAgent = "qatlasd-search (QuantumAtlas; +https://github.com/IAI-USTC-Quantum/QuantumAtlas)"

// ArxivProvider searches the arXiv Atom API. Exact identity lookups
// (id_list by arXiv id) score 0.9; query matches (doi / title / text)
// score 0.6 decaying by rank. Backend failures (transport, 429, 5xx)
// follow the provider contract: recorded via BaseProvider, reported as
// (nil, nil).
type ArxivProvider struct {
	BaseProvider
	client  *http.Client
	baseURL string // overridable in tests
}

// NewArxivProvider builds an ArxivProvider. A nil client falls back to a
// default http.Client with a 10s timeout.
func NewArxivProvider(client *http.Client) *ArxivProvider {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &ArxivProvider{client: client, baseURL: arxivAPIURL}
}

// Name implements Provider.
func (p *ArxivProvider) Name() string { return "arxiv" }

// Search implements Provider. Identity precedence mirrors the catalog
// provider: arXiv id (id_list) > DOI (doi:"…") > title (ti:"…") >
// text tokens (all:t AND …). An entry with nothing to query yields no
// hits (not a failure).
func (p *ArxivProvider) Search(ctx context.Context, e SearchEntry) ([]Hit, error) {
	params := url.Values{
		"start":       {"0"},
		"max_results": {strconv.Itoa(e.MaxResults)},
	}
	exact := false
	switch {
	case e.ArxivID != "":
		id := arxivQueryID(e.ArxivID)
		if id == "" {
			return nil, nil
		}
		params.Set("id_list", id)
		exact = true
	case e.DOI != "":
		// arXiv indexes DOIs in the doi field.
		params.Set("search_query", fmt.Sprintf(`doi:"%s"`, registry.NormalizeDOI(e.DOI)))
	case e.Title != "":
		params.Set("search_query", fmt.Sprintf(`ti:"%s"`, e.Title))
	default:
		tokens := queryTokens(e.Text)
		if len(tokens) == 0 {
			return nil, nil
		}
		parts := make([]string, len(tokens))
		for i, tok := range tokens {
			parts[i] = "all:" + tok
		}
		params.Set("search_query", strings.Join(parts, " AND "))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return p.RecordFailure(fmt.Errorf("search: arxiv build request: %w", err))
	}
	req.Header.Set("User-Agent", arxivUserAgent)

	resp, err := p.client.Do(req)
	if err != nil {
		return p.RecordFailure(fmt.Errorf("search: arxiv request: %w", err))
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return p.RecordFailure(fmt.Errorf("search: arxiv http %d", resp.StatusCode))
	}

	var feed atomFeed
	// Bounded read so a misbehaving upstream can't blow up memory.
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&feed); err != nil {
		return p.RecordFailure(fmt.Errorf("search: arxiv decode feed: %w", err))
	}

	hits := make([]Hit, 0, len(feed.Entries))
	for rank, entry := range feed.Entries {
		title := strings.Join(strings.Fields(entry.Title), " ")
		if title == "" {
			continue
		}
		hits = append(hits, Hit{
			ArxivID: arxivIDFromAtomURL(entry.ID),
			DOI:     registry.NormalizeDOI(strings.TrimSpace(entry.DOI)),
			Title:   title,
			Authors: entry.AuthorNames(),
			Year:    atomYear(entry.Published),
			Score:   arxivScore(rank, exact),
			Source:  p.Name(),
		})
	}
	return hits, nil
}

// atomFeed / atomEntry decode the Atom subset we consume. encoding/xml
// matches tags by local name, so the arxiv:doi element lands in DOI.
type atomFeed struct {
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	ID        string     `xml:"id"`
	Title     string     `xml:"title"`
	Published string     `xml:"published"`
	DOI       string     `xml:"doi"`
	Authors   []atomName `xml:"author"`
}

type atomName struct {
	Name string `xml:"name"`
}

// AuthorNames returns the byline names in order, blanks skipped.
func (e atomEntry) AuthorNames() []string {
	out := make([]string, 0, len(e.Authors))
	for _, a := range e.Authors {
		if name := strings.TrimSpace(a.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// arxivQueryID reduces a raw arXiv identifier to the version-stripped
// form id_list expects, canonicalizing old-style ids ("quant-ph/9508027")
// via paperassets.Parse and falling back to the registry normalization
// for anything Parse rejects.
func arxivQueryID(raw string) string {
	if parsed, err := paperassets.Parse(raw); err == nil {
		if parsed.IsOldStyle && parsed.Category != "" {
			return parsed.Category + "/" + parsed.StemBase
		}
		return parsed.StemBase
	}
	return registry.NormalizeArxivID(raw)
}

// arxivIDFromAtomURL extracts the version-stripped arXiv id from an
// entry's id URL ("http://arxiv.org/abs/2401.12345v2" → "2401.12345").
// Version info is intentionally dropped: identities own versioning.
func arxivIDFromAtomURL(raw string) string {
	i := strings.Index(raw, "/abs/")
	if i < 0 {
		return ""
	}
	return paperassets.StripVersion(raw[i+len("/abs/"):])
}

// atomYear parses the 4-digit year prefix of an Atom published timestamp
// ("2024-01-23T00:00:00Z" → 2024), 0 when unparsable.
func atomYear(published string) int {
	if len(published) < 4 {
		return 0
	}
	year, err := strconv.Atoi(published[:4])
	if err != nil {
		return 0
	}
	return year
}

// arxivScore scores id_list exact hits 0.9; query hits start at 0.6 and
// decay by rank, floored at 0.1.
func arxivScore(rank int, exact bool) float64 {
	if exact {
		return 0.9
	}
	if score := 0.6 - 0.01*float64(rank); score > 0.1 {
		return score
	}
	return 0.1
}
