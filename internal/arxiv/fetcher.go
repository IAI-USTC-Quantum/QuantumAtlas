// Package arxiv is the server-side fetcher for arXiv PDFs, used by the
// silent paper-access pipeline (plan §4 Phase C).
//
// Scope:
//   - one entry point: Fetcher.Fetch(ctx, ParsedArxivID) → (bytes, sha256, err)
//   - global token-bucket rate limit (default 4 req/s) so the server
//     never hammers arxiv.org regardless of how many converter goroutines
//     are in flight
//   - bounded retry with exponential backoff for 429/5xx, honouring the
//     Retry-After header per RFC 7231 §7.1.3 (both seconds and HTTP-date)
//   - size cap and %PDF- magic byte check before returning bytes
//   - User-Agent of the form `qatlasd/<version> (mailto:<contact>)` so
//     arxiv can identify our traffic and contact us if we misbehave
//     (arxiv.org/help/bulk_data#etiquette explicitly asks for this)
//
// NOT scope:
//   - object-store write — caller (converter) writes with sha256
//     metadata + IfNoneMatch:"*" idempotency
//   - retry queueing across converter restarts — caller manages job state
//   - DOI / metadata lookup — see internal/openalex
//
// The fetcher only ever issues GET against arxiv.org/pdf/<id>; it does
// NOT crawl the abs page or follow redirects to mirrors.

package arxiv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"

	"golang.org/x/time/rate"
)

// DefaultBaseURL is arxiv.org's canonical PDF endpoint. We always hit
// `<base>/<full-id>` (versioned) because that URL is byte-immutable per
// arxiv policy — required so that a client that received pdf_sha256
// from upload-mineru can verify the same bytes by re-fetching.
const DefaultBaseURL = "https://arxiv.org/pdf/"

// Defaults conservatively chosen so a misconfigured deployment can't
// accidentally DoS arxiv.org. arxiv's bulk-data etiquette page suggests
// ≤4 req/s; we default lower (rps=2, burst=2) and let operators raise
// it via QATLAS_ARXIV_FETCH_RPS if their workload genuinely needs it.
const (
	DefaultRPS          = 2
	DefaultBurst        = 2
	DefaultMaxBytes     = 100 * 1024 * 1024 // matches paperassets.MaxPDFBytes
	DefaultHTTPTimeout  = 60 * time.Second
	DefaultRetryMax     = 3
	DefaultRetryMinWait = 1 * time.Second
	DefaultRetryMaxWait = 30 * time.Second // hard cap on a single backoff sleep
)

// Errors that callers may want to branch on. All are errors.Is-compatible.
var (
	// ErrNotFound is returned when arxiv responds 404 (no such paper /
	// no such version). Fatal — retrying will not change the answer.
	ErrNotFound = errors.New("arxiv: paper not found")
	// ErrRateLimited is returned when arxiv responds 429 and the
	// retry budget is exhausted. Operator should reduce
	// QATLAS_ARXIV_FETCH_RPS.
	ErrRateLimited = errors.New("arxiv: rate-limited (retry budget exhausted)")
	// ErrTooLarge is returned when the response body exceeds MaxBytes.
	ErrTooLarge = errors.New("arxiv: pdf exceeds configured size cap")
	// ErrNotPDF is returned when the response body doesn't start with
	// the `%PDF-` magic bytes (arxiv occasionally serves an HTML
	// error page with status 200).
	ErrNotPDF = errors.New("arxiv: response body is not a PDF")
	// ErrUpstream wraps any transport / 5xx error that exhausted
	// retries.
	ErrUpstream = errors.New("arxiv: upstream error")
)

// Result is what Fetch returns on success. Body is a *bytes.Reader so
// callers can rewind without buffering twice (the body is already
// fully materialized for size + magic checks).
type Result struct {
	// Body is the PDF bytes ready to be written to object store.
	Body *bytes.Reader
	// Size is len(body) — convenient for objstore.Put which wants the
	// exact byte count.
	Size int64
	// Sha256 is the lower-case hex digest, computed inline during
	// read. Caller should attach this to object metadata so the
	// upload-mineru handler's pdf_sha256 verification stays consistent.
	Sha256 string
	// Attempts is how many HTTP round-trips the fetch consumed
	// (1 + number of retries). Useful for logging.
	Attempts int
}

