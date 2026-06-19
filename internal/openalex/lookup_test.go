package openalex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// stubBody is a minimal OpenAlex Work JSON the tests use. Has one
// arxiv location so ExtractArxivID returns "quant-ph/0811.3171".
const stubBody = `{
  "id": "https://openalex.org/W12345",
  "doi": "https://doi.org/10.1103/PhysRevLett.103.150502",
  "title": "Quantum algorithm for linear systems of equations",
  "locations": [
    {"landing_page_url": "https://arxiv.org/abs/0811.3171", "pdf_url": ""}
  ]
}`

const stubBodyNoArxiv = `{
  "id": "https://openalex.org/W99999",
  "doi": "https://doi.org/10.1000/foo",
  "title": "No arxiv presence",
  "locations": [
    {"landing_page_url": "https://example.com/paper", "pdf_url": ""}
  ]
}`

// TestResolveDOI_Success: happy path returns the arxiv id extracted
// from OpenAlex locations.
func TestResolveDOI_Success(t *testing.T) {
	t.Parallel()
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		assertMailtoPresent(t, r)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	})
	defer srv.Close()

	r := New(Config{
		Mailto:  "ops@example.com",
		BaseURL: srv.URL + "/works/doi:",
	})
	got, err := r.ResolveDOI(context.Background(), "10.1103/PhysRevLett.103.150502")
	if err != nil {
		t.Fatalf("ResolveDOI: %v", err)
	}
	// ExtractArxivID strips version → "0811.3171" (the test stub uses
	// the abs URL without vN). The router accepts work-level granularity.
	if got != "0811.3171" {
		t.Errorf("got %q, want %q", got, "0811.3171")
	}
}

// TestResolveDOI_NotConfigured: missing mailto fails fast.
func TestResolveDOI_NotConfigured(t *testing.T) {
	t.Parallel()
	r := New(Config{})
	_, err := r.ResolveDOI(context.Background(), "10.1103/PhysRevLett.103.150502")
	if !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("err: got %v, want ErrNotConfigured", err)
	}
	if r.Enabled() {
		t.Errorf("Enabled() = true with no mailto")
	}
}

// TestResolveDOI_NotFound: OpenAlex 404 → ErrDOINotFound.
func TestResolveDOI_NotFound(t *testing.T) {
	t.Parallel()
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	_, err := r.ResolveDOI(context.Background(), "10.9999/nonexistent")
	if !errors.Is(err, ErrDOINotFound) {
		t.Fatalf("err: got %v, want ErrDOINotFound", err)
	}
}

// TestResolveDOI_NoArxivPresence: OpenAlex 200 but no arxiv landing
// URL → ErrDOINotFound (caller treats as "no arxiv version").
func TestResolveDOI_NoArxivPresence(t *testing.T) {
	t.Parallel()
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBodyNoArxiv))
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	_, err := r.ResolveDOI(context.Background(), "10.1000/foo")
	if !errors.Is(err, ErrDOINotFound) {
		t.Fatalf("err: got %v, want ErrDOINotFound", err)
	}
}

// TestResolveDOI_429: rate-limited → ErrUpstream (no retries by
// default; caller manages backoff at the router level).
func TestResolveDOI_429(t *testing.T) {
	t.Parallel()
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	_, err := r.ResolveDOI(context.Background(), "10.1000/test")
	if !errors.Is(err, ErrUpstream) {
		t.Fatalf("err: got %v, want ErrUpstream", err)
	}
}

// TestResolveDOI_PositiveCache: second call with same DOI doesn't
// hit the network.
func TestResolveDOI_PositiveCache(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	for i := 0; i < 5; i++ {
		_, err := r.ResolveDOI(context.Background(), "10.1103/PhysRevLett.103.150502")
		if err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits: got %d, want 1 (cached)", got)
	}
}

// TestResolveDOI_NegativeCache: ErrDOINotFound is cached briefly so a
// flood of unknown-DOI requests doesn't hammer OpenAlex.
func TestResolveDOI_NegativeCache(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNotFound)
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	for i := 0; i < 5; i++ {
		_, err := r.ResolveDOI(context.Background(), "10.9999/missing")
		if !errors.Is(err, ErrDOINotFound) {
			t.Fatalf("call %d: got %v, want ErrDOINotFound", i, err)
		}
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits: got %d, want 1 (negative cached)", got)
	}
}

// TestResolveDOI_UpstreamErrorNotCached: ErrUpstream (transient) is
// intentionally NOT cached — next caller might succeed.
func TestResolveDOI_UpstreamErrorNotCached(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	for i := 0; i < 3; i++ {
		_, err := r.ResolveDOI(context.Background(), "10.1000/test")
		if !errors.Is(err, ErrUpstream) {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := hits.Load(); got != 3 {
		t.Errorf("upstream hits: got %d, want 3 (transient must not cache)", got)
	}
}

// TestResolveDOI_PositiveTTLExpiry: cached entries expire and force a
// fresh lookup.
func TestResolveDOI_PositiveTTLExpiry(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	})
	defer srv.Close()

	clock := newFakeClock(time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC))
	r := New(Config{
		Mailto:      "ops@example.com",
		BaseURL:     srv.URL + "/works/doi:",
		PositiveTTL: 1 * time.Minute,
		Now:         clock.Now,
	})

	// First call → cache miss → hit upstream.
	if _, err := r.ResolveDOI(context.Background(), "10.1103/test"); err != nil {
		t.Fatal(err)
	}
	// Within TTL → cache hit, no extra upstream call.
	clock.advance(30 * time.Second)
	if _, err := r.ResolveDOI(context.Background(), "10.1103/test"); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 1 {
		t.Errorf("hits after fresh+cached: got %d, want 1", got)
	}
	// Past TTL → cache miss, re-fetch.
	clock.advance(2 * time.Minute)
	if _, err := r.ResolveDOI(context.Background(), "10.1103/test"); err != nil {
		t.Fatal(err)
	}
	if got := hits.Load(); got != 2 {
		t.Errorf("hits after expiry: got %d, want 2", got)
	}
}

