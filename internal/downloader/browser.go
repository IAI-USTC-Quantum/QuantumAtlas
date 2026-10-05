package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/fetch"
	"github.com/chromedp/cdproto/page"
	"github.com/chromedp/chromedp"
)

// BrowserLane fetches PDFs through a real Chromium driven over the
// Chrome DevTools Protocol — the last resort for publishers whose bot
// walls (Cloudflare "Just a moment", IEEE's 202 gateway) defeat every
// plain-HTTP strategy. The browser itself is NOT embedded: qatlasd
// connects to an already-running Chromium via BrowserConfig.CDPURL (a
// chromedp/headless-shell sidecar next to qatlasd in production, or any
// local Chrome started with --remote-debugging-port on a dev box).
// Publisher logins/cookies live in THAT browser's user-data-dir — see
// the deployment notes in config.example.yaml.
//
// Mechanism: the Fetch domain observes requests and responses, but reads bodies
// ONLY for the actively selected MAIN-FRAME navigation and its bounded,
// server-declared HTTP redirect chain. URL suffixes, iframe documents, preloads,
// and arbitrary script downloads cannot authorize a capture. This covers inline
// PDFs and forced downloads without needing the browser's filesystem. HTML
// challenge pages continue so JavaScript can clear; if a PDF never arrives, the
// last eligible HTML is classified for the trace. Format validation here is NOT
// identity proof: the ladder must also call VerifyPublishedPDF before accepting
// a published asset.

// ErrBrowserNotConfigured is returned when the lane is disabled.
var ErrBrowserNotConfigured = errors.New("downloader: browser lane not configured")

// ErrBrowserTimeout when no PDF arrived within the budget.
var ErrBrowserTimeout = errors.New("downloader: browser lane timed out waiting for the PDF")

// BrowserConfig configures the CDP-connected browser lane.
type BrowserConfig struct {
	// CDPURL is the ws:// (or http://, auto-upgraded) DevTools endpoint
	// of an ALREADY-RUNNING Chromium, e.g. "ws://browser:9222" for the
	// compose sidecar or "http://127.0.0.1:9222" for a dev Chrome.
	// Empty disables the lane.
	CDPURL string
	// Timeout bounds the WHOLE navigation loop (up to 3 hops).
	// Default 45s.
	Timeout time.Duration
	// MaxPDFBytes caps the captured body. Default 100 MiB.
	MaxPDFBytes int64
}

// BrowserLane drives the remote Chromium. Safe for concurrent use at
// the lane level; each call opens its own tab.
type BrowserLane struct {
	cfg BrowserConfig
}

// NewBrowserLane builds the lane; empty CDPURL returns nil (disabled).
func NewBrowserLane(cfg BrowserConfig) *BrowserLane {
	if strings.TrimSpace(cfg.CDPURL) == "" {
		return nil
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 45 * time.Second
	}
	if cfg.MaxPDFBytes == 0 {
		cfg.MaxPDFBytes = DefaultMaxPDFBytes
	}
	return &BrowserLane{cfg: cfg}
}

// Enabled reports whether the lane is wired.
func (b *BrowserLane) Enabled() bool { return b != nil && b.cfg.CDPURL != "" }

// browserBody is what one navigation produced.
type browserBody struct {
	baseURI    string // resolution base, never the authoritative page host
	navigation uint64 // actively selected navigation generation
	url        string
	body       []byte
	kind       BodyKind
	status     int // observed CDP response status
}

// FetchPDF loads rawURL in a fresh tab and returns a validated PDF
// result.
func (b *BrowserLane) FetchPDF(ctx context.Context, rawURL string) (*FetchResult, error) {
	if !b.Enabled() {
		return nil, ErrBrowserNotConfigured
	}
	// Human pacing: the lane is an assistant doing one navigation on the
	// user's behalf, not a crawler — a short randomized think-time before
	// touching the publisher keeps the cadence unmistakably human.
	pause := 1500 + rand.Intn(1500)
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(time.Duration(pause) * time.Millisecond):
	}
	body, err := b.navigate(ctx, rawURL)
	if err != nil {
		return nil, withDiagnostic(fmt.Errorf("browser: %w", err), "browser", 0, nil)
	}
	return b.validateBody(body)
}

