// Wiki in-memory cache. Eliminates the per-request fs.WalkDir + YAML
// parse pass that was making /api/pages, /api/stats and /api/search
// take ~1s each on a 500-page wiki (one full re-scan + parse for every
// JSON byte returned). The catalog is small (~5 MB parsed) and read-only
// from the server's POV (writes happen via `git push` from contributors,
// then `git pull` on this host), so caching is a near-perfect fit.
//
// Design notes:
//
//   - Single immutable snapshot pointer, swapped atomically on refresh
//     (atomic.Pointer[CacheSnapshot]). Readers never lock; the slice +
//     stats maps are treated as immutable. Handlers that need a mutable
//     copy (e.g. summaries projection) iterate and build their own.
//   - Refresh staleness check: `git rev-parse HEAD` is sub-millisecond
//     and detects out-of-band `git pull` / direct edits + commit without
//     having to stat every file. Falls back to "always re-walk" for
//     non-git wiki dirs (mtime-based detection is a future YAGNI).
//   - Two refresh triggers: (1) background ticker every refreshEvery,
//     (2) synchronous Refresh(true) called from /api/wiki/sync/pull after
//     `git pull` succeeds so the client sees fresh data immediately.
//   - This is intentionally NOT the paperindex (S3 parquet + DuckDB)
//     pattern. Paperindex exists to converge multi-edge writes into one
//     queryable catalog; wiki is single-source-of-truth per edge (the
//     git checkout) so all that machinery is overkill here.
package wiki

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// CacheSnapshot is one immutable view of the parsed wiki tree. New
// snapshots are produced by Cache.Refresh; readers fetch the current
// snapshot via atomic load so they never block writers.
type CacheSnapshot struct {
	// Pages is sorted by Frontmatter.ID for stable JSON output.
	// Handlers MUST treat this slice as read-only (do not append /
	// reslice into it) — append would alias the cache's backing array.
	Pages     []*Page
	GitCommit string // `git rev-parse HEAD` at load time; "" for non-git dirs
	LoadedAt  time.Time
	ParseErrs int
	stats     Stats // private; accessed via Cache.Stats() which deep-copies the maps
}

// Cache is the live wiki catalog used by /api/pages, /api/stats,
// /api/search and /api/pages/{id}. Construct with NewCache; call Stop()
// at shutdown to halt the background refresh ticker.
type Cache struct {
	dir          string
	refreshEvery time.Duration

	snap atomic.Pointer[CacheSnapshot]

	// muRefresh serializes concurrent Refresh calls so a burst of
	// requests on a stale snapshot doesn't trigger N parallel walks.
	// Doesn't block readers (they go through snap.Load).
	muRefresh sync.Mutex

	cancel context.CancelFunc
	done   chan struct{}
}

