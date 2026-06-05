// DOI → arxiv_id resolver via OpenAlex's public API.
//
// Used by the paper-access router to accept DOIs in the same
// `/api/papers/{id_or_doi}/markdown` URL path that already accepts
// arxiv ids — when the path matches the DOI shape `10.<reg>/<suffix>`
// the router calls Resolver.ResolveDOI to get the canonical arxiv id
// and then dispatches to the existing handler. The handler itself
// stays DOI-agnostic.
//
// This is the v1 implementation per plan §2.3: hit the OpenAlex public
// API at runtime. A follow-up (issue #11) will swap this for a Neo4j
// :PaperWork.doi index lookup once the OpenAlex bootstrap is reliable
// in production. Keeping the public API as the fallback (or as the
// only path during transition) is acceptable because:
//
//   - polite-pool requests with a configured mailto are reliable enough
//     for low-volume access (we cache resolutions for 5 min);
//   - the singleflight wrapper collapses concurrent requests for the
//     same DOI to one upstream call;
//   - negative caching (1 min TTL) prevents a flood of unknown-DOI
//     lookups from hammering OpenAlex.
//
// SECURITY: DOI suffixes can contain slashes and many special chars
// (DOI grammar is essentially "anything URL-safe"). The resolver
// URL-escapes the suffix before building the OpenAlex URL so callers
// can't inject `?` / `#` to redirect the request elsewhere.

package openalex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"
)

// Defaults conservatively chosen so a misconfigured deployment can't
// hammer OpenAlex. The 5-minute TTL is a compromise between freshness
// (DOI → arxiv mappings rarely change) and quota friendliness.
const (
	DefaultBaseURL        = "https://api.openalex.org/works/doi:"
	DefaultHTTPTimeout    = 10 * time.Second
	DefaultPositiveTTL    = 5 * time.Minute
	DefaultNegativeTTL    = 1 * time.Minute
	DefaultMaxCacheSize   = 1024
	DefaultMaxDOILen      = 256
)

// Errors returned by ResolveDOI. errors.Is-compatible so callers can
// branch on each case to render the right HTTP status.
var (
	// ErrNotConfigured is returned when the resolver was constructed
	// without a Mailto — OpenAlex polite-pool requires mailto, and a
	// resolver without one would be both rude and rate-limited.
	// Surfaces as 503 to the client.
	ErrNotConfigured = errors.New("openalex: resolver missing mailto (set QATLAS_OPENALEX_MAILTO)")
	// ErrInvalidDOI is returned for input that doesn't look like a DOI
	// (no `10.<digits>/<suffix>` prefix, exceeds MaxDOILen, contains
	// control chars). Surfaces as 400 to the client.
	ErrInvalidDOI = errors.New("openalex: invalid DOI")
	// ErrDOINotFound is returned when OpenAlex responds 404 OR when
	// the work has no arxiv presence (locations[*].landing_page_url
	// contains no arxiv.org link). Fatal — won't change on retry.
	// Surfaces as 404 to the client.
	ErrDOINotFound = errors.New("openalex: DOI not found or has no arxiv presence")
	// ErrUpstream wraps any transport / 5xx / 429 error after retries
	// (or zero retries; the resolver doesn't retry by default because
	// it's blocking the user's request). Surfaces as 502 to the
	// client.
	ErrUpstream = errors.New("openalex: upstream error")
)

// Config configures a Resolver. Zero values fall back to Default*.
type Config struct {
	// Mailto is the polite-pool contact email. REQUIRED — without it
	// New returns an enabled=false resolver that errors out on every
	// call. The same email is also folded into the User-Agent for
	// audit on OpenAlex's side.
	Mailto string

	// BaseURL is the OpenAlex DOI lookup endpoint; default
	// DefaultBaseURL. The full URL becomes
	// `<base><doi-escaped>?mailto=<mailto>`.
	BaseURL string
	// HTTPClient lets tests inject a stub. Defaults to a new
	// http.Client with Timeout=HTTPTimeout when nil.
	HTTPClient *http.Client
	// HTTPTimeout is the per-request timeout. Default 10s.
	HTTPTimeout time.Duration
	// PositiveTTL is how long a successful resolve is cached. Default 5min.
	PositiveTTL time.Duration
	// NegativeTTL is how long a "DOI not found" / "no arxiv presence"
	// answer is cached. Default 1min — short enough that a DOI added
	// to OpenAlex eventually becomes resolvable without manual cache
	// invalidation, long enough to absorb a flood of unknown-DOI hits.
	NegativeTTL time.Duration
	// MaxCacheSize bounds the LRU cache. Default 1024 entries (well
	// under 1 MiB total memory).
	MaxCacheSize int
	// MaxDOILen is the input length cap before parsing. Default 256.
	MaxDOILen int
	// Now lets tests advance time without sleeping.
	Now func() time.Time
}

