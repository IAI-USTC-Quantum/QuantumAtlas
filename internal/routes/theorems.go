package routes

import (
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/theoremsplugin"

	"github.com/pocketbase/pocketbase/core"
)

// TheoremsPlugin is the builtin theorems plugin (ADR 0002): it reads through a
// server-side `git pull --ff-only` checkout of the upstream Lean-content repo
// and exposes the proved-Theorems catalog read-only under /api/theorems/*. Its
// git-sync pair (/api/theorems/sync/{pull,status}) is mounted by the platform's
// GitPullPlugin capability, symmetric with wiki.
//
// It deliberately does NOT own a PocketBase collection (ADR 0004): proved
// Theorems are read-through from git, not persisted. Claims (pre-proof) live
// only in the localhost contrib agent + gitea (ADR 0005), never in QA.
type TheoremsPlugin struct {
	cfg   *config.Config
	cache *theoremsplugin.Cache
}

// NewTheoremsPlugin constructs the theorems builtin. cache MUST be non-nil and
// already constructed (NewCache happens in main.go at startup so the first
// request hits warm data).
func NewTheoremsPlugin(cfg *config.Config, cache *theoremsplugin.Cache) *TheoremsPlugin {
	return &TheoremsPlugin{cfg: cfg, cache: cache}
}

// PluginID is the theorems plugin's URL/scope namespace.
func (t *TheoremsPlugin) PluginID() string { return "theorems" }

// GitRepoDir is the Lean-content checkout the host's `git pull --ff-only`
// runs in.
func (t *TheoremsPlugin) GitRepoDir() string { return t.cfg.TheoremsDir }

// OnPullSucceeded reloads the in-memory registry cache synchronously so the
// next read reflects the pulled commit immediately.
func (t *TheoremsPlugin) OnPullSucceeded() error { return t.cache.Reload() }

// RegisterRoutes mounts the theorems read surface. All endpoints are gated
// behind scopeGuard("theorems", "read"): the catalog is not anonymously
// browsable — callers need a session token or a PAT carrying theorems:read.
func (t *TheoremsPlugin) RegisterRoutes(se *core.ServeEvent, deps PluginDeps) error {
	cache, enforcer := t.cache, deps.Enforcer

	// GET /api/theorems/list — filterable catalog listing.
	se.Router.GET("/api/theorems/list", scopeGuard(enforcer, "theorems", "read", func(re *core.RequestEvent) error {
		q := re.Request.URL.Query()
		items := cache.List(theoremsplugin.ListFilter{
			FamilyID:    q.Get("family_id"),
			AuditStatus: q.Get("audit_status"),
			Kind:        q.Get("kind"),
		})
		return re.JSON(http.StatusOK, map[string]any{
			"total":    len(items),
			"theorems": items,
		})
	}))

	// GET /api/theorems/families — family definitions (filter dropdown source).
	se.Router.GET("/api/theorems/families", scopeGuard(enforcer, "theorems", "read", func(re *core.RequestEvent) error {
		fams := cache.Families()
		return re.JSON(http.StatusOK, map[string]any{
			"total":    len(fams),
			"families": fams,
		})
	}))

	// GET /api/theorems/stats — aggregate counts.
	se.Router.GET("/api/theorems/stats", scopeGuard(enforcer, "theorems", "read", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, cache.Stats())
	}))

	// GET /api/theorems/theorem/{fqn} — full registry entry + audit verdict.
	se.Router.GET("/api/theorems/theorem/{fqn}", scopeGuard(enforcer, "theorems", "read", func(re *core.RequestEvent) error {
		fqn := re.Request.PathValue("fqn")
		thm, ok := cache.Find(fqn)
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "theorem not found: " + fqn})
		}
		out := map[string]any{"theorem": thm}
		if cert, ok := cache.CertifiedFor(thm.UnitID); ok {
			out["certified"] = cert
		} else {
			out["certified"] = nil
		}
		return re.JSON(http.StatusOK, out)
	}))

	// GET /api/theorems/theorem-source/{fqn} — Lean source on demand.
	se.Router.GET("/api/theorems/theorem-source/{fqn}", scopeGuard(enforcer, "theorems", "read", func(re *core.RequestEvent) error {
		fqn := re.Request.PathValue("fqn")
		file, src, err := cache.Source(fqn)
		if err != nil {
			if _, ok := err.(*theoremsplugin.SourceError); ok {
				return re.JSON(http.StatusNotFound, map[string]string{"detail": err.Error()})
			}
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"lean_fqn": fqn,
			"file":     file,
			"source":   src,
		})
	}))

	return nil
}