// Config configures a Fetcher. Zero values fall back to the Default*
// constants above.
type Config struct {
	// BaseURL is the arxiv PDF prefix; default DefaultBaseURL.
	BaseURL string
	// AbsBaseURL is the arxiv abstract-page prefix used by
	// ResolveLatestVersion. Default `https://arxiv.org/abs/`. Tests
	// inject a stub server URL here so they don't hit production.
	AbsBaseURL string
	// UserAgent identifies our traffic. SHOULD include a mailto so
	// arxiv ops can reach the deployer if our traffic misbehaves; the
	// converter constructor builds this from QATLAS_OPENALEX_MAILTO
	// (re-used as the deployment contact email).
	UserAgent string
	// RPS is the steady-state request rate against arxiv.org. Default
	// DefaultRPS.
	RPS int
	// Burst is the rate-limiter burst capacity. Default DefaultBurst.
	Burst int
	// MaxBytes caps the response body size before %PDF- check. A 100
	// MiB body that is genuinely a PDF returns ErrTooLarge — arxiv
	// PDFs above this size are exceedingly rare and almost certainly
	// the wrong endpoint resolved.
	MaxBytes int64
	// HTTPTimeout is the per-attempt timeout (header + body).
	HTTPTimeout time.Duration
	// RetryMax is the maximum number of retries on 429/5xx.
	RetryMax int
	// HTTPClient lets tests inject a stub. Defaults to a new
	// http.Client with Timeout=HTTPTimeout when nil.
	HTTPClient *http.Client
	// Now lets tests fast-forward retry timing. Defaults to time.Now.
	Now func() time.Time
}

// Fetcher is the actual fetcher. Safe for concurrent use by multiple
// goroutines — the rate.Limiter handles per-call serialization, the
// http.Client is concurrency-safe, and there's no other mutable state.
type Fetcher struct {
	cfg     Config
	limiter *rate.Limiter
	client  *http.Client
	now     func() time.Time
}