// Resolver resolves DOIs to canonical arxiv ids via OpenAlex. Safe for
// concurrent use; concurrent requests for the same DOI are coalesced
// via singleflight to a single upstream call.
//
// Cache is PER-PROCESS (in-memory LRU). Two qatlasd processes (e.g.
// active-active on RackNerd + Alibaba) each maintain independent
// caches, so the same DOI resolved on both edges incurs two OpenAlex
// hits within the TTL. Acceptable for current scale (~2 edges, low
// DOI QPS, polite-pool 10 req/s budget per IP); cross-edge cache
// sharing (Redis or local Neo4j index) is tracked in issue #13 (the
// MinerU dedupe issue covers shared-state infrastructure that the
// DOI cache would naturally share) — independent of issue #11 which
// is about replacing OpenAlex itself with a local index.
type Resolver struct {
	cfg     Config
	enabled bool
	client  *http.Client
	now     func() time.Time

	sf singleflight.Group

	mu    sync.Mutex
	cache map[string]cacheEntry
	// order tracks insertion order for cheap LRU eviction; head of
	// slice is oldest. Not the most efficient LRU but more than enough
	// for 1024 entries.
	order []string
}

type cacheEntry struct {
	canonical string // empty for negative cache
	err       error  // ErrDOINotFound when negative
	expiresAt time.Time
}

// New constructs a Resolver. When cfg.Mailto is empty the returned
// resolver is in "disabled" mode — every ResolveDOI call returns
// ErrNotConfigured. This is intentional: the master switch
// QATLAS_PAPER_ACCESS_ENABLED may be ON while OpenAlex mailto is
// forgotten; we want a clear 503 + WARN log rather than silently
// degrading into anonymous rate-limited mode.
func New(cfg Config) *Resolver {
	if cfg.BaseURL == "" {
		cfg.BaseURL = DefaultBaseURL
	}
	if cfg.HTTPTimeout == 0 {
		cfg.HTTPTimeout = DefaultHTTPTimeout
	}
	if cfg.PositiveTTL == 0 {
		cfg.PositiveTTL = DefaultPositiveTTL
	}
	if cfg.NegativeTTL == 0 {
		cfg.NegativeTTL = DefaultNegativeTTL
	}
	if cfg.MaxCacheSize == 0 {
		cfg.MaxCacheSize = DefaultMaxCacheSize
	}
	if cfg.MaxDOILen == 0 {
		cfg.MaxDOILen = DefaultMaxDOILen
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: cfg.HTTPTimeout}
	}
	return &Resolver{
		cfg:     cfg,
		enabled: strings.TrimSpace(cfg.Mailto) != "",
		client:  cfg.HTTPClient,
		now:     cfg.Now,
		cache:   make(map[string]cacheEntry, cfg.MaxCacheSize),
	}
}

// Enabled reports whether the resolver has a mailto configured. False
// → every ResolveDOI returns ErrNotConfigured.
func (r *Resolver) Enabled() bool { return r.enabled }

// ResolveDOI returns the canonical arxiv id (e.g.
// "quant-ph/0811.3171" or "0811.3171") that OpenAlex associates with
// doi. Returns ErrDOINotFound when OpenAlex doesn't know the DOI or
// when the work has no arxiv presence in its locations[]. The
// returned id has the version suffix STRIPPED (work-level granularity
// — OpenAlex doesn't track per-version arxiv ids) so callers that
// need a specific version must resolve "latest" via another path.
func (r *Resolver) ResolveDOI(ctx context.Context, doi string) (string, error) {
	if !r.enabled {
		return "", ErrNotConfigured
	}

	norm, err := normalizeDOI(doi, r.cfg.MaxDOILen)
	if err != nil {
		return "", err
	}

	if v, ok := r.cacheGet(norm); ok {
		return v.canonical, v.err
	}

	// Singleflight: collapse concurrent ResolveDOI(same-doi) calls to
	// one upstream lookup. The shared result is then cached so the
	// next 5 minutes of identical requests are free.
	result, err, _ := r.sf.Do(norm, func() (any, error) {
		canonical, lookupErr := r.lookup(ctx, norm)
		// Cache both positive and negative answers (the negative-cache
		// case is the most important — protects against flood of
		// unknown-DOI hits).
		ttl := r.cfg.PositiveTTL
		if lookupErr != nil {
			if !errors.Is(lookupErr, ErrDOINotFound) {
				// Transient upstream — don't cache (next caller might succeed).
				return canonical, lookupErr
			}
			ttl = r.cfg.NegativeTTL
		}
		r.cachePut(norm, cacheEntry{
			canonical: canonical,
			err:       lookupErr,
			expiresAt: r.now().Add(ttl),
		})
		return canonical, lookupErr
	})
	if err != nil {
		return "", err
	}
	return result.(string), nil
}