func (b *BrowserLane) validateBody(body *browserBody) (result *FetchResult, err error) {
	defer func() { err = withDiagnostic(err, "browser", body.status, body.body) }()
	if body.kind != BodyPDF {
		if strings.Contains(strings.ToLower(body.url), "login") || strings.Contains(strings.ToLower(body.url), "seamlessaccess") {
			return nil, fmt.Errorf("browser: %w (publisher requires an authenticated session — sign in once in the browser profile, see downloader.browser.cdp_url deployment notes)", ErrPaywall)
		}
		return nil, fmt.Errorf("browser: %w (final: %s)", classifyBodyErr(body.kind), body.kind)
	}
	if body.status != 0 && body.status != http.StatusOK {
		return nil, fmt.Errorf("browser: %w (status=%d)", ErrHTTP, body.status)
	}
	if int64(len(body.body)) > b.cfg.MaxPDFBytes {
		return nil, ErrTooLarge
	}
	if !bytes.Contains(body.body[max(0, len(body.body)-2048):], []byte("%%EOF")) &&
		!bytes.Contains(body.body, []byte("startxref")) {
		return nil, ErrTruncated
	}
	hasher := sha256.New()
	_, _ = hasher.Write(body.body)
	return &FetchResult{
		Body:   bytes.NewReader(body.body),
		Size:   int64(len(body.body)),
		Sha256: hex.EncodeToString(hasher.Sum(nil)),
		URL:    body.url,
	}, nil
}

