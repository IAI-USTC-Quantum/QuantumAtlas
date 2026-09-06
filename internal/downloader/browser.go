package downloader

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

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
// Mechanism: the Fetch domain intercepts every response (stage
// Response). PDF-ish responses (PDF/Octet-Stream mime, .pdf in the
// URL, or a Content-Disposition attachment) have their body read over
// CDP before being allowed to continue — this captures both inline
// PDFs and forced downloads without needing the browser's filesystem.
// HTML challenge pages are allowed through so their JavaScript can
// clear; if a PDF never arrives within the budget the last HTML is
// classified for the trace. The captured body still passes the shared
// validation pipeline before the ladder accepts it.

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
	url  string
	body []byte
	kind BodyKind
}

// pdfish reports whether a paused response plausibly carries PDF bytes.
// Content-Type was unreliable in EventRequestPaused across publishers,
// so the URL shape and disposition carry the signal; the captured body
// is classified anyway before acceptance.
func pdfish(url, disposition string) bool {
	u := strings.ToLower(url)
	if strings.Contains(u, ".pdf") || strings.Contains(u, "/pdf/") ||
		strings.HasSuffix(u, "/pdf") || strings.Contains(u, "getpdf") || strings.Contains(u, "pdfft") {
		return true
	}
	if strings.Contains(strings.ToLower(disposition), "attachment") {
		return true
	}
	return false
}

// FetchPDF loads rawURL in a fresh tab and returns a validated PDF
// result.
func (b *BrowserLane) FetchPDF(ctx context.Context, rawURL string) (*FetchResult, error) {
	if !b.Enabled() {
		return nil, ErrBrowserNotConfigured
	}
	body, err := b.navigate(ctx, rawURL)
	if err != nil {
		return nil, fmt.Errorf("browser: %w", err)
	}
	if body.kind != BodyPDF {
		if strings.Contains(strings.ToLower(body.url), "login") || strings.Contains(strings.ToLower(body.url), "seamlessaccess") {
			return nil, fmt.Errorf("browser: %w (publisher requires an authenticated session — sign in once in the browser profile, see downloader.browser.cdp_url deployment notes)", ErrPaywall)
		}
		return nil, fmt.Errorf("browser: %w (final: %s)", classifyBodyErr(body.kind), body.kind)
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
// and navigate it — at most two hops. PDF-ish responses are captured
// passively through the Fetch domain the whole time.
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

	chromedp.ListenTarget(tabCtx, func(ev interface{}) {
		e, ok := ev.(*fetch.EventRequestPaused)
		if !ok || e.Request == nil {
			return
		}
		go func(e *fetch.EventRequestPaused) {
			defer func() { _ = recover() }() // tab may close mid-capture
			url := e.Request.URL
			isResponseStage := e.ResponseStatusCode != 0 || e.ResponseErrorReason != ""
			isDocument := e.ResourceType == "Document"
			disposition := ""
			for _, h := range e.ResponseHeaders {
				if strings.EqualFold(h.Name, "Content-Disposition") {
					disposition = h.Value
				}
			}
			if isResponseStage && (isDocument || pdfish(url, disposition)) {
				var body []byte
				err := chromedp.Run(tabCtx, chromedp.ActionFunc(func(cctx context.Context) error {
					var berr error
					body, berr = fetch.GetResponseBody(e.RequestID).Do(cctx)
					return berr
				}))
				if err == nil && len(body) > 0 {
					switch kind := ClassifyBody(body); kind {
					case BodyPDF:
						select {
						case resultCh <- &browserBody{url: url, body: body, kind: kind}:
						default:
						}
					default:
						if isDocument {
							select {
							case htmlCh <- &browserBody{url: url, body: body, kind: kind}:
							default:
							}
							mu.Lock()
							lastHTML = &browserBody{url: url, body: body, kind: kind}
							mu.Unlock()
						}
					}
				}
			}
			// Always let the request/response continue (even after
			// capturing) so the page's own JS can proceed.
			_ = chromedp.Run(tabCtx, fetch.ContinueRequest(e.RequestID))
		}(e)
	})

	if err := chromedp.Run(tabCtx,
		fetch.Enable().WithPatterns([]*fetch.RequestPattern{
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

	navigateTo := func(u string) {
		if visited[u] || len(visited) >= 8 {
			return
		}
		visited[u] = true
		_ = chromedp.Run(tabCtx, chromedp.ActionFunc(func(cctx context.Context) error {
			_, _, _, err := page.Navigate(u).Do(cctx)
			return err
		}))
	}
	navigateTo(rawURL)

	// Event-driven wait: every HTML document that arrives is mined
	// immediately (Go-side regex, reusing the landing-page extractors)
	// for the next PDF hop — challenge pages self-clear in the browser
	// while we just keep following the links they reveal.
	timer := time.NewTimer(b.cfg.Timeout)
	defer timer.Stop()
	for {
		select {
		case body := <-resultCh:
			return body, nil
		case html := <-htmlCh:
			for _, u := range mineBrowserHTML(html) {
				navigateTo(u)
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

// mineBrowserHTML extracts the next-hop PDF URLs from a document the
// browser loaded — publisher-declared citation_pdf_url first, then
// same-host-ish pdf anchors and the IEEE stamp iframe.
func mineBrowserHTML(doc *browserBody) []string {
	if doc == nil || len(doc.body) == 0 {
		return nil
	}
	html := string(doc.body)
	base := doc.url
	var out []string
	seen := map[string]bool{}
	add := func(raw string) {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			return
		}
		abs := absolutize(htmlUnescape(raw), base)
		if abs == "" || seen[abs] {
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
	for _, m := range jsonPDFURLRe.FindAllStringSubmatch(html, 4) {
		add(m[1])
	}
	for _, m := range pdfHrefRe.FindAllStringSubmatch(html, 12) {
		add(m[1])
	}
	if m := ieeeFrameSrcRe.FindStringSubmatch(html); m != nil {
		add(m[1])
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