// New constructs a Fetcher from cfg, applying defaults for zero fields.
// Returns an error only when the supplied config is internally
// inconsistent (e.g. negative RPS); the typical use is
// `must(arxiv.New(cfg))` in main.go.
func New(cfg Config) (*Fetcher, error) {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.RPS == 0 {
		cfg.RPS = DefaultRPS
	}
	if cfg.Burst == 0 {
		cfg.Burst = DefaultBurst
	}
	if cfg.MaxBytes == 0 {
		cfg.MaxBytes = DefaultMaxBytes
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = DefaultHTTPTimeout
	}
	if cfg.RetryMax == 0 {
		cfg.RetryMax = DefaultRetryMax
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.RPS < 0 || cfg.Burst < 1 || cfg.MaxBytes < 1024 || cfg.HTTPTimeout < 0 {
		return nil, fmt.Errorf("arxiv: invalid config: rps=%d burst=%d max=%d timeout=%v",
			cfg.RPS, cfg.Burst, cfg.MaxBytes, cfg.HTTPTimeout)
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	return &Fetcher{
		cfg:     cfg,
		limiter: rate.NewLimiter(rate.Limit(cfg.RPS), cfg.Burst),
		client:  cfg.HTTPClient,
		now:     cfg.Now,
	}, nil
}

// Fetch downloads the PDF for p. p MUST have a non-empty Version
// (caller's contract; arxiv's versioned URL is what gives us byte
// immutability). For old-style bare ids the URL points at the legacy
// `arxiv.org/pdf/<bare>v<n>` which arxiv 404s — caller is responsible
// for disambiguating bare → canonical before calling Fetch.
//
// Returns ErrNotFound on 404 (fatal), ErrRateLimited on persistent 429,
// ErrTooLarge on oversize body, ErrNotPDF on wrong magic, ErrUpstream
// wrapping the transport / 5xx error after retries are exhausted.
func (f *Fetcher) Fetch(ctx context.Context, p paperassets.ParsedArxivID) (*Result, error) {
	if p.Version == "" {
		return nil, fmt.Errorf("arxiv: fetch requires versioned id, got %q", p.Canonical)
	}

	url := f.cfg.BaseURL + p.Canonical
	var lastErr error
	for attempt := 1; attempt <= f.cfg.RetryMax+1; attempt++ {
		if err := f.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("arxiv: rate-limiter wait: %w", err)
		}

		res, retryAfter, err := f.doOnce(ctx, url)
		if err == nil {
			res.Attempts = attempt
			return res, nil
		}
		lastErr = err

		// Fatal — don't retry.
		if errors.Is(err, ErrNotFound) || errors.Is(err, ErrTooLarge) || errors.Is(err, ErrNotPDF) {
			return nil, err
		}
		// Context killed mid-flight — propagate without further retry.
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %v (after %d attempt(s))", ErrUpstream, err, attempt)
		}
		// Out of retries — surface the right sentinel.
		if attempt > f.cfg.RetryMax {
			if errors.Is(err, ErrRateLimited) {
				return nil, ErrRateLimited
			}
			return nil, fmt.Errorf("%w: %v (after %d attempt(s))", ErrUpstream, err, attempt)
		}

		// Backoff: honour Retry-After if the server gave one, else
		// exponential with jitter-free cap.
		wait := retryAfter
		if wait <= 0 {
			wait = DefaultRetryMinWait << (attempt - 1) // 1s, 2s, 4s, ...
			if wait > DefaultRetryMaxWait {
				wait = DefaultRetryMaxWait
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("%w: %v (cancelled during backoff)", ErrUpstream, ctx.Err())
		case <-time.After(wait):
		}
	}
	return nil, fmt.Errorf("%w: %v", ErrUpstream, lastErr)
}

// doOnce performs one HTTP round-trip and validates the body. Returns
// (nil, retryAfter, err) on retryable failure; (nil, 0, ErrNotFound|
// ErrTooLarge|ErrNotPDF) on fatal failure; (*Result, 0, nil) on success.
func (f *Fetcher) doOnce(ctx context.Context, url string) (*Result, time.Duration, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("build request: %w", err)
	}
	if ua := f.cfg.UserAgent; ua != "" {
		req.Header.Set("User-Agent", ua)
	}
	req.Header.Set("Accept", "application/pdf")

	resp, err := f.client.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, 0, ErrNotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, parseRetryAfter(resp.Header.Get("Retry-After"), f.now()), fmt.Errorf("%w: 429", ErrRateLimited)
	case resp.StatusCode >= 500:
		return nil, parseRetryAfter(resp.Header.Get("Retry-After"), f.now()), fmt.Errorf("http %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		// Anything else (4xx other than 404 / 429) is treated as fatal
		// upstream — retrying won't change a 403 or a 410.
		return nil, 0, fmt.Errorf("%w: http %d", ErrUpstream, resp.StatusCode)
	}

	// Bounded read + inline sha256.
	hasher := sha256.New()
	// LimitReader caps at MaxBytes+1 so we can detect "exactly at cap
	// or over" without truncating on success.
	limited := io.LimitReader(resp.Body, f.cfg.MaxBytes+1)
	tee := io.TeeReader(limited, hasher)

	buf, err := io.ReadAll(tee)
	if err != nil {
		return nil, 0, fmt.Errorf("read body: %w", err)
	}
	if int64(len(buf)) > f.cfg.MaxBytes {
		return nil, 0, ErrTooLarge
	}
	if len(buf) < 5 || !bytes.HasPrefix(buf, []byte("%PDF-")) {
		// arxiv sometimes serves an HTML "this paper has been
		// withdrawn" page with status 200; magic-byte check catches that.
		return nil, 0, ErrNotPDF
	}

	return &Result{
		Body:   bytes.NewReader(buf),
		Size:   int64(len(buf)),
		Sha256: hex.EncodeToString(hasher.Sum(nil)),
	}, 0, nil
}

// parseRetryAfter parses an HTTP Retry-After header value, supporting
// both the integer-seconds form ("Retry-After: 30") and the HTTP-date
// form ("Retry-After: Wed, 21 Oct 2026 07:28:00 GMT"). Returns 0 when
// the value is missing or unparseable so the caller falls back to
// exponential backoff.
func parseRetryAfter(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	if secs, err := strconv.Atoi(raw); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(raw); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
	}
	return 0
}

// BuildUserAgent returns the canonical User-Agent string used by the
// fetcher. Exposed so callers (config validation, logs) can render it
// without poking into Config. Empty mailto → no parenthetical, matches
// what RFC 9110 allows for an absent contact.
func BuildUserAgent(version, mailto string) string {
	if version == "" {
		version = "dev"
	}
	if mailto == "" {
		return "qatlasd/" + version
	}
	return "qatlasd/" + version + " (mailto:" + mailto + ")"
}
