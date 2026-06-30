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

	"github.com/pocketbase/pocketbase/core"
)

// WikiPlugin is the builtin wiki plugin (ADR 0002): it reads through a
// server-side `git pull --ff-only` checkout of the markdown wiki repo and
// exposes the knowledge-base read surface under /api/pages*, /api/stats,
// /api/search. Its git-sync pair (/api/wiki/sync/{pull,status}) is mounted
// by the platform's GitPullPlugin capability, not here.
type WikiPlugin struct {
	cfg   *config.Config
	cache *wiki.Cache
}

// NewWikiPlugin constructs the wiki builtin. cache MUST be non-nil and
// already constructed (NewCache happens in main.go at startup so the first
// request hits warm data).
func NewWikiPlugin(cfg *config.Config, cache *wiki.Cache) *WikiPlugin {
	return &WikiPlugin{cfg: cfg, cache: cache}
}

// PluginID is the wiki plugin's URL/scope namespace.
func (w *WikiPlugin) PluginID() string { return "wiki" }

// GitRepoDir is the wiki checkout the host's `git pull --ff-only` runs in.
func (w *WikiPlugin) GitRepoDir() string { return w.cfg.WikiDir }

// OnPullSucceeded refreshes the in-memory page cache synchronously so the
// next read sees the pulled commit immediately.
func (w *WikiPlugin) OnPullSucceeded() error {
	_, err := w.cache.Refresh(true)
	return err
}

// RegisterRoutes mounts the wiki read surface. All endpoints are gated behind
// scopeGuard("wiki", "read"): the knowledge base is not anonymously browsable
// — callers need a session token or a PAT carrying wiki:read. The git-sync
// pair (/api/wiki/sync/*) is mounted separately by the platform.
func (w *WikiPlugin) RegisterRoutes(se *core.ServeEvent, deps PluginDeps) error {
	cfg, cache, enforcer := w.cfg, w.cache, deps.Enforcer

	se.Router.GET("/api/pages", scopeGuard(enforcer, "wiki", "read", func(re *core.RequestEvent) error {
		if !wikiDirExists(cfg) {
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
		if !wikiDirExists(cfg) {
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
		if !wikiDirExists(cfg) {
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
		if !wikiDirExists(cfg) {
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

	return nil
}

// wikiDirExists reports whether cfg.WikiDir is an existing directory.
//
// cfg.WikiDir is guaranteed non-empty by config.Load (it falls back to the
// sibling-checkout default "<.env dir>/../QuantumAtlas-Wiki" when no env var
// is set), so no in-handler fallback is needed.
func wikiDirExists(cfg *config.Config) bool {
	return dirExists(cfg.WikiDir)
}

// isExternalToProject reports whether dir is outside the project working
// directory (CWD at server start). Used by the platform sync-status payload
// to warn operators that a content repo is non-local.
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