// normalizeDOI validates and case-normalizes a DOI for cache-key /
// URL-build use. DOI grammar accepted: must start with `10.` then
// digits then `/` then non-empty suffix. Whitespace trimmed,
// case-lowered (DOIs are case-insensitive per DOI Handbook).
func normalizeDOI(in string, maxLen int) (string, error) {
	v := strings.TrimSpace(in)
	if v == "" {
		return "", fmt.Errorf("%w: empty", ErrInvalidDOI)
	}
	if len(v) > maxLen {
		return "", fmt.Errorf("%w: exceeds %d chars", ErrInvalidDOI, maxLen)
	}
	v = strings.ToLower(v)
	// Strip common URL prefixes contributors paste.
	for _, prefix := range []string{
		"https://doi.org/",
		"http://doi.org/",
		"doi.org/",
		"doi:",
	} {
		if strings.HasPrefix(v, prefix) {
			v = strings.TrimPrefix(v, prefix)
			break
		}
	}
	if !strings.HasPrefix(v, "10.") {
		return "", fmt.Errorf("%w: must start with 10.", ErrInvalidDOI)
	}
	slash := strings.IndexByte(v, '/')
	if slash < 4 { // need at least "10.x/"
		return "", fmt.Errorf("%w: missing registrant/suffix separator", ErrInvalidDOI)
	}
	if slash == len(v)-1 {
		return "", fmt.Errorf("%w: empty suffix", ErrInvalidDOI)
	}
	// Reject control chars (anything < 0x20 or 0x7f) that would break
	// URL building or risk header injection.
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: control character in DOI", ErrInvalidDOI)
		}
	}
	return v, nil
}

// lookup does the actual HTTP round-trip without any caching or
// dedup. Pure function of (doi, http client, config).
func (r *Resolver) lookup(ctx context.Context, doi string) (string, error) {
	// PathEscape the DOI: the suffix can contain `/`, `?`, `#`, etc.
	// We want them all percent-encoded so they don't terminate the
	// path or start a query.
	target := r.cfg.BaseURL + url.PathEscape(doi) + "?mailto=" + url.QueryEscape(r.cfg.Mailto)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return "", fmt.Errorf("%w: build request: %v", ErrUpstream, err)
	}
	req.Header.Set("User-Agent", "qatlasd (mailto:"+r.cfg.Mailto+")")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: http: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return "", ErrDOINotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return "", fmt.Errorf("%w: 429 rate-limited (check mailto / lower QPS)", ErrUpstream)
	case resp.StatusCode != http.StatusOK:
		return "", fmt.Errorf("%w: http %d", ErrUpstream, resp.StatusCode)
	}

	// Bounded read so a malicious / misconfigured upstream can't
	// blow up our memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return "", fmt.Errorf("%w: read body: %v", ErrUpstream, err)
	}

	var work Work
	if err := json.Unmarshal(body, &work); err != nil {
		return "", fmt.Errorf("%w: decode body: %v", ErrUpstream, err)
	}

	id := ExtractArxivID(work)
	if id == "" {
		return "", ErrDOINotFound
	}
	return id, nil
}

// cacheGet returns a non-expired entry from the LRU cache, if any.
// Expired entries are evicted in-place so the next caller re-fetches.
func (r *Resolver) cacheGet(key string) (cacheEntry, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e, ok := r.cache[key]
	if !ok {
		return cacheEntry{}, false
	}
	if r.now().After(e.expiresAt) {
		delete(r.cache, key)
		r.evictFromOrder(key)
		return cacheEntry{}, false
	}
	return e, true
}

// cachePut inserts (or refreshes) an entry, evicting the oldest when
// at capacity.
func (r *Resolver) cachePut(key string, e cacheEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cache[key]; !exists {
		// New key — track in order. Evict oldest if at capacity.
		if len(r.cache) >= r.cfg.MaxCacheSize {
			oldest := r.order[0]
			r.order = r.order[1:]
			delete(r.cache, oldest)
		}
		r.order = append(r.order, key)
	}
	r.cache[key] = e
}

// evictFromOrder removes key from the order slice (linear scan; OK at
// 1024 entries). Used by cache expiry path.
func (r *Resolver) evictFromOrder(key string) {
	for i, k := range r.order {
		if k == key {
			r.order = append(r.order[:i], r.order[i+1:]...)
			return
		}
	}
}

// CacheSize returns the current number of entries, for tests + healthz.
func (r *Resolver) CacheSize() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.cache)
}