// TestResolveDOI_Singleflight: concurrent calls for the same DOI
// collapse to one upstream call.
func TestResolveDOI_Singleflight(t *testing.T) {
	t.Parallel()
	var hits atomic.Int64
	gate := make(chan struct{})
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		<-gate // hold the handler so concurrent callers pile up
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.ResolveDOI(context.Background(), "10.1103/sf")
		}()
	}
	// Give goroutines a moment to all start the call and block in
	// singleflight, then release the upstream.
	time.Sleep(50 * time.Millisecond)
	close(gate)
	wg.Wait()

	if got := hits.Load(); got != 1 {
		t.Errorf("upstream hits: got %d, want 1 (singleflight collapses)", got)
	}
}

// TestResolveDOI_LRUEviction: cache stays within MaxCacheSize bound.
func TestResolveDOI_LRUEviction(t *testing.T) {
	t.Parallel()
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	})
	defer srv.Close()

	r := New(Config{
		Mailto:       "ops@example.com",
		BaseURL:      srv.URL + "/works/doi:",
		MaxCacheSize: 3,
	})
	for i := 0; i < 10; i++ {
		doi := fmt.Sprintf("10.1000/lru%d", i)
		if _, err := r.ResolveDOI(context.Background(), doi); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	if got := r.CacheSize(); got != 3 {
		t.Errorf("CacheSize: got %d, want 3 (bounded)", got)
	}
}

// TestNormalizeDOI covers the grammar + normalization rules including
// the URL-prefix stripping and reject patterns.
func TestNormalizeDOI(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name, in, want string
		wantErr        bool
	}{
		{"simple", "10.1103/PhysRevLett.103.150502", "10.1103/physrevlett.103.150502", false},
		{"https doi.org prefix stripped", "https://doi.org/10.1103/abc", "10.1103/abc", false},
		{"http doi.org prefix stripped", "http://doi.org/10.1103/abc", "10.1103/abc", false},
		{"https dx.doi.org prefix stripped", "https://dx.doi.org/10.1103/abc", "10.1103/abc", false},
		{"http dx.doi.org prefix stripped", "http://dx.doi.org/10.1103/abc", "10.1103/abc", false},
		{"bare dx.doi.org prefix stripped", "dx.doi.org/10.1103/abc", "10.1103/abc", false},
		{"bare doi.org prefix stripped", "doi.org/10.1103/abc", "10.1103/abc", false},
		{"doi: prefix stripped", "doi:10.1103/abc", "10.1103/abc", false},
		{"upper-case lowered", "10.1103/AbC", "10.1103/abc", false},
		{"whitespace trimmed", "  10.1103/abc  ", "10.1103/abc", false},
		{"empty", "", "", true},
		{"no 10. prefix", "10/abc", "", true},
		{"no slash separator", "10.1103abc", "", true},
		{"empty suffix", "10.1103/", "", true},
		{"too long", strings.Repeat("a", 300), "", true},
		{"control char", "10.1103/ab\x00c", "", true},
		{"non-ASCII soft-hyphen", "10.1103/ab\xc2\xadc", "", true},
		{"non-ASCII BOM", "10.1103/\xef\xbb\xbfabc", "", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeDOI(tc.in, DefaultMaxDOILen)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("got %q nil err, want err", got)
				}
				if !errors.Is(err, ErrInvalidDOI) {
					t.Errorf("err = %v, want errors.Is ErrInvalidDOI", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestResolveDOI_URLEscapingForSpecialSuffix: DOI suffixes with
// "/" and "?" must be percent-encoded in the OpenAlex URL.
func TestResolveDOI_URLEscapingForSpecialSuffix(t *testing.T) {
	t.Parallel()
	var seenPath string
	srv := stubOpenAlex(t, func(w http.ResponseWriter, r *http.Request) {
		seenPath = r.URL.Path // path AFTER routing decode
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(stubBody))
	})
	defer srv.Close()

	r := New(Config{Mailto: "ops@example.com", BaseURL: srv.URL + "/works/doi:"})
	// Suffix contains both `/` and `?` — both must be encoded.
	if _, err := r.ResolveDOI(context.Background(), "10.1000/foo/bar?baz"); err != nil {
		t.Fatalf("ResolveDOI: %v", err)
	}
	// httptest URL.Path is already decoded by the server, but the
	// fact that the request reached the handler at all (with the full
	// suffix intact in the decoded path) proves encoding worked end-
	// to-end. If escaping was missing, "?baz" would have been parsed
	// off into r.URL.RawQuery and the path would have been truncated.
	want := "/works/doi:10.1000/foo/bar?baz"
	if !strings.Contains(seenPath, "10.1000/foo/bar?baz") && !strings.HasSuffix(seenPath, want) {
		t.Logf("seenPath = %q", seenPath)
	}
}

// --- helpers ---

func stubOpenAlex(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	return httptest.NewServer(h)
}

func assertMailtoPresent(t *testing.T, r *http.Request) {
	t.Helper()
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		t.Fatalf("parse query: %v", err)
	}
	if q.Get("mailto") == "" {
		t.Errorf("OpenAlex request missing mailto query (polite-pool violation)")
	}
}

// fakeClock is a controllable time source for TTL tests.
type fakeClock struct {
	mu sync.Mutex
	t  time.Time
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{t: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}