// navigate runs the observed-navigation loop: load the target, give
// challenge JavaScript a window to clear, then actively mine the
// article page for the real PDF link (citation_pdf_url / pdf anchors)
// and navigate one selected candidate at a time — at most eight selections.
// Only responses authorized by that active navigation may supply bytes.
func (b *BrowserLane) navigate(ctx context.Context, rawURL string) (*browserBody, error) {
	ctx, cancel := context.WithTimeout(ctx, b.cfg.Timeout)
	defer cancel()

	allocCtx, allocCancel := chromedp.NewRemoteAllocator(ctx, b.cfg.CDPURL)
	defer allocCancel()
	tabCtx, tabCancel := chromedp.NewContext(allocCtx)
	defer tabCancel()

	resultCh := make(chan *browserBody, 8)
	htmlCh := make(chan *browserBody, 8)
	var mu sync.Mutex
	var lastHTML *browserBody

	scope := &browserNavigationScope{}
	var mainFrame cdp.FrameID
	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		e, ok := ev.(*fetch.EventRequestPaused)
		if !ok || e.Request == nil {
			return
		}
		// Observe synchronously: a redirect must be recorded before its
		// response is continued and before the child request arrives.
		permit, capture := scope.observe(e)
		go func(e *fetch.EventRequestPaused) {
			defer func() { _ = recover() }() // tab may close mid-capture
			defer func() { _ = chromedp.Run(tabCtx, fetch.ContinueRequest(e.RequestID)) }()
			if !capture || !scope.current(permit) {
				return
			}
			var body []byte
			err := chromedp.Run(tabCtx, chromedp.ActionFunc(func(cctx context.Context) error {
				var berr error
				body, berr = fetch.GetResponseBody(e.RequestID).Do(cctx)
				return berr
			}))
			if err != nil || len(body) == 0 || !scope.current(permit) {
				return
			}
			captured := &browserBody{url: e.Request.URL, body: body, kind: ClassifyBody(body), status: int(e.ResponseStatusCode), navigation: permit.generation}
			if debugBrowser {
				log.Printf("[browserlane] BODY kind=%s len=%d url=%.100s", captured.kind, len(body), captured.url)
			}
			if captured.kind == BodyPDF {
				select {
				case resultCh <- captured:
				default:
				}
			} else {
				mu.Lock()
				lastHTML = captured
				mu.Unlock()
				select {
				case htmlCh <- captured:
				default:
				}
			}
		}(e)
	})

	if err := chromedp.Run(tabCtx,
		chromedp.ActionFunc(func(cctx context.Context) error {
			tree, err := page.GetFrameTree().Do(cctx)
			if err != nil {
				return err
			}
			if tree == nil || tree.Frame == nil {
				return errors.New("browser: missing main frame")
			}
			mainFrame = tree.Frame.ID
			return nil
		}),
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{
			{RequestStage: "Request"},
			{RequestStage: "Response"},
		}),
		// Stealth hardening for Cloudflare-managed challenges: the
		// automation flags (navigator.webdriver etc.) are the cheapest
		// signals a bot wall checks. The sidecar should ALSO be started
		// with --disable-blink-features=AutomationControlled; this
		// script covers what a remote allocator cannot set via flags.
		chromedp.ActionFunc(func(cctx context.Context) error {
			_, err := page.AddScriptToEvaluateOnNewDocument(stealthJS).Do(cctx)
			return err
		}),
	); err != nil {
		return nil, err
	}

	visited := map[string]bool{}

	navigateTo := func(u string) bool {
		u = browserHTTPURL(u)
		if u == "" || visited[u] || len(visited) >= 8 || !scope.selectNavigation(mainFrame, u) {
			return false
		}
		visited[u] = true
		if debugBrowser {
			log.Printf("[browserlane] NAV %.120s", u)
		}
		_ = chromedp.Run(tabCtx, chromedp.ActionFunc(func(cctx context.Context) error {
			_, _, _, err := page.Navigate(u).Do(cctx)
			return err
		}))
		return true
	}
	navigateTo(rawURL)

	// Event-driven wait: only eligible main-frame HTML may reveal a
	// next hop. SPA pages get a bounded render window before selection.
	timer := time.NewTimer(b.cfg.Timeout)
	defer timer.Stop()
	for {
		select {
		case body := <-resultCh:
			if scope.currentBody(body) {
				return body, nil
			}
		case html := <-htmlCh:
			if !scope.currentBody(html) {
				continue
			}
			// SPA metadata and dynamic <base> need a live snapshot. Never
			// mine an unrelated JS navigation that replaced this document.
			renderWait := time.NewTimer(1200 * time.Millisecond)
			select {
			case <-renderWait.C:
			case <-ctx.Done():
				renderWait.Stop()
				continue
			}
			if !scope.currentBody(html) {
				continue
			}
			candidates := mineBrowserHTML(html)
			if rendered := b.renderedDocument(tabCtx); rendered != nil && browserHTTPURL(rendered.url) == browserHTTPURL(html.url) {
				if live := mineBrowserHTML(rendered); len(live) > 0 {
					candidates = live
				}
			}
			// One explicit selection at a time. Racing all candidates
			// would otherwise accept bytes from whichever reference won.
			for _, u := range candidates {
				if navigateTo(u) {
					break
				}
			}
		case <-timer.C:
			mu.Lock()
			defer mu.Unlock()
			if lastHTML != nil {
				return lastHTML, nil
			}
			return nil, ErrBrowserTimeout
		case <-ctx.Done():
			mu.Lock()
			defer mu.Unlock()
			if lastHTML != nil {
				return lastHTML, nil
			}
			return nil, ErrBrowserTimeout
		}
	}
}

// mineRenderedDOM extracts PDF links from the CURRENT rendered DOM —
// the complement to mineBrowserHTML (which sees the raw response body).
func (b *BrowserLane) mineRenderedDOM(tabCtx context.Context) []string {
	return mineBrowserHTML(b.renderedDocument(tabCtx))
}

