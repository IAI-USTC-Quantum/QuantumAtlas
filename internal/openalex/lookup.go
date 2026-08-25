// DOI → fetchable-paper resolver via OpenAlex's public API.
//
// Used by the paper-access router to accept DOIs in the same
// `/api/papers/{id_or_doi}/markdown` URL path that already accepts
// arxiv ids — when the path matches the DOI shape `10.<reg>/<suffix>`
// the router calls Resolver.ResolveDOI and then dispatches on the
// Resolution: an arXiv twin goes to the arxiv pipeline; a published-only
// OA work (best_oa_location.pdf_url) goes to the DOI fetch pipeline
// (mineru.Converter.EnsureByDOI). The handler itself stays DOI-agnostic.
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

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
	"golang.org/x/sync/singleflight"
)

// Defaults conservatively chosen so a misconfigured deployment can't
// hammer OpenAlex. The 5-minute TTL is a compromise between freshness
// (DOI → arxiv mappings rarely change) and quota friendliness.
const (
	DefaultBaseURL      = "https://api.openalex.org/works/doi:"
	DefaultHTTPTimeout  = 10 * time.Second
	DefaultPositiveTTL  = 5 * time.Minute
	DefaultNegativeTTL  = 1 * time.Minute
	DefaultMaxCacheSize = 1024
	// DefaultMaxDOILen mirrors paperassets.MaxDOILen so the two
	// layers can't drift; the alias keeps the openalex-facing API
	// (resolver Config.MaxDOILen) stable while the source of truth
	// lives in paperassets.
	DefaultMaxDOILen = paperassets.MaxDOILen
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
	// the work offers no fetchable full text: no arxiv presence
	// (locations[*].landing_page_url contains no arxiv.org link) AND no
	// open-access PDF URL (best_oa_location.pdf_url empty). Fatal —
	// won't change on retry. Surfaces as 404 to the client.
	ErrDOINotFound = errors.New("openalex: DOI not found or has no fetchable full text (no arxiv twin, no OA PDF)")
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
// active-active edges) each maintain independent caches, so the same
// DOI resolved on both edges incurs two OpenAlex hits within the TTL.
// Acceptable for current scale (~2 edges, low
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
	res       Resolution // zero value for negative cache
	meta      Metadata
	err       error // ErrDOINotFound when negative
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

// Resolution is what a successful ResolveDOI returns. Exactly one of the
// two fields may be empty:
//
//   - ArxivID non-empty: the work has an arXiv twin; the caller dispatches
//     to the arxiv pipeline (resolve latest version → fetch → convert).
//   - ArxivID empty, OAPdfURL non-empty: published-only OA work; the
//     caller drives the DOI fetch pipeline (EnsureByDOI) instead.
//
// When both are empty ResolveDOI fails with ErrDOINotFound, so a nil
// error guarantees at least one fetch path exists. Both fields may be
// non-empty (arXiv twin AND a publisher OA PDF); the arXiv path wins.
type Resolution struct {
	// ArxivID is the canonical arxiv id (e.g. "quant-ph/0811.3171" or
	// "0811.3171") OpenAlex associates with the DOI, version-stripped
	// (work-level granularity — OpenAlex doesn't track per-version arxiv
	// ids). Empty when the work has no arxiv presence.
	ArxivID string
	// OAPdfURL is the best open-access PDF URL (see ExtractOAPdfURL).
	// Empty when OpenAlex knows no direct OA PDF.
	OAPdfURL string
}

// ResolveDOI resolves doi via OpenAlex. Returns ErrDOINotFound when
// OpenAlex doesn't know the DOI or when the work has neither an arxiv
// presence nor an OA PDF URL in its record.
func (r *Resolver) ResolveDOI(ctx context.Context, doi string) (Resolution, error) {
	if !r.enabled {
		return Resolution{}, ErrNotConfigured
	}

	norm, err := normalizeDOI(doi, r.cfg.MaxDOILen)
	if err != nil {
		return Resolution{}, err
	}

	if v, ok := r.cacheGet(norm); ok {
		return v.res, v.err
	}

	// Singleflight: collapse concurrent ResolveDOI(same-doi) calls to
	// one upstream lookup. The shared result is then cached so the
	// next 5 minutes of identical requests are free.
	//
	// Detach from the caller's context (same rationale as LookupMetadata):
	// if caller A cancels mid-flight (browser navigation, request
	// timeout) and other waiters B, C, … are still alive on the same
	// singleflight slot, A's cancellation must not propagate into B/C's
	// returned error. The HTTP timeout still bounds the outbound call
	// via cfg.HTTPTimeout.
	detachedCtx := context.WithoutCancel(ctx)
	result, err, _ := r.sf.Do(norm, func() (any, error) {
		res, lookupErr := r.lookup(detachedCtx, norm)
		// Cache both positive and negative answers (the negative-cache
		// case is the most important — protects against flood of
		// unknown-DOI hits).
		ttl := r.cfg.PositiveTTL
		if lookupErr != nil {
			if !errors.Is(lookupErr, ErrDOINotFound) {
				// Transient upstream — don't cache (next caller might succeed).
				return res, lookupErr
			}
			ttl = r.cfg.NegativeTTL
		}
		r.cachePut(norm, cacheEntry{
			res:       res,
			err:       lookupErr,
			expiresAt: r.now().Add(ttl),
		})
		return res, lookupErr
	})
	if err != nil {
		return Resolution{}, err
	}
	return result.(Resolution), nil
}

// Metadata is the subset of a DOI's OpenAlex record used for upload-time
// verification + accounting on DOI contributions.
type Metadata struct {
	DOI     string   // normalized bare doi the lookup was keyed on
	Title   string   // OpenAlex display title (may be empty)
	Authors []string // author display names in byline order
	ArxivID string   // linked canonical arxiv id, or "" when none
}

// LookupMetadata fetches a DOI's OpenAlex record and returns its title +
// authors (+ any linked arxiv id) for upload-time verification.
//
// Unlike ResolveDOI it does NOT require the work to have an arxiv
// presence: a published-only paper (no preprint) resolves fine. This is
// exactly the case the DOI upload endpoints care about — the DOI may
// point at a published version that never had an arXiv id.
//
// Reuses the same DOI normalization/validation, HTTP client, polite-pool
// mailto, singleflight coalescing, and in-memory LRU as ResolveDOI.
// Results are cached under a "meta:<doi>" key (so a metadata lookup and
// a ResolveDOI for the same DOI occupy two distinct slots and never
// collide): positives for PositiveTTL, ErrDOINotFound for NegativeTTL,
// transient errors not at all. This matters because a contributor often
// uploads PDF then MinerU for the same DOI within minutes — the second
// upload should not re-hit OpenAlex. Returns ErrNotConfigured when no
// mailto is set, ErrInvalidDOI for malformed input, ErrDOINotFound on a
// 404, ErrUpstream on transport failures.
func (r *Resolver) LookupMetadata(ctx context.Context, doi string) (Metadata, error) {
	if !r.enabled {
		return Metadata{}, ErrNotConfigured
	}
	norm, err := normalizeDOI(doi, r.cfg.MaxDOILen)
	if err != nil {
		return Metadata{}, err
	}
	key := "meta:" + norm
	if v, ok := r.cacheGet(key); ok {
		return v.meta, v.err
	}
	// Detach from caller context so cancellation of the first caller
	// doesn't poison coalesced waiters.
	detachedCtx := context.WithoutCancel(ctx)
	result, err, _ := r.sf.Do(key, func() (any, error) {
		work, ferr := r.fetchWork(detachedCtx, norm)
		if ferr != nil {
			// Negative-cache only ErrDOINotFound (a DOI absent from
			// OpenAlex won't appear within seconds); transient upstream
			// errors stay uncached so the next caller can retry.
			if errors.Is(ferr, ErrDOINotFound) {
				r.cachePut(key, cacheEntry{
					meta:      Metadata{DOI: norm},
					err:       ferr,
					expiresAt: r.now().Add(r.cfg.NegativeTTL),
				})
			}
			return Metadata{DOI: norm}, ferr
		}
		meta := Metadata{
			DOI:     norm,
			Title:   strings.TrimSpace(work.Title),
			Authors: AuthorNames(work),
			ArxivID: ExtractArxivID(work),
		}
		r.cachePut(key, cacheEntry{meta: meta, expiresAt: r.now().Add(r.cfg.PositiveTTL)})
		return meta, nil
	})
	if err != nil {
		return Metadata{DOI: norm}, err
	}
	return result.(Metadata), nil
}

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
	// Strip common URL prefixes contributors paste. Sources the canonical
	// list from paperassets so both layers (validation + URL building +
	// router dispatch) accept the same inputs.
	for _, prefix := range paperassets.DOIURLPrefixes {
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
	// Reject control chars (anything < 0x20 or 0x7f) and non-ASCII
	// runelets (Unicode non-printables such as U+00AD soft-hyphen or
	// U+FEFF BOM) that would break URL building or risk header
	// injection. Real DOIs per the DOI Handbook are always ASCII.
	for _, r := range v {
		if r < 0x20 || r == 0x7f {
			return "", fmt.Errorf("%w: control character in DOI", ErrInvalidDOI)
		}
		if r > 0x7f {
			return "", fmt.Errorf("%w: non-ASCII character in DOI", ErrInvalidDOI)
		}
	}
	return v, nil
}

// lookup does the actual HTTP round-trip without any caching or
// dedup, then extracts the arxiv twin and/or OA PDF URL. Pure function
// of (doi, http client, config).
func (r *Resolver) lookup(ctx context.Context, doi string) (Resolution, error) {
	work, err := r.fetchWork(ctx, doi)
	if err != nil {
		return Resolution{}, err
	}
	res := Resolution{
		ArxivID:  ExtractArxivID(work),
		OAPdfURL: ExtractOAPdfURL(work),
	}
	if res.ArxivID == "" && res.OAPdfURL == "" {
		return Resolution{}, ErrDOINotFound
	}
	return res, nil
}

// fetchWork performs the OpenAlex `/works/doi:<doi>` round-trip and
// decodes the Work record. doi MUST be pre-normalized (lower-cased,
// prefix-stripped). Returns ErrDOINotFound on a 404, ErrUpstream on
// transport / non-2xx / decode failures. Unlike lookup() it does NOT
// require any arxiv presence — the full record (title, authors,
// locations) is returned so callers can verify published-only works.
func (r *Resolver) fetchWork(ctx context.Context, doi string) (Work, error) {
	target := r.cfg.BaseURL + url.PathEscape(doi) + "?mailto=" + url.QueryEscape(r.cfg.Mailto)
	body, err := r.getRaw(ctx, target)
	if err != nil {
		return Work{}, err
	}
	var work Work
	if err := json.Unmarshal(body, &work); err != nil {
		return Work{}, fmt.Errorf("%w: decode body: %v", ErrUpstream, err)
	}
	return work, nil
}

// workFetchResult boxes FetchWorkRecord's two return values so they can
// ride a single singleflight slot (Do returns a lone any).
type workFetchResult struct {
	raw  json.RawMessage
	work Work
}

// FetchWorkRecord fetches one OpenAlex work live from the /works/ endpoint
// by kind ("openalex" → /works/W…, "doi" → /works/doi:<doi>) and returns
// BOTH the raw json record (for verbatim storage in the local corpus —
// lazy fetch-on-miss write-through, ADR 0006) and the decoded Work. Returns
// ErrNotConfigured when no mailto is set, ErrDOINotFound on a 404, and
// ErrUpstream on transport / non-2xx / decode failures. "arxiv" is not a
// /works/{id} key and returns ErrDOINotFound (resolve via the corpus filter).
//
// Concurrent calls for the same work coalesce to one upstream hit via the
// same singleflight.Group as ResolveDOI/LookupMetadata, keyed "work:<kind>:<id>"
// so they never collide with the bare-doi (ResolveDOI) or "meta:" slots.
// Unlike those two there is NO in-memory cache layer here: the corpus
// (PostgreSQL) is the write-through cache and the caller persists the result
// via UpsertFetchedWork, so an LRU copy of these large records would be
// redundant. Coalescing still cuts a concurrent-miss stampede to a single
// polite-pool request.
func (r *Resolver) FetchWorkRecord(ctx context.Context, kind, id string) (json.RawMessage, Work, error) {
	if !r.enabled {
		return nil, Work{}, ErrNotConfigured
	}
	// worksBase is the endpoint without the DOI convenience suffix, e.g.
	// "https://api.openalex.org/works/".
	worksBase := strings.TrimSuffix(r.cfg.BaseURL, "doi:")
	var target, sfKey string
	switch kind {
	case "openalex":
		bare := strings.TrimSpace(id)
		if bare == "" {
			return nil, Work{}, ErrDOINotFound
		}
		target = worksBase + url.PathEscape(bare) + "?mailto=" + url.QueryEscape(r.cfg.Mailto)
		sfKey = "work:openalex:" + bare
	case "doi":
		norm, err := normalizeDOI(id, r.cfg.MaxDOILen)
		if err != nil {
			return nil, Work{}, err
		}
		// Keep "doi:" unescaped (a path prefix), escape only the DOI body —
		// mirrors fetchWork's DOI-suffixed BaseURL behaviour.
		target = worksBase + "doi:" + url.PathEscape(norm) + "?mailto=" + url.QueryEscape(r.cfg.Mailto)
		sfKey = "work:doi:" + norm
	default:
		return nil, Work{}, ErrDOINotFound
	}

	// Detach from the caller context so one waiter's cancellation doesn't
	// abort the shared fetch for the others (bounded by cfg.HTTPTimeout) —
	// same rationale as ResolveDOI/LookupMetadata.
	detachedCtx := context.WithoutCancel(ctx)
	res, err, _ := r.sf.Do(sfKey, func() (any, error) {
		body, ferr := r.getRaw(detachedCtx, target)
		if ferr != nil {
			return nil, ferr
		}
		var work Work
		if uerr := json.Unmarshal(body, &work); uerr != nil {
			return nil, fmt.Errorf("%w: decode body: %v", ErrUpstream, uerr)
		}
		return workFetchResult{raw: json.RawMessage(body), work: work}, nil
	})
	if err != nil {
		return nil, Work{}, err
	}
	out := res.(workFetchResult)
	return out.raw, out.work, nil
}

// getRaw performs a GET against target and returns the response body (bounded
// read), mapping OpenAlex status codes onto the package's typed errors.
func (r *Resolver) getRaw(ctx context.Context, target string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("%w: build request: %v", ErrUpstream, err)
	}
	req.Header.Set("User-Agent", "qatlasd (mailto:"+r.cfg.Mailto+")")
	req.Header.Set("Accept", "application/json")

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w: http: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, ErrDOINotFound
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, fmt.Errorf("%w: 429 rate-limited (check mailto / lower QPS)", ErrUpstream)
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("%w: http %d", ErrUpstream, resp.StatusCode)
	}

	// Bounded read so a malicious / misconfigured upstream can't
	// blow up our memory.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %v", ErrUpstream, err)
	}
	return body, nil
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
// at capacity. When the key already exists, it is moved to the tail
// (most-recently-used position) to preserve true LRU semantics.
func (r *Resolver) cachePut(key string, e cacheEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.cache[key]; exists {
		// Move existing key to tail (most-recently-used).
		r.evictFromOrder(key)
		r.order = append(r.order, key)
	} else {
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
