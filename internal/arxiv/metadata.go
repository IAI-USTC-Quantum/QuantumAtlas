// metadata.go: batch metadata lookup against the arXiv Atom API.
//
// The arXiv query endpoint accepts id_list=id1,id2,... (comma-separated,
// up to ~200 ids) and returns one Atom entry per found id. This is the
// metadata source for `papers backfill-metadata`, which fills title /
// authors / publication_date / abstract for papers that `papers sync`
// minted from bucket listings with only an arxiv_id.
//
// The entry XML is the same shape internal/search/arxiv.go parses; the
// small atom* types are duplicated here (kept minimal on purpose) so the
// fetcher package stays free of a search dependency.

package arxiv

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
)

// MetadataAPIURL is the arXiv Atom query endpoint (same one
// internal/search/arxiv.go queries).
const MetadataAPIURL = "https://export.arxiv.org/api/query"

// metadataUserAgent identifies backfill traffic to the arXiv API, which
// rate-limits anonymous / unidentified clients aggressively. Mirrors the
// search provider convention.
const metadataUserAgent = "qatlasd-backfill (QuantumAtlas; +https://github.com/IAI-USTC-Quantum/QuantumAtlas)"

// EntryMetadata is the per-entry projection of an arXiv Atom reply.
type EntryMetadata struct {
	// ArxivID is the bare, version-stripped id ("2401.12345").
	ArxivID string
	Title   string
	Authors []string
	// Published is the arXiv <published> timestamp (v1 submission date).
	Published time.Time
	Abstract  string
	// DOI is the arxiv:doi element, normalized ("" when absent).
	DOI string
}

// FetchMetadataBatch queries the arXiv API for ids (version-stripped or
// not — versions are dropped before the request) and returns the entries
// keyed by bare arXiv id. Ids arXiv doesn't know are simply absent from
// the map (not an error). Callers own rate limiting; one call = one HTTP
// request.
func FetchMetadataBatch(ctx context.Context, client *http.Client, ids []string) (map[string]EntryMetadata, error) {
	return fetchMetadataBatch(ctx, client, MetadataAPIURL, ids)
}

// fetchMetadataBatch is FetchMetadataBatch with an overridable base URL
// so tests can point at an httptest server.
func fetchMetadataBatch(ctx context.Context, client *http.Client, baseURL string, ids []string) (map[string]EntryMetadata, error) {
	if len(ids) == 0 {
		return map[string]EntryMetadata{}, nil
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}

	bare := make([]string, 0, len(ids))
	for _, id := range ids {
		if b := paperassets.StripVersion(strings.TrimSpace(id)); b != "" {
			bare = append(bare, b)
		}
	}
	if len(bare) == 0 {
		return map[string]EntryMetadata{}, nil
	}

	params := url.Values{
		"id_list":     {strings.Join(bare, ",")},
		"max_results": {strconv.Itoa(len(bare))},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+"?"+params.Encode(), nil)
	if err != nil {
		return nil, fmt.Errorf("arxiv: build metadata request: %w", err)
	}
	req.Header.Set("User-Agent", metadataUserAgent)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("arxiv: metadata request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("arxiv: metadata http %d", resp.StatusCode)
	}

	var feed atomFeed
	// Bounded read so a misbehaving upstream can't blow up memory.
	if err := xml.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&feed); err != nil {
		return nil, fmt.Errorf("arxiv: decode metadata feed: %w", err)
	}

	out := make(map[string]EntryMetadata, len(feed.Entries))
	for _, entry := range feed.Entries {
		id := arxivIDFromAtomURL(entry.ID)
		if id == "" {
			continue
		}
		published, _ := time.Parse(time.RFC3339, entry.Published)
		out[id] = EntryMetadata{
			ArxivID:   id,
			Title:     strings.Join(strings.Fields(entry.Title), " "),
			Authors:   entry.authorNames(),
			Published: published,
			Abstract:  strings.Join(strings.Fields(entry.Summary), " "),
			DOI:       paperassets.NormalizeDOI(strings.TrimSpace(entry.DOI)),
		}
	}
	return out, nil
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
	Summary   string     `xml:"summary"`
	DOI       string     `xml:"doi"`
	Authors   []atomName `xml:"author"`
}

type atomName struct {
	Name string `xml:"name"`
}

// authorNames returns the byline names in order, blanks skipped.
func (e atomEntry) authorNames() []string {
	out := make([]string, 0, len(e.Authors))
	for _, a := range e.Authors {
		if name := strings.TrimSpace(a.Name); name != "" {
			out = append(out, name)
		}
	}
	return out
}

// arxivIDFromAtomURL extracts the version-stripped arXiv id from an
// entry's id URL ("http://arxiv.org/abs/2401.12345v2" → "2401.12345").
func arxivIDFromAtomURL(raw string) string {
	i := strings.Index(raw, "/abs/")
	if i < 0 {
		return ""
	}
	return paperassets.StripVersion(raw[i+len("/abs/"):])
}
