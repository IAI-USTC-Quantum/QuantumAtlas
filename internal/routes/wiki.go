// Package routes hosts the HTTP route handlers for the QuantumAtlas Go
// server. Each business module gets its own file: wiki.go, pages.go,
// graph.go, papers.go, shares.go, info.go.
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

// RegisterWiki registers the /api/wiki/*, /api/pages*, /api/stats and
// /api/search routes on se.Router. cfg supplies the wiki_dir resolution.
func RegisterWiki(se *core.ServeEvent, cfg *config.Config) {
	se.Router.GET("/api/wiki/sync/status", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, wikiSyncStatus(cfg))
	})

	se.Router.POST("/api/wiki/sync/pull", func(re *core.RequestEvent) error {
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
	})

	se.Router.GET("/api/pages", func(re *core.RequestEvent) error {
		dir, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusOK, map[string]any{
				"total": 0,
				"pages": []any{},
			})
		}
		req := re.Request
		filter := wiki.ListFilter{
			Type:     req.URL.Query().Get("page_type"),
			Status:   req.URL.Query().Get("status"),
		}
		if raw := req.URL.Query().Get("tags"); raw != "" {
			for _, t := range strings.Split(raw, ",") {
				t = strings.TrimSpace(t)
				if t != "" {
					filter.Tags = append(filter.Tags, t)
				}
			}
		}
		pages, _, err := wiki.ListPages(dir, filter)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		summaries := make([]map[string]any, 0, len(pages))
		for _, p := range pages {
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
	})

	se.Router.GET("/api/pages/{page_id}", func(re *core.RequestEvent) error {
		pageID := re.Request.PathValue("page_id")
		dir, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusNotFound, map[string]string{
				"detail": "Page not found: " + pageID,
			})
		}
		page, err := wiki.FindPage(dir, pageID)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
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
	})

	se.Router.GET("/api/stats", func(re *core.RequestEvent) error {
		dir, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusOK, map[string]any{
				"total_pages":     0,
				"by_type":         map[string]int{},
				"by_status":       map[string]int{},
				"by_category":     map[string]int{},
				"synced_to_neo4j": 0,
				"needs_sync":      0,
			})
		}
		stats, err := wiki.ComputeStats(dir)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		return re.JSON(http.StatusOK, stats)
	})

	se.Router.GET("/api/search", func(re *core.RequestEvent) error {
		q := re.Request.URL.Query().Get("q")
		limitRaw := re.Request.URL.Query().Get("limit")
		limit := 10
		if limitRaw != "" {
			if n, err := strconv.Atoi(limitRaw); err == nil && n > 0 {
				limit = n
			}
		}
		dir, status := resolveWikiDir(cfg)
		if !status["wiki"].(map[string]any)["exists"].(bool) {
			return re.JSON(http.StatusOK, map[string]any{
				"query":   q,
				"total":   0,
				"results": []any{},
			})
		}
		results, err := wiki.Search(dir, q, limit)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		if results == nil {
			results = []wiki.SearchResult{}
		}
		return re.JSON(http.StatusOK, map[string]any{
			"query":   q,
			"total":   len(results),
			"results": results,
		})
	})

	// /api/lint — placeholder. The full Python lint pass is ~370 LOC
	// across atlas/wiki/linter.py and isn't on the CLI hot path; we
	// emit an empty issue list so the frontend renders a "no problems"
	// pane instead of erroring out. A follow-up commit will port the
	// real lint rules.
	se.Router.GET("/api/lint", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]any{
			"issues":           []any{},
			"checked_pages":    0,
			"linter_available": false,
			"note":             "Go server: lint rules not yet ported. See atlas/wiki/linter.py for the Python implementation.",
		})
	})
}

// resolveWikiDir returns the absolute wiki dir path and the same nested
// status map shape the /api/wiki/sync/status endpoint exposes. We compute
// both at once because every wiki route needs to know "does the dir
// even exist" before doing real work.
func resolveWikiDir(cfg *config.Config) (string, map[string]any) {
	dir := cfg.WikiDir
	if dir == "" {
		// Mirror the Python default project-relative behavior so
		// a `wiki/` directory next to CWD still works during dev.
		if cwd, err := os.Getwd(); err == nil {
			dir = filepath.Join(cwd, "wiki")
		}
	}
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