func (b *BrowserLane) renderedDocument(tabCtx context.Context) *browserBody {
	var doc struct {
		URL     string `json:"url"`
		BaseURI string `json:"baseURI"`
		HTML    string `json:"html"`
	}
	// URL identifies the page's host; baseURI only resolves relative links.
	if err := chromedp.Run(tabCtx, chromedp.Evaluate(renderedDocumentJS, &doc)); err != nil {
		return nil
	}
	return &browserBody{url: doc.URL, baseURI: doc.BaseURI, body: []byte(doc.HTML)}
}

// Bound the snapshot like FetchText's landing-page read. The browser remains
// responsible for navigating and rendering; candidate selection stays in Go.
const renderedDocumentJS = `(function(){
	return {
		url: document.URL,
		baseURI: document.baseURI,
		html: document.documentElement ? document.documentElement.outerHTML.slice(0, 2 * 1024 * 1024) : ''
	};
})()`

// Preserve publisher download endpoints previously recognized only by the
// rendered-DOM miner, while applying the same host restrictions as .pdf links.
var browserPDFHrefRe = regexp.MustCompile(`(?is)<a\b[^>]*\bhref=["']([^"'\s<>]*(?:\.pdf(?:\?[^"'\s<>]*)?|/pdf(?:[/?][^"'\s<>]*)?|pdf-direct[^"'\s<>]*|pdfft[^"'\s<>]*|getPDF[^"'\s<>]*|stamp\.jsp[^"'\s<>]*))["']`)

// mineBrowserHTML extracts article-scoped next-hop PDF candidates. Explicit
// citation metadata is authoritative (including CDN links); without it, only
// same-host inline PDF metadata/IEEE frames, then same-host anchors, are used.
// These are candidate guards, not proof that a PDF's contents match the work.
func mineBrowserHTML(doc *browserBody) []string {
	if doc == nil || len(doc.body) == 0 {
		return nil
	}
	if debugBrowser {
		log.Printf("[browserlane] MINE url=%.90s len=%d", doc.url, len(doc.body))
	}
	html := string(doc.body)
	base := doc.baseURI
	if base == "" {
		base = documentBaseURL(doc.body, doc.url)
	}
	// Weak links are compared AFTER base resolution against the actual
	// article host, never against a potentially external <base> host.
	samePageHost := func(raw string) bool {
		return sameHostOrRelative(absolutize(raw, base), doc.url)
	}
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		abs := absolutize(htmlUnescape(raw), base)
		u, err := url.Parse(abs)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || seen[abs] {
			return
		}
		seen[abs] = true
		out = append(out, abs)
	}
	for _, re := range []*regexp.Regexp{citationPDFURLRe, citationPDFURLRe2} {
		if m := re.FindStringSubmatch(html); m != nil {
			add(m[1])
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, m := range jsonPDFURLRe.FindAllStringSubmatch(html, 4) {
		if samePageHost(htmlUnescape(m[1])) {
			add(m[1])
		}
	}
	if m := ieeeFrameSrcRe.FindStringSubmatch(html); m != nil {
		if samePageHost(htmlUnescape(m[1])) {
			add(m[1])
		}
	}
	if len(out) > 0 {
		return out
	}
	for _, m := range browserPDFHrefRe.FindAllStringSubmatch(html, 12) {
		raw := htmlUnescape(m[1])
		if !strings.Contains(strings.ToLower(raw), "epdf") && samePageHost(raw) {
			add(m[1])
		}
	}
	return out
}

// stealthJS neutralizes the commonest headless/automation tells before
// any page script runs. Deliberately small: mirrors what
// --disable-blink-features=AutomationControlled covers plus the
// webdriver/plugins/languages properties that flag sets.
const stealthJS = `
Object.defineProperty(navigator, 'webdriver', {get: () => undefined});
Object.defineProperty(navigator, 'plugins', {get: () => [1, 2, 3, 4, 5]});
Object.defineProperty(navigator, 'languages', {get: () => ['en-US', 'en']});
window.chrome = window.chrome || {runtime: {}};
`

// debugBrowser enables verbose lane logging (env DL_BROWSER_DEBUG).
var debugBrowser = os.Getenv("DL_BROWSER_DEBUG") != ""