// NewCache builds the initial snapshot synchronously (so the first
// /api/pages request after startup hits warm cache) and starts the
// background refresh ticker. The initial walk is allowed to fail —
// the cache stays empty and accessors degrade to empty responses
// until a subsequent Refresh succeeds. Pass refreshEvery <= 0 to use
// the default of 60s.
func NewCache(dir string, refreshEvery time.Duration) *Cache {
	if refreshEvery <= 0 {
		refreshEvery = 60 * time.Second
	}
	c := &Cache{
		dir:          dir,
		refreshEvery: refreshEvery,
		done:         make(chan struct{}),
	}
	if _, err := c.Refresh(true); err != nil {
		slog.Warn("wiki: initial cache load failed", "dir", dir, "error", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.cancel = cancel
	go c.loop(ctx)
	return c
}

// Stop tears down the background refresh goroutine and waits for it
// to exit. Safe to call multiple times.
func (c *Cache) Stop() {
	if c.cancel == nil {
		return
	}
	c.cancel()
	<-c.done
	c.cancel = nil
}

func (c *Cache) loop(ctx context.Context) {
	defer close(c.done)
	t := time.NewTicker(c.refreshEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := c.Refresh(false); err != nil {
				slog.Warn("wiki: background refresh failed", "error", err)
			}
		}
	}
}

// Refresh rebuilds the snapshot if git HEAD has moved (or always when
// force=true / the cache is empty). Returns (changed, err) where
// changed=true means a new snapshot was published. The walk runs under
// muRefresh so concurrent Refresh callers coalesce — only one walk
// executes, the rest see changed=false.
func (c *Cache) Refresh(force bool) (bool, error) {
	c.muRefresh.Lock()
	defer c.muRefresh.Unlock()

	// Cheap staleness check: skip the walk if git HEAD hasn't moved
	// AND we already have a snapshot to serve from. For non-git dirs
	// gitOutput returns "" and we end up re-walking every tick, which
	// is acceptable for the once-a-minute cadence (the walk is ~1s
	// even cold; the goal was just to stop doing it per-request).
	currentCommit := gitOutput(c.dir, "rev-parse", "HEAD")
	cur := c.snap.Load()
	if !force && cur != nil && cur.GitCommit != "" && cur.GitCommit == currentCommit {
		return false, nil
	}

	pages := make([]*Page, 0, 512)
	stats := Stats{
		ByType:     map[string]int{},
		ByStatus:   map[string]int{},
		ByCategory: map[string]int{},
	}
	parseErrs, err := IteratePages(c.dir, WalkOptions{IgnoreParseErrors: true}, func(p *Page) error {
		pages = append(pages, p)
		stats.TotalPages++
		if t := p.Frontmatter.Type; t != "" {
			stats.ByType[t]++
		}
		if s := p.Frontmatter.Status; s != "" {
			stats.ByStatus[s]++
		}
		if cat := p.Frontmatter.Category; cat != "" {
			stats.ByCategory[cat]++
		}
		if p.Frontmatter.Neo4jSynced {
			stats.SyncedToNeo4j++
		} else {
			stats.NeedsSync++
		}
		return nil
	})
	if err != nil {
		return false, err
	}

	sort.Slice(pages, func(i, j int) bool {
		return pages[i].Frontmatter.ID < pages[j].Frontmatter.ID
	})

	snap := &CacheSnapshot{
		Pages:     pages,
		GitCommit: currentCommit,
		LoadedAt:  time.Now(),
		ParseErrs: parseErrs,
		stats:     stats,
	}
	c.snap.Store(snap)
	slog.Info("wiki: cache refreshed",
		"pages", len(pages), "parse_errors", parseErrs,
		"git_commit", shortCommit(currentCommit))
	return true, nil
}

// Pages returns the cached pages matching f, or an empty slice if no
// snapshot has loaded yet (wiki dir absent or initial load failed).
// When f is empty the cache's own slice is returned directly — the
// caller MUST treat the result as read-only.
func (c *Cache) Pages(f ListFilter) []*Page {
	snap := c.snap.Load()
	if snap == nil {
		return nil
	}
	if f.IsEmpty() {
		return snap.Pages
	}
	out := make([]*Page, 0, len(snap.Pages))
	for _, p := range snap.Pages {
		if f.matches(p) {
			out = append(out, p)
		}
	}
	return out
}

// FindPage returns the cached page with matching frontmatter.id, or
// nil if absent / cache empty. O(N) scan — fine for our scale; promote
// to a map if FindPage shows up in CPU profiles.
func (c *Cache) FindPage(id string) *Page {
	snap := c.snap.Load()
	if snap == nil {
		return nil
	}
	for _, p := range snap.Pages {
		if p.Frontmatter.ID == id {
			return p
		}
	}
	return nil
}

// Stats returns the cached aggregate counts. Maps are deep-copied so
// the handler is free to mutate them (or hand them to json.Marshal,
// which itself doesn't mutate, but defensive copies cost ~microseconds
// for our key cardinality). Returns a zero-value Stats when the cache
// is empty.
func (c *Cache) Stats() Stats {
	snap := c.snap.Load()
	if snap == nil {
		return Stats{
			ByType:     map[string]int{},
			ByStatus:   map[string]int{},
			ByCategory: map[string]int{},
		}
	}
	return Stats{
		TotalPages:    snap.stats.TotalPages,
		SyncedToNeo4j: snap.stats.SyncedToNeo4j,
		NeedsSync:     snap.stats.NeedsSync,
		ByType:        copyCountMap(snap.stats.ByType),
		ByStatus:      copyCountMap(snap.stats.ByStatus),
		ByCategory:    copyCountMap(snap.stats.ByCategory),
	}
}

// Search runs the same case-insensitive substring scan as the standalone
// wiki.Search function, but against the cached *Page slice — no fs walk,
// no per-file ReadFile. Identical scoring semantics so the frontend sees
// stable ranks across the cache/no-cache split.
func (c *Cache) Search(query string, maxResults int) []SearchResult {
	if maxResults <= 0 {
		maxResults = 10
	}
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return nil
	}
	snap := c.snap.Load()
	if snap == nil {
		return nil
	}
	var results []SearchResult
	for _, p := range snap.Pages {
		score := 0.0
		title := strings.ToLower(p.Frontmatter.Title)
		id := strings.ToLower(p.Frontmatter.ID)
		body := strings.ToLower(p.Content)

		if strings.Contains(title, q) {
			score += 3
		}
		if strings.Contains(id, q) {
			score += 3
		}
		for _, tag := range p.Frontmatter.Tags {
			if strings.Contains(strings.ToLower(tag), q) {
				score += 2
			}
		}
		if hits := strings.Count(body, q); hits > 0 {
			score += float64(hits)
		}
		if score == 0 {
			continue
		}
		results = append(results, SearchResult{
			ID:      p.Frontmatter.ID,
			Title:   p.Frontmatter.Title,
			Type:    p.Frontmatter.Type,
			Snippet: makeSnippet(p.Content, q, 160),
			Score:   score,
		})
	}
	sortResults(results)
	if len(results) > maxResults {
		results = results[:maxResults]
	}
	return results
}

// Ready reports whether at least one snapshot has loaded. Health checks
// can use this to distinguish "wiki misconfigured" from "wiki temporarily
// failing to refresh".
func (c *Cache) Ready() bool {
	return c.snap.Load() != nil
}

// Snapshot returns the current snapshot pointer (or nil). Exposed for
// introspection (e.g. /api/wiki/sync/status reports loaded_at + git
// commit so operators can confirm freshness without scraping logs).
func (c *Cache) Snapshot() *CacheSnapshot {
	return c.snap.Load()
}

func copyCountMap(src map[string]int) map[string]int {
	dst := make(map[string]int, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func shortCommit(c string) string {
	if len(c) < 7 {
		return c
	}
	return c[:7]
}
