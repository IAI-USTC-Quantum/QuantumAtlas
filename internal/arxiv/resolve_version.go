package arxiv

// resolve_version.go: figure out the latest published vN for an arXiv
// id that arrived without an explicit version suffix.
//
// Why this exists: OpenAlex's `landing_page_url` for an arxiv work
// stops at the bare id ("http://arxiv.org/abs/0811.3171"), with no vN
// suffix. So the DOI dispatch path in internal/routes/papers.go lands
// canonical ids without a version, but every downstream layer
// (`paperassets.AssetKey`, `arxiv.Fetcher.Fetch`, MinerU storage)
// requires an explicit `vN` for content immutability. We need a way
// to translate "0811.3171" → "0811.3171v3" before handing off.
//
// What didn't work:
//   - GET /pdf/<id> doesn't redirect; arxiv just serves the latest
//     bytes with no version exposed in any header or final URL.
//   - HEAD /abs/<id> ditto — 200, no Location header.
//   - https://export.arxiv.org/api/query?id_list= heavily rate-limited
//     (we hit 429 in seconds during probe) and would need a separate
//     polite-pool with a different UA. Overkill for one-shot lookups.
//
// What works: GET /abs/<id> includes
//   <meta property="og:url" content="https://arxiv.org/abs/<id>v<N>" />
// in the HTML head. We do one bounded GET, grep that single line, and
// parse the version off the end. Body cap of 64 KiB is comfortably
// larger than any real abs page (~30 KB) so the regex never has to
// scan junk.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/paperassets"
)

// AbsBaseURL is the canonical arXiv abstract page prefix used for
// version-resolution lookups. Override via Config.AbsBaseURL in tests.
const AbsBaseURL = "https://arxiv.org/abs/"

const resolveBodyCap = 64 * 1024 // arxiv abs pages are ~30 KB; 64 KiB is headroom

// ogURLVersionRE matches the version suffix in the og:url meta tag.
// Example match input:
//
//	<meta property="og:url" content="https://arxiv.org/abs/0811.3171v3" />
//	<meta property="og:url" content="https://arxiv.org/abs/quant-ph/9508027v2" />
//
// We anchor on the `og:url` attribute to avoid matching unrelated
// version strings elsewhere in the page (citation_arxiv_id, etc.).
var ogURLVersionRE = regexp.MustCompile(
	`property="og:url"[^>]*content="https?://arxiv\.org/abs/[a-zA-Z0-9./\-]+v(\d+)"`,
)

// ResolveLatestVersion fetches the arXiv abs page for bareID and
// returns a ParsedArxivID with the latest published version filled in.
//
// bareID must already be normalized (`Parse()` accepted it with
// Version=="") and have the correct shape (new-style "2401.12345" or
// old-style "quant-ph/9508027" / "9508027"). The bare form is what
// OpenAlex hands us via landing_page_url.
//
// Errors map to the same sentinels Fetch uses so callers can branch
// uniformly:
//
//	ErrNotFound  — abs page 404 (paper retracted / never existed)
//	ErrUpstream  — transport / 5xx / parse failure (no vN in HTML)
//
// Rate-limited via the same Fetcher token bucket as Fetch — so a
// flood of DOI requests for unknown papers can't burst past our
// global arxiv.org QPS budget.
func (f *Fetcher) ResolveLatestVersion(ctx context.Context, bareID paperassets.ParsedArxivID) (paperassets.ParsedArxivID, error) {
	if bareID.Version != "" {
		// Already versioned — short-circuit. Defensive: callers that
		// already know vN shouldn't bother calling this.
		return bareID, nil
	}
	if !bareID.IsValid() {
		return paperassets.ParsedArxivID{}, fmt.Errorf("arxiv: invalid bare id for resolve: %q", bareID.Canonical)
	}

	if err := f.limiter.Wait(ctx); err != nil {
		return paperassets.ParsedArxivID{}, fmt.Errorf("arxiv: rate-limiter wait: %w", err)
	}

	base := f.cfg.AbsBaseURL
	if base == "" {
		base = AbsBaseURL
	}
	url := base + bareID.Canonical

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return paperassets.ParsedArxivID{}, fmt.Errorf("%w: build request: %v", ErrUpstream, err)
	}
	req.Header.Set("User-Agent", f.cfg.UserAgent)
	req.Header.Set("Accept", "text/html")

	resp, err := f.client.Do(req)
	if err != nil {
		return paperassets.ParsedArxivID{}, fmt.Errorf("%w: get abs: %v", ErrUpstream, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// fall through
	case http.StatusNotFound:
		return paperassets.ParsedArxivID{}, ErrNotFound
	case http.StatusTooManyRequests:
		return paperassets.ParsedArxivID{}, ErrRateLimited
	default:
		return paperassets.ParsedArxivID{}, fmt.Errorf("%w: abs status %d", ErrUpstream, resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, resolveBodyCap))
	if err != nil {
		return paperassets.ParsedArxivID{}, fmt.Errorf("%w: read abs body: %v", ErrUpstream, err)
	}
	m := ogURLVersionRE.FindSubmatch(body)
	if m == nil {
		return paperassets.ParsedArxivID{}, fmt.Errorf("%w: og:url version tag missing for %s", ErrUpstream, bareID.Canonical)
	}
	version := "v" + string(m[1])

	versioned, err := paperassets.Parse(bareID.Canonical + version)
	if err != nil {
		return paperassets.ParsedArxivID{}, fmt.Errorf("%w: re-parse %s%s: %v", ErrUpstream, bareID.Canonical, version, err)
	}
	return versioned, nil
}

// stripWhitespace is a tiny helper used by tests when comparing
// expected vs actual url strings around the og:url tag. (No callers
// outside tests; promoting to package level for visibility.)
//
//nolint:deadcode,unused // kept for parity with Python helpers
func stripWhitespace(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

var _ = errors.New // keep errors imported across edits
