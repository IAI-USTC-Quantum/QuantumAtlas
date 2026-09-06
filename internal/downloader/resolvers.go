package downloader

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// OAResolver produces candidate direct-PDF URLs for a DOI from one
// open-access metadata API. Implementations must be read-only, cheap,
// and never fetch the PDF itself — validation stays in FetchClient.
type OAResolver interface {
	Name() string
	Candidates(ctx context.Context, doi string) ([]string, error)
}

// getJSON is the shared bounded-JSON GET used by the resolver clients.
func getJSON(ctx context.Context, client *http.Client, ua, rawURL string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	if ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// getS2JSON is getJSON plus the S2 x-api-key header when a key is set.
func getS2JSON(ctx context.Context, client *http.Client, apiKey, rawURL string, out any) error {
	if apiKey == "" {
		return getJSON(ctx, client, "", rawURL, out)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, out)
}

// Unpaywall resolves via https://unpaywall.org. email is required by
// the API (100k calls/day per address); empty disables the resolver.
type Unpaywall struct {
	BaseURL string
	Email   string
	Client  *http.Client
}

// NewUnpaywall builds the client; baseURL empty → production endpoint.
func NewUnpaywall(baseURL, email string, timeout time.Duration) *Unpaywall {
	if baseURL == "" {
		baseURL = "https://api.unpaywall.org"
	}
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	return &Unpaywall{BaseURL: baseURL, Email: email, Client: &http.Client{Timeout: timeout}}
}

func (u *Unpaywall) Name() string { return "unpaywall" }

func (u *Unpaywall) Enabled() bool { return strings.TrimSpace(u.Email) != "" }

type unpaywallResponse struct {
	IsOA        bool                `json:"is_oa"`
	BestOA      *unpaywallLocation  `json:"best_oa_location"`
	OALocations []unpaywallLocation `json:"oa_locations"`
}

type unpaywallLocation struct {
	URLForPDF string `json:"url_for_pdf"`
	URL       string `json:"url"`
}

func (u *Unpaywall) Candidates(ctx context.Context, doi string) ([]string, error) {
	if !u.Enabled() {
		return nil, fmt.Errorf("unpaywall: no email configured")
	}
	var out unpaywallResponse
	raw := fmt.Sprintf("%s/v2/%s?email=%s", u.BaseURL, url.PathEscape(doi), url.QueryEscape(u.Email))
	if err := getJSON(ctx, u.Client, "", raw, &out); err != nil {
		return nil, fmt.Errorf("unpaywall: %w", err)
	}
	var cands []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s != "" && !seen[s] {
			seen[s] = true
			cands = append(cands, s)
		}
	}
	if out.BestOA != nil {
		add(out.BestOA.URLForPDF)
	}
	for _, loc := range out.OALocations {
		add(loc.URLForPDF)
	}
	// Landing pages come last: green-OA repositories (HAL, university
	// repos) expose only a landing URL here — the ladder mines them for
	// the real PDF link (oa:<name>+landing).
	if out.BestOA != nil {
		add(out.BestOA.URL)
	}
	for _, loc := range out.OALocations {
		add(loc.URL)
	}
	return cands, nil
}

// EuropePMC resolves via https://europepmc.org. fullTextUrlList carries
// pdf links with availability flags; subscription-required PDF links
// are kept after the open ones (an entitled network can still fetch
// them). Also resolves PMCIDs (PMC1234567) to DOIs.
type EuropePMC struct {
	BaseURL string
	Client  *http.Client
}

func NewEuropePMC(baseURL string, timeout time.Duration) *EuropePMC {
	if baseURL == "" {
		baseURL = "https://www.ebi.ac.uk/europepmc/webservices/rest"
	}
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	return &EuropePMC{BaseURL: baseURL, Client: &http.Client{Timeout: timeout}}
}

func (e *EuropePMC) Name() string { return "europepmc" }

type epmcSearchResponse struct {
	ResultList struct {
		Result []epmcResult `json:"result"`
	} `json:"resultList"`
}

type epmcResult struct {
	DOI   string `json:"doi"`
	PMCID string `json:"pmcid"`
	// EPMC serializes isOpenAccess as "Y"/"N" (string), not a bool —
	// we never branch on it, so keep it a string.
	IsOA            string               `json:"isOpenAccess"`
	FullTextURLList *epmcFullTextURLList `json:"fullTextUrlList"`
}

