package routes

// info.go: GET /api/server/info — capability discovery for clients
// (qatlas CLI, SPA, third-party integrators). The response keeps the
// historical mode / version / engine fields and adds a capabilities
// block describing which optional surfaces this deployment serves, so
// a client can decide how to drive the server without probing
// endpoints one by one.
//
// Privacy mirrors /api/health (see the healthz package doc §"Privacy
// tiers"): every caller — anonymous included — sees the capability
// booleans; authenticated callers (system PAT or session JWT, per
// IsCallerAuthenticated) additionally see the MinerU daily-cap numbers,
// which double as deployment-load fingerprints. Nothing else about the
// topology (tokens, bucket names, mesh endpoints) ever leaks here.

import (
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"

	"github.com/pocketbase/pocketbase/core"
)

// RegisterServerInfo mounts GET /api/server/info. version is the
// server build version (cmd/qatlasd.Version). converter / scheduler
// may be nil (MinerU unconfigured — the route still reports, with
// mineru.enabled=false); agenticConfigured reports whether any agentic
// backend (remote qatlas-search microservice or local runner) is
// wired behind POST /api/search/agentic.
func RegisterServerInfo(
	se *core.ServeEvent,
	cfg *config.Config,
	version string,
	converter *mineru.Converter,
	scheduler *mineru.Scheduler,
	agenticConfigured bool,
) {
	se.Router.GET("/api/server/info", func(re *core.RequestEvent) error {
		// mineru.enabled: the converter will actually drive MinerU
		// (paper access on + at least one API token). mineru.on_demand:
		// GET /api/papers/{id}/markdown can trigger a conversion on
		// cache miss (asset endpoints registered + converter enabled).
		mineruEnabled := converter != nil && converter.Enabled()
		mineruCaps := map[string]any{
			"enabled":   mineruEnabled,
			"on_demand": mineruEnabled && cfg.PaperAccessEnabled,
		}
		if IsCallerAuthenticated(re) {
			// Numbers for authenticated callers only, same snapshot
			// source as /api/health. daily_cap is the BATCH scheduler's
			// self-limit (default 4000/day, reserving headroom for
			// interactive traffic); on-demand GET-triggered conversions
			// are NOT counted against it — they share only the upstream
			// per-token quota.
			if scheduler != nil {
				snap := scheduler.Snapshot()
				mineruCaps["daily_cap"] = snap.DailyCap
				mineruCaps["converted_today"] = snap.ConvertedToday
			}
		}
		capabilities := map[string]any{
			"paper_access":      cfg.PaperAccessEnabled,
			"markdown_delivery": cfg.PaperAccessEnabled,
			// PDF delivery is disabled by design (the /pdf endpoint
			// answers 410); the constant false lets clients branch
			// without probing.
			"pdf_delivery":   false,
			"agentic_search": agenticConfigured,
			"mineru":         mineruCaps,
		}
		return re.JSON(http.StatusOK, map[string]any{
			"mode":         "server",
			"version":      version,
			"engine":       "go+pocketbase",
			"capabilities": capabilities,
		})
	})
}
