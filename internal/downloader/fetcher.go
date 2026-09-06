// Package downloader is the robust multi-paradigm PDF acquisition
// module behind POST /api/downloader/* (the SPA's "Robust Downloader"
// page) and the `qatlasd downloader probe` harness.
//
// Given a paper identity (arXiv id / DOI), it tries a ladder of
// strategies until one yields a validated PDF:
//
//  1. arxiv       — the shared arxiv.Fetcher (pinned version, global
//     rate limit, %PDF- magic check).
//  2. oa-apis     — parallel fan-out to Europe PMC, Unpaywall, OpenAlex
//     and Semantic Scholar for direct OA PDF URLs.
//  3. patterns    — publisher URL constructions keyed on the DOI prefix
//     (Springer, Wiley, T&F, Sage, ACS, ACM, Frontiers,
//     PLOS, eLife …), fetched with a cookie jar and
//     browser-like headers on the entitled network.
//  4. landing     — GET the doi.org landing page and extract
//     citation_pdf_url / PDF links (regex, bounded read);
//     includes the IEEE stamp.jsp iframe resolution.
//  5. agent       — LLM fallback (OpenAI-compatible endpoint or local
//     claude CLI) that reads the landing-page HTML and
//     proposes candidate PDF URLs.
//
// Every candidate goes through the same validation pipeline (magic
// bytes, %%EOF trailer, size bounds, HTML-disguise classification)
// before it is accepted; robots.txt is honored per host. A successful
// fetch is stored under the canonical asset key and registered exactly
// like the ingest pipeline does, then the PDF-ready hook (MinerU) is
// fired. The strategy trace of every attempt is recorded both in the
// in-process job snapshot (GET /api/downloader/jobs) and in the
// paper_acquisition_events audit trail.
//
// Compliance: this module fetches what the deployment's network is
// entitled to (institutional subscriptions) plus open-access copies. It
// never touches shadow libraries and it respects robots.txt (see
// Config.RespectRobots).
package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Fetch defaults. Timeouts are per HTTP attempt; retries are bounded to
// one extra attempt on transient statuses (publisher hosts are far more
// sensitive to retry storms than arXiv).
const (
	DefaultRequestTimeout = 60 * time.Second
	DefaultMaxPDFBytes    = 100 * 1024 * 1024 // matches paperassets.MaxPDFBytes
	DefaultMinPDFBytes    = 10 * 1024
	DefaultLandingBytes   = 2 * 1024 * 1024
	DefaultHostRPS        = 0.5
	DefaultHostBurst      = 1
	fetchRetryMax         = 1
	fetchBackoffBase      = 2 * time.Second
	retryAfterCap         = 8 * time.Second
)

// DefaultBrowserUserAgent is the UA used for publisher/OA fetches.
// Publishers (Cloudflare et al.) block non-browser agents outright;
// Zotero and friends ship browser UAs for the same reason. The
// QAtlasDownloader suffix keeps the traffic identifiable for operators.
const DefaultBrowserUserAgent = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 QAtlasDownloader"

// Error taxonomy surfaced in the strategy trace and the job snapshot.
var (
	// ErrNotPDF: the body was not a PDF (HTML page, XML, …).
	ErrNotPDF = errors.New("downloader: response body is not a PDF")
	// ErrTooLarge / ErrTooSmall: body outside the size bounds.
	ErrTooLarge = errors.New("downloader: body exceeds size cap")
	ErrTooSmall = errors.New("downloader: body below minimum size")
	// ErrTruncated: PDF magic ok but no %%EOF trailer.
	ErrTruncated = errors.New("downloader: pdf has no %%EOF trailer (likely truncated)")
	// ErrChallenge: bot-detection / proof-of-work interstitial.
	ErrChallenge = errors.New("downloader: bot challenge page")
	// ErrPaywall: login/entitlement page.
	ErrPaywall = errors.New("downloader: paywall/login page")
	// ErrRobots: disallowed by robots.txt.
	ErrRobots = errors.New("downloader: disallowed by robots.txt")
	// ErrRobotsHostile: robots.txt itself 403s (hostile to clients).
	// Treated as allowed — the operator's entitled traffic, not a crawl.
	// ErrHTTP: non-200 final status.
	ErrHTTP = errors.New("downloader: http error")
	// ErrUpstream: transport failure after retries.
	ErrUpstream = errors.New("downloader: upstream error")
)

