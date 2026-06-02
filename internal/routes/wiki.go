// Package routes hosts the HTTP route handlers for the QuantumAtlas Go
// server. Each business module gets its own file: wiki.go, pages.go,
// graph.go, papers.go, info.go.
//
// Handlers are wired up by Register(se, app, cfg) called from main.go
// inside the OnServe hook.
package routes

import (
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/wiki"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterWiki registers the /api/wiki/*, /api/pages*, /api/stats and
// /api/search routes on se.Router. cfg supplies the wiki_dir resolution.
//
// cache MUST be non-nil and already constructed (NewCache happens in
// main.go at startup so the first request hits warm data). All read
// paths go through the cache; only /api/wiki/sync/pull writes (then
// refreshes the cache synchronously so callers see fresh data on the
// next request).
//
// All wiki read endpoints (/api/pages*, /api/stats, /api/search,
// /api/lint, /api/wiki/sync/status) are gated behind
// scopeGuard("wiki", "read"): the knowledge base is not anonymously
// browsable — callers need a session token or a PAT carrying wiki:read.
// /api/wiki/sync/pull additionally mutates server state (runs git +
// rebuilds the cache), so it requires the stronger wiki:write scope
// (which implies wiki:read).
func RegisterWiki(se *core.ServeEvent, cfg *config.Config, cache *wiki.Cache, enforcer *casbin.Enforcer) {
	se.Router.GET("/api/wiki/sync/status", scopeGuard(enforcer, "wiki", "read", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, wikiSyncStatus(cfg))
	}))

	se.Router.POST("/api/wiki/sync/pull", scopeGuard(enforcer, "wiki", "write", func(re *core.RequestEvent) error {
		dir, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusConflict, map[string]string{"detail": "wiki directory does not exist"})
		}
		result, err := wiki.Pull(dir)
		if err != nil {
			if pe, ok := err.(*wiki.PullError); ok {
				return re.JSON(pe.Status, map[string]string{"detail": pe.Detail})
			}
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		// Force a synchronous refresh so the next read sees the pulled
		// commit immediately, not 60s later when the ticker would fire.
		// Non-fatal: a refresh failure here is logged inside Refresh and
		// the old snapshot keeps serving.
		if _, refreshErr := cache.Refresh(true); refreshErr != nil {
			// Don't fail the pull response — git pull DID succeed.
			// The next ticker tick will retry the cache rebuild.
			_ = refreshErr
		}
		// Merge git status into the response just like the Python handler.
		out := map[string]any{
			"status":     result.Status,
			"changed":    result.Changed,
			"old_commit": result.OldCommit,
			"new_commit": result.NewCommit,
		}
		for k, v := range wikiSyncStatus(cfg) {
			out[k] = v
		}
		return re.JSON(http.StatusOK, out)
	}))

	se.Router.GET("/api/pages", scopeGuard(enforcer, "wiki", "read", func(re *core.RequestEvent) error {
		_, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusOK, map[string]any{
				"total": 0,
				"pages": []any{},
			})
		}
		req := re.Request
		filter := wiki.ListFilter{
			Type:   req.URL.Query().Get("page_type"),
			Status: req.URL.Query().Get("status"),
		}
		if raw := req.URL.Query().Get("tags"); raw != "" {
			for _, t := range strings.Split(raw, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					filter.Tags = append(filter.Tags, t)
				}
			}
		}
		pages := cache.Pages(filter)
		// Wikipedia-style browse: source pages (processed papers) are
		// citations, not browsable entries. Exclude them from the default
		// listing unless the caller explicitly asked for page_type=source.
		hideSources := filter.Type == ""
		summaries := make([]map[string]any, 0, len(pages))
		for _, p := range pages {
			if hideSources && p.Frontmatter.Type == wiki.TypeSource {
				continue
			}
			summaries = append(summaries, map[string]any{
				"id":       p.Frontmatter.ID,
				"title":    p.Frontmatter.Title,
				"type":     p.Frontmatter.Type,
				"category": p.Frontmatter.Category,
				"status":   p.Frontmatter.Status,
				"tags":     p.Frontmatter.Tags,
			})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"total": len(summaries),
			"pages": summaries,
		})
	}))

	se.Router.GET("/api/pages/{page_id}", scopeGuard(enforcer, "wiki", "read", func(re *core.RequestEvent) error {
		pageID := re.Request.PathValue("page_id")
		_, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "Page not found: " + pageID,
			})
		}
		page := cache.FindPage(pageID)
		if page == nil {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "Page not found: " + pageID,
			})
		}
		out := map[string]any{
			"id":       page.Frontmatter.ID,
			"title":    page.Frontmatter.Title,
			"type":     page.Frontmatter.Type,
			"category": page.Frontmatter.Category,
			"tags":     page.Frontmatter.Tags,
			"status":   page.Frontmatter.Status,
			"content":  page.Content,
		}
		if page.Frontmatter.CreatedAt != nil {
			out["created_at"] = page.Frontmatter.CreatedAt
		}
		if page.Frontmatter.UpdatedAt != nil {
			out["updated_at"] = page.Frontmatter.UpdatedAt
		}
		return re.JSON(http.StatusOK, out)
	}))

	se.Router.GET("/api/stats", scopeGuard(enforcer, "wiki", "read", func(re *core.RequestEvent) error {
		_, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusOK, map[string]any{
				"total_pages":     0,
				"entries":         0,
				"sources":         0,
				"by_type":         map[string]int{},
				"by_status":       map[string]int{},
				"by_category":     map[string]int{},
				"synced_to_neo4j": 0,
				"needs_sync":      0,
			})
		}
		stats := cache.Stats()
		// "entries" = browsable wiki entries = everything except source
		// pages (processed papers, which are citations not entries).
		// total_pages keeps its original all-inclusive meaning for any
		// existing consumer; the SPA reads "entries" for its tile.
		entries := stats.TotalPages - stats.ByType[wiki.TypeSource]
		if entries < 0 {
			entries = 0
		}
		return re.JSON(http.StatusOK, map[string]any{
			"total_pages":     stats.TotalPages,
			"entries":         entries,
			"sources":         stats.ByType[wiki.TypeSource],
			"by_type":         stats.ByType,
			"by_status":       stats.ByStatus,
			"by_category":     stats.ByCategory,
			"synced_to_neo4j": stats.SyncedToNeo4j,
			"needs_sync":      stats.NeedsSync,
		})
	}))

	se.Router.GET("/api/search", scopeGuard(enforcer, "wiki", "read", func(re *core.RequestEvent) error {
		q := re.Request.URL.Query().Get("q")
		limitRaw := re.Request.URL.Query().Get("limit")
		limit := 10
		if limitRaw != "" {
			if n, err := strconv.Atoi(limitRaw); err == nil && n > 0 {
				limit = n
			}
		}
		_, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusOK, map[string]any{
				"query":   q,
				"total":   0,
				"results": []any{},
			})
		}
		// Source pages are citations, not browsable entries — keep them
		// out of search results unless explicitly requested. Over-fetch
		// then filter so the post-filter result count still approaches
		// the requested limit.
		includeSources := re.Request.URL.Query().Get("include_sources") == "true"
		fetch := limit
		if !includeSources {
			fetch = limit * 3
		}
		results := cache.Search(q, fetch)
		if results == nil {
			results = []wiki.SearchResult{}
		}
		if !includeSources {
			filtered := results[:0]
			for _, r := range results {
				if r.Type == wiki.TypeSource {
					continue
				}
				filtered = append(filtered, r)
			}
			results = filtered
			if len(results) > limit {
				results = results[:limit]
			}
		}
		return re.JSON(http.StatusOK, map[string]any{
			"query":   q,
			"total":   len(results),
			"results": results,
		})
	}))

	// /api/lint — placeholder. The full Python lint pass is ~370 LOC
	// across atlas/wiki/linter.py and isn't on the CLI hot path; we
	// emit an empty issue list so the frontend renders a "no problems"
	// pane instead of erroring out. A follow-up commit will port the
	// real lint rules.
	se.Router.GET("/api/lint", scopeGuard(enforcer, "wiki", "read", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]any{
			"issues":           []any{},
			"checked_pages":    0,
			"linter_available": false,
			"note":             "Go server: lint rules not yet ported. See atlas/wiki/linter.py for the Python implementation.",
		})
	}))
}

