package routes

import (
	"log/slog"
	"net/http"
	"os"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/gitpull"
	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// This file is the host-core plugin platform (ADR 0003): the single
// registration hook every builtin plugin goes through, plus the optional
// GitPullPlugin capability that mounts a uniform /api/<id>/sync/{pull,status}
// for plugins that read through a server-side `git pull --ff-only` checkout.
//
// Builtin plugins are satisfied STRUCTURALLY — a plugin package implements
// PluginID/RegisterRoutes/GitRepoDir/... without importing this package, so the
// host core stays domain-agnostic and plugins own their domain end-to-end.

// PluginDeps are the host-core backends a builtin plugin's RegisterRoutes may
// need. A plugin that also needs its own backend (wiki cache, theorems cache)
// captures it at construction and holds it on its own struct.
type PluginDeps struct {
	Cfg      *config.Config
	Enforcer *casbin.Enforcer
	Registry *qplugin.Registry
}

// BuiltinPlugin is the registration hook every first-party (kind=builtin)
// plugin implements. main.go iterates the builtin list and calls each hook —
// new builtins land by adding to the list, with no host-core surgery.
type BuiltinPlugin interface {
	// PluginID is the URL/scope namespace: routes live under /api/<id>/*.
	PluginID() string
	// RegisterRoutes mounts the plugin's own /api/<id>/* routes.
	RegisterRoutes(se *core.ServeEvent, deps PluginDeps) error
}

// GitPullPlugin is the optional capability a builtin implements when it reads
// through a server-side `git pull --ff-only` checkout of an upstream content
// repo (wiki, theorems). The platform type-asserts each builtin to this and,
// for those that satisfy it, mounts the uniform POST /api/<id>/sync/pull +
// GET /api/<id>/sync/status pair so the contract is identical across every
// pull-style plugin.
type GitPullPlugin interface {
	BuiltinPlugin
	// GitRepoDir is the working-tree path the host's `git pull --ff-only`
	// runs in.
	GitRepoDir() string
	// OnPullSucceeded runs plugin-specific post-pull work (wiki:
	// cache.Refresh; theorems: registry cache reload).
	OnPullSucceeded() error
}

// RegisterBuiltins registers each builtin plugin's routes and, for those that
// also satisfy GitPullPlugin, mounts the uniform git-sync route pair. Which
// builtins mount is config-driven, not fixed by this arg list: a builtin whose
// PluginID is not enabled by QATLAS_PLUGINS_ENABLED/DISABLED is skipped,
// honoring the SAME enable/disable rule (qplugin.IsEnabled) the manifest
// registry applies — so an operator disabling e.g. "rag" drops both its
// manifest entry and its in-process routes.
func RegisterBuiltins(se *core.ServeEvent, deps PluginDeps, plugins ...BuiltinPlugin) error {
	for _, p := range plugins {
		if !qplugin.IsEnabled(p.PluginID(), deps.Cfg.PluginsEnabled, deps.Cfg.PluginsDisabled) {
			slog.Info("plugins: builtin disabled by config", "id", p.PluginID())
			continue
		}
		if err := p.RegisterRoutes(se, deps); err != nil {
			return err
		}
		if gp, ok := p.(GitPullPlugin); ok {
			mountGitSync(se, deps.Enforcer, gp)
		}
	}
	return nil
}

// mountGitSync mounts the canonical /api/<id>/sync/{pull,status} pair for a
// GitPullPlugin. status requires <id>:read; pull additionally mutates server
// state (runs git + post-pull work) so it requires <id>:write.
func mountGitSync(se *core.ServeEvent, enforcer *casbin.Enforcer, p GitPullPlugin) {
	id := p.PluginID()

	se.Router.GET("/api/"+id+"/sync/status", scopeGuard(enforcer, id, "read", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, gitSyncStatus(p))
	}))

	se.Router.POST("/api/"+id+"/sync/pull", scopeGuard(enforcer, id, "write", func(re *core.RequestEvent) error {
		dir := p.GitRepoDir()
		if !dirExists(dir) {
			return re.JSON(http.StatusConflict, map[string]string{"detail": id + " content directory does not exist"})
		}
		result, err := gitpull.Pull(dir)
		if err != nil {
			if pe, ok := err.(*gitpull.PullError); ok {
				return re.JSON(pe.Status, map[string]string{"detail": pe.Detail})
			}
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		// Refresh the plugin's view synchronously so the next read sees
		// the pulled commit immediately. Non-fatal: git pull DID succeed;
		// a refresh failure is the plugin's own background-retry concern.
		_ = p.OnPullSucceeded()
		out := map[string]any{
			"status":     result.Status,
			"changed":    result.Changed,
			"old_commit": result.OldCommit,
			"new_commit": result.NewCommit,
		}
		for k, v := range gitSyncStatus(p) {
			out[k] = v
		}
		return re.JSON(http.StatusOK, out)
	}))
}

// gitSyncStatus is the uniform /api/<id>/sync/status payload, shared by the
// status route and merged into the pull response.
func gitSyncStatus(p GitPullPlugin) map[string]any {
	dir := p.GitRepoDir()
	exists := dirExists(dir)
	git := gitpull.GitInfo{}
	if exists {
		git = gitpull.ReadGitInfo(dir)
	}
	return map[string]any{
		"id":           p.PluginID(),
		"exists":       exists,
		"dir_external": isExternalToProject(dir),
		"git":          git,
	}
}

func dirExists(dir string) bool {
	info, err := os.Stat(dir)
	return err == nil && info.IsDir()
}

// --- Builtin adapters for the non-pull plugins (graph, rag) ------------------
//
// graph and rag construct their Neo4j/Qdrant clients per request, so they hold
// no backend state — the platform deps carry everything they need. They are
// NOT GitPullPlugin (no upstream git checkout), so the platform mounts no
// /sync/* pair for them.

type graphBuiltin struct{}

// NewGraphPlugin returns the graph builtin adapter.
func NewGraphPlugin() BuiltinPlugin { return graphBuiltin{} }

func (graphBuiltin) PluginID() string { return "graph" }
func (graphBuiltin) RegisterRoutes(se *core.ServeEvent, d PluginDeps) error {
	RegisterGraph(se, d.Cfg, d.Registry, d.Enforcer)
	return nil
}

type ragBuiltin struct{}

// NewRAGPlugin returns the rag builtin adapter.
func NewRAGPlugin() BuiltinPlugin { return ragBuiltin{} }

func (ragBuiltin) PluginID() string { return "rag" }
func (ragBuiltin) RegisterRoutes(se *core.ServeEvent, d PluginDeps) error {
	RegisterRAG(se, d.Cfg, d.Registry, d.Enforcer)
	return nil
}