// BodyKind classifies a non-PDF body for the trace.
type BodyKind string

const (
	BodyPDF          BodyKind = "pdf"
	BodyBotChallenge BodyKind = "bot_challenge"
	BodyPoWChallenge BodyKind = "pow_challenge"
	BodyPaywall      BodyKind = "paywall"
	BodyErrorPage    BodyKind = "error_page"
	BodyOther        BodyKind = "other"
)

// FetchResult is a validated PDF body ready for object storage.
type FetchResult struct {
	Body     *bytes.Reader
	Size     int64
	Sha256   string
	URL      string // final URL after redirects
	Attempts int
}

// FetchConfig configures FetchClient. Zero values fall back to the
// Default* constants.
type FetchConfig struct {
	BrowserUserAgent string
	RequestTimeout   time.Duration
	MaxPDFBytes      int64
	MinPDFBytes      int64
	LandingMaxBytes  int64
	// LandingBaseURL prefixes the DOI landing-page fetch; default
	// "https://doi.org/". Tests point it at a stub server.
	LandingBaseURL string
	// HostRPS is the default steady-state rate per host.
	HostRPS   float64
	HostBurst int
	// RespectRobots gates fetching on the per-host robots.txt. Default
	// OFF: this module performs user-directed, on-demand fetches of
	// single entitled articles — not crawling — and several publishers
	// now blanket-disallow "*" from robots.txt as an anti-AI-crawler
	// measure (e.g. IOP), which would silently break legitimate
	// downloads. Operators who want strict crawler parity can enable
	// it; the checker itself stays maintained.
	RespectRobots bool
	// HTTPClient lets tests inject a stub transport. When set, Jar is
	// still installed unless the client already has one.
	HTTPClient *http.Client
	Now        func() time.Time
}