// resolveWikiDir returns the absolute wiki dir path and the same nested
// status map shape the /api/wiki/sync/status endpoint exposes. We compute
// both at once because every wiki route needs to know "does the dir
// even exist" before doing real work.
//
// cfg.WikiDir is guaranteed non-empty by config.Load (it falls back to
// the sibling-checkout default "<.env dir>/../QuantumAtlas-Wiki" when
// no env var is set), so no in-handler fallback is needed.
func resolveWikiDir(cfg *config.Config) (string, map[string]any) {
	dir := cfg.WikiDir
	exists := false
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		exists = true
	}
	gitInfo := wiki.GitInfo{}
	if exists {
		gitInfo = wiki.ReadGitInfo(dir)
	}
	return dir, map[string]any{
		"wiki": map[string]any{
			"exists":   exists,
			"external": isExternalToProject(dir),
		},
		"git": gitInfo,
	}
}

// wikiSyncStatus is the public-facing payload for /api/wiki/sync/status.
func wikiSyncStatus(cfg *config.Config) map[string]any {
	_, status := resolveWikiDir(cfg)
	return status
}

// isExternalToProject reports whether dir is outside the project working
// directory (CWD at server start). Matches the Python helper of the same
// name; used by the UI to warn operators that the wiki repo is non-local.
func isExternalToProject(dir string) bool {
	cwd, err := os.Getwd()
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(cwd, dir)
	if err != nil {
		return true
	}
	return strings.HasPrefix(rel, "..")
}