type epmcFullTextURLList struct {
	FullTextURL []epmcFullTextURL `json:"fullTextUrl"`
}

type epmcFullTextURL struct {
	Availability  string `json:"availability"`
	DocumentStyle string `json:"documentStyle"`
	URL           string `json:"url"`
}

func (e *EuropePMC) search(ctx context.Context, query string) (*epmcResult, error) {
	var out epmcSearchResponse
	raw := fmt.Sprintf("%s/search?query=%s&format=json&resultType=core&pageSize=1",
		e.BaseURL, url.QueryEscape(query))
	if err := getJSON(ctx, e.Client, "", raw, &out); err != nil {
		return nil, fmt.Errorf("europepmc: %w", err)
	}
	results := out.ResultList.Result
	if len(results) == 0 {
		return nil, fmt.Errorf("europepmc: no result")
	}
	return &results[0], nil
}

func (e *EuropePMC) Candidates(ctx context.Context, doi string) ([]string, error) {
	res, err := e.search(ctx, fmt.Sprintf("DOI:%q", doi))
	if err != nil {
		return nil, err
	}
	return epmcPDFURLs(res), nil
}

// CandidatesByPMCID returns PDF candidates plus the article's DOI.
func (e *EuropePMC) CandidatesByPMCID(ctx context.Context, pmcid string) (cands []string, doi string, err error) {
	res, err := e.search(ctx, "PMCID:"+pmcid)
	if err != nil {
		return nil, "", err
	}
	return epmcPDFURLs(res), res.DOI, nil
}

// epmcPDFURLs orders open-access pdf links first, then
// subscription-required pdf links; the PoW-free render URL for PMC
// hosted articles is injected first when the record is in EPMC.
func epmcPDFURLs(res *epmcResult) []string {
	var open, sub []string
	seen := map[string]bool{}
	add := func(list *[]string, s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		*list = append(*list, s)
	}
	if res.PMCID != "" {
		// europepmc.org render path sidesteps pmc.ncbi.nlm.nih.gov's
		// proof-of-work interstitial entirely.
		add(&open, "https://europepmc.org/article/PMC/"+res.PMCID+"?pdf=render")
	}
	if res.FullTextURLList != nil {
		for _, ft := range res.FullTextURLList.FullTextURL {
			if !strings.EqualFold(ft.DocumentStyle, "pdf") {
				continue
			}
			if strings.EqualFold(ft.Availability, "Open access") || strings.EqualFold(ft.Availability, "Free") {
				add(&open, ft.URL)
			} else {
				add(&sub, ft.URL)
			}
		}
	}
	return append(open, sub...)
}

// SemanticScholar resolves via the S2 Graph API openAccessPdf field.
// The unauthenticated pool is globally shared and often saturated; the
// resolver is best-effort by design (errors just demote the strategy).
type SemanticScholar struct {
	BaseURL string
	APIKey  string
	Client  *http.Client
}

func NewSemanticScholar(baseURL, apiKey string, timeout time.Duration) *SemanticScholar {
	if baseURL == "" {
		baseURL = "https://api.semanticscholar.org"
	}
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	return &SemanticScholar{BaseURL: baseURL, APIKey: apiKey, Client: &http.Client{Timeout: timeout}}
}

func (s *SemanticScholar) Name() string { return "semantic_scholar" }

type s2PaperResponse struct {
	OpenAccessPDF *struct {
		URL string `json:"url"`
	} `json:"openAccessPdf"`
}

func (s *SemanticScholar) Candidates(ctx context.Context, doi string) ([]string, error) {
	var out s2PaperResponse
	raw := fmt.Sprintf("%s/graph/v1/paper/DOI:%s?fields=openAccessPdf", s.BaseURL, url.PathEscape(doi))
	err := getS2JSON(ctx, s.Client, s.APIKey, raw, &out)
	if err != nil && strings.Contains(err.Error(), "429") {
		// The unauthenticated pool is globally shared and bursts into
		// 429s; one polite backoff recovers most of them.
		select {
		case <-ctx.Done():
		case <-time.After(3 * time.Second):
			err = getJSON(ctx, s.Client, "", raw, &out)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("semantic_scholar: %w", err)
	}
	if out.OpenAccessPDF == nil || strings.TrimSpace(out.OpenAccessPDF.URL) == "" {
		return nil, fmt.Errorf("semantic_scholar: no openAccessPdf")
	}
	return []string{out.OpenAccessPDF.URL}, nil
}