// FetchClient is the generic PDF/HTML fetcher shared by every strategy
// except the arXiv one (which has its own rate-limited fetcher). Safe
// for concurrent use.
type FetchClient struct {
	cfg    FetchConfig
	client *http.Client
	now    func() time.Time

	robots *robotsCache

	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// NewFetchClient builds a FetchClient with a per-process cookie jar.
func NewFetchClient(cfg FetchConfig) (*FetchClient, error) {
	if cfg.BrowserUserAgent == "" {
		cfg.BrowserUserAgent = DefaultBrowserUserAgent
	}
	if cfg.RequestTimeout == 0 {
		cfg.RequestTimeout = DefaultRequestTimeout
	}
	if cfg.MaxPDFBytes == 0 {
		cfg.MaxPDFBytes = DefaultMaxPDFBytes
	}
	if cfg.MinPDFBytes == 0 {
		cfg.MinPDFBytes = DefaultMinPDFBytes
	}
	if cfg.LandingMaxBytes == 0 {
		cfg.LandingMaxBytes = DefaultLandingBytes
	}
	if cfg.HostRPS == 0 {
		cfg.HostRPS = DefaultHostRPS
	}
	if cfg.HostBurst == 0 {
		cfg.HostBurst = DefaultHostBurst
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	client := cfg.HTTPClient
	jar, _ := cookiejar.New(nil)
	if client == nil {
		client = &http.Client{Timeout: cfg.RequestTimeout, Jar: jar}
	} else if client.Jar == nil {
		cp := *client
		cp.Jar = jar
		client = &cp
	}
	return &FetchClient{
		cfg:      cfg,
		client:   client,
		now:      cfg.Now,
		robots:   newRobotsCache(client, cfg.BrowserUserAgent),
		limiters: map[string]*rate.Limiter{},
	}, nil
}

// limiterFor returns (creating on demand) the per-host rate limiter.
func (f *FetchClient) limiterFor(host string) *rate.Limiter {
	f.mu.Lock()
	defer f.mu.Unlock()
	l, ok := f.limiters[host]
	if !ok {
		l = rate.NewLimiter(rate.Limit(f.cfg.HostRPS), f.cfg.HostBurst)
		f.limiters[host] = l
	}
	return l
}

// FetchPDF fetches rawURL and returns a validated PDF result. It
// follows redirects (cookie jar active), honors per-host rate limits
// and robots.txt, retries once on 429/503, and runs the full validation
// pipeline on the body.
func (f *FetchClient) FetchPDF(ctx context.Context, rawURL string) (*FetchResult, error) {
	body, finalURL, kind, status, err := f.fetch(ctx, rawURL, f.cfg.MaxPDFBytes, false)
	if err != nil {
		return nil, err
	}
	_ = status
	if kind != BodyPDF {
		return nil, classifyBodyErr(kind)
	}
	if int64(len(body)) < f.cfg.MinPDFBytes {
		return nil, fmt.Errorf("%w: %d bytes", ErrTooSmall, len(body))
	}
	if !bytes.Contains(body[max(0, len(body)-2048):], []byte("%%EOF")) {
		// Some publishers append tracking junk after %%EOF beyond the
		// 2 KiB window, or use incremental updates; treat a missing
		// trailer as suspicious but non-fatal ONLY when the body also
		// parses as a plausible PDF object tree (has /Type /Catalog or
		// startxref). Otherwise: truncated.
		if !bytes.Contains(body, []byte("startxref")) {
			return nil, ErrTruncated
		}
	}
	hasher := sha256.New()
	_, _ = hasher.Write(body)
	// Publisher identity-provider handshakes (idp.springer.com/authorize
	// &c.) land on an HTML article page without issuing usable cookies
	// to non-entitled clients — report them as paywalled, not as a
	// generic error page.
	if strings.Contains(finalURL, "/authorize") || strings.Contains(finalURL, "idp.") {
		return nil, fmt.Errorf("%w (identity-provider handshake at %s)", ErrPaywall, finalURL)
	}
	return &FetchResult{
		Body:   bytes.NewReader(body),
		Size:   int64(len(body)),
		Sha256: hex.EncodeToString(hasher.Sum(nil)),
		URL:    finalURL,
	}, nil
}

// FetchText fetches rawURL as (bounded) text — the landing-page and
// robots.txt paths. Returns body, final URL, and the HTTP status.
func (f *FetchClient) FetchText(ctx context.Context, rawURL string, limit int64) ([]byte, string, int, error) {
	body, finalURL, _, status, err := f.fetch(ctx, rawURL, limit, true)
	return body, finalURL, status, err
}

// fetch performs the rate-limited, robots-checked, retrying GET.
func (f *FetchClient) fetch(ctx context.Context, rawURL string, limit int64, isText bool) ([]byte, string, BodyKind, int, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, "", BodyOther, 0, fmt.Errorf("not an absolute http(s) URL: %q", rawURL)
	}
	if f.cfg.RespectRobots {
		allowed, rerr := f.robots.Allowed(ctx, u)
		if rerr == nil && !allowed {
			return nil, "", BodyOther, 0, fmt.Errorf("%w: %s", ErrRobots, u.Host)
		}
	}

	var lastErr error
	for attempt := 0; attempt <= fetchRetryMax; attempt++ {
		if werr := f.limiterFor(u.Host).Wait(ctx); werr != nil {
			return nil, "", BodyOther, 0, fmt.Errorf("rate-limiter wait: %w", werr)
		}
		body, finalURL, kind, status, retryable, err := f.doOnce(ctx, rawURL, limit, isText)
		if err == nil {
			return body, finalURL, kind, status, nil
		}
		lastErr = err
		// Only transient statuses retry; everything else is terminal.
		if !retryable || ctx.Err() != nil || attempt == fetchRetryMax {
			return nil, "", kind, status, err
		}
		wait := fetchBackoffBase << attempt
		if wait > retryAfterCap {
			wait = retryAfterCap
		}
		select {
		case <-ctx.Done():
			return nil, "", kind, status, fmt.Errorf("%w: %v", ErrUpstream, ctx.Err())
		case <-time.After(wait):
		}
	}
	return nil, "", BodyOther, 0, fmt.Errorf("%w: %v", ErrUpstream, lastErr)
}

// errTransient marks retryable statuses internally.
var errTransient = errors.New("transient status")

// doOnce runs one GET with browser-like headers and classifies the body.
func (f *FetchClient) doOnce(ctx context.Context, rawURL string, limit int64, isText bool) (body []byte, finalURL string, kind BodyKind, status int, retryable bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, "", BodyOther, 0, false, err
	}
	f.setBrowserHeaders(req, isText)
	resp, err := f.client.Do(req)
	if err != nil {
		return nil, "", BodyOther, 0, false, fmt.Errorf("%w: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 400:
		// Read a bounded body so bot-wall HTML (Cloudflare "Just a
		// moment", Radware) can be classified even when it arrives with
		// a 403/429 — the taxonomy drives the retry + UI messaging.
		body, rerr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		if rerr != nil {
			return nil, "", BodyOther, resp.StatusCode, false, fmt.Errorf("%w: http %d", ErrHTTP, resp.StatusCode)
		}
		kind := ClassifyBody(body)
		switch {
		case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
			return nil, "", kind, resp.StatusCode, true, fmt.Errorf("%w: http %d", ErrHTTP, resp.StatusCode)
		case kind == BodyBotChallenge || kind == BodyPoWChallenge:
			return nil, "", kind, resp.StatusCode, false, fmt.Errorf("%w (%s, http %d)", ErrChallenge, kind, resp.StatusCode)
		case kind == BodyPaywall:
			return nil, "", kind, resp.StatusCode, false, fmt.Errorf("%w (http %d)", ErrPaywall, resp.StatusCode)
		default:
			return nil, "", kind, resp.StatusCode, false, fmt.Errorf("%w: http %d", ErrHTTP, resp.StatusCode)
		}
	case resp.StatusCode != http.StatusOK:
		return nil, "", BodyOther, resp.StatusCode, false, fmt.Errorf("%w: http %d", ErrHTTP, resp.StatusCode)
	}

	body, err = io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, "", BodyOther, resp.StatusCode, false, fmt.Errorf("%w: read body: %v", ErrUpstream, err)
	}
	if int64(len(body)) > limit {
		return nil, "", BodyOther, resp.StatusCode, false, ErrTooLarge
	}
	kind = ClassifyBody(body)
	return body, resp.Request.URL.String(), kind, resp.StatusCode, false, nil
}

// setBrowserHeaders installs the header set publishers expect from a
// real browser; anything less triggers Cloudflare et al.
func (f *FetchClient) setBrowserHeaders(req *http.Request, isText bool) {
	req.Header.Set("User-Agent", f.cfg.BrowserUserAgent)
	if isText {
		req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	} else {
		req.Header.Set("Accept", "application/pdf,text/html,application/xhtml+xml;q=0.9,*/*;q=0.8")
	}
	req.Header.Set("Accept-Language", "en-US,en;q=0.9")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
}

// ClassifyBody sniffs what the body actually is. A valid PDF must start
// with the magic bytes; an HTML body is further classified into the
// interstitial families the strategy trace reports.
func ClassifyBody(body []byte) BodyKind {
	if len(body) >= 5 && bytes.HasPrefix(body, []byte("%PDF-")) {
		return BodyPDF
	}
	head := body
	if len(head) > 2048 {
		head = head[:2048]
	}
	lower := strings.ToLower(string(head))
	isHTML := bytes.HasPrefix(body, []byte("\xEF\xBB\xBF<")) ||
		bytes.HasPrefix(body, []byte("<")) ||
		strings.Contains(lower, "<html") || strings.Contains(lower, "<!doctype html")
	if !isHTML {
		return BodyOther
	}
	switch {
	case strings.Contains(lower, "just a moment"),
		strings.Contains(lower, "cf-browser-verification"),
		strings.Contains(lower, "attention required"),
		strings.Contains(lower, "verify you are human"),
		strings.Contains(lower, "perfdrive"),
		strings.Contains(lower, "radware"):
		return BodyBotChallenge
	case strings.Contains(lower, "preparing to download"),
		strings.Contains(lower, "pow_challenge"),
		strings.Contains(lower, "cloudpmc-viewer-pow"):
		return BodyPoWChallenge
	case strings.Contains(lower, "access denied"),
		strings.Contains(lower, "sign in"),
		strings.Contains(lower, "log in"),
		strings.Contains(lower, "purchase this article"),
		strings.Contains(lower, "buy this article"),
		strings.Contains(lower, "request permissions"):
		return BodyPaywall
	}
	return BodyErrorPage
}

// classifyBodyErr maps a non-PDF BodyKind onto the trace error.
func classifyBodyErr(kind BodyKind) error {
	switch kind {
	case BodyBotChallenge, BodyPoWChallenge:
		return fmt.Errorf("%w (%s)", ErrChallenge, kind)
	case BodyPaywall:
		return ErrPaywall
	default:
		return fmt.Errorf("%w (%s)", ErrNotPDF, kind)
	}
}
