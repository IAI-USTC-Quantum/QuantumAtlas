package routes

// search_agentic.go: POST /api/search/agentic — the metered agentic
// search endpoint. The backend is selected by search.agentic.backend:
// "remote" talks to the external qatlas-search microservice, "local"
// drives the in-process claude-CLI runner (internal/agentic); both
// answer with the same standardized search.RemoteResponse.
//
// Unlike POST /api/search (fan-out over local providers) this endpoint
// may drive an LLM and costs real tokens. Every call is metered per
// user per day (usage_daily, enforced atomically by
// usage.Store.CheckAndReserve) against the caller's effective limit
// (per-user override > plan > search.agentic.daily_limit). A failed
// upstream call is refunded.
//
// Auth: papers:read scope (same as /api/search) PLUS a user-bound
// credential — system PATs (re.Auth == nil) get 403 because there is
// no user to meter against.

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// agenticUsageJSON is the metering section of the agentic response:
// today's post-call count, the caller's effective daily limit, and the
// LLM tokens this call consumed (as reported by the microservice).
type agenticUsageJSON struct {
	Today     int   `json:"today"`
	Limit     int   `json:"limit"`
	LLMTokens int64 `json:"llm_tokens"`
}

// agenticResponseJSON is the wire shape of POST /api/search/agentic.
// Results/candidates mirror POST /api/search (identity-anchored hits
// minted into the registry); conclusion, usage and the per-backend
// errors map come straight from the microservice contract.
type agenticResponseJSON struct {
	Results    []searchResultJSON `json:"results"`
	Candidates []search.Hit       `json:"candidates"`
	Conclusion *string            `json:"conclusion"`
	Usage      agenticUsageJSON   `json:"usage"`
	Errors     map[string]string  `json:"errors"`
}

// AgenticBackend is the backend behind POST /api/search/agentic: either
// the remote qatlas-search microservice (*search.RemoteProvider) or the
// local claude-CLI runner (*agentic.Runner), selected by
// search.agentic.backend. Both return the same standardized
// search.RemoteResponse, so the handler below is backend-agnostic.
// sources optionally pins the remote microservice backends (nil = its
// default tool list); the local runner ignores it.
type AgenticBackend interface {
	SearchAgentic(ctx context.Context, entry search.SearchEntry, agent bool, sources []string) (search.RemoteResponse, error)
}

// compile-time check: the remote microservice client is a backend.
var _ AgenticBackend = (*search.RemoteProvider)(nil)

// RegisterSearchAgentic mounts POST /api/search/agentic. backend is nil
// when neither search.remote nor the local backend is configured — the
// route still registers (so the surface is stable) but every call gets a
// 503. usageStore meters against the registry Postgres; engine is used
// only for its resolve-or-mint half (registry anchoring of the returned
// hits).
func RegisterSearchAgentic(se *core.ServeEvent, cfg *config.Config, backend AgenticBackend, usageStore *usage.Store, engine *search.Engine, enforcer *casbin.Enforcer) {
	se.Router.POST("/api/search/agentic", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		if backend == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "agentic search service not configured",
			})
		}
		if re.Auth == nil {
			// System PAT: no user record to meter against. Agentic
			// search is a per-user metered surface; ops scripts should
			// call the microservice directly.
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "agentic search requires a user-bound credential",
			})
		}
		userID := re.Auth.Id

		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "read search entry body: " + err.Error(),
			})
		}
		// The body is a standard SearchEntry plus two extension fields:
		// "agent" (default true) lets the caller skip the LLM conclusion
		// while still using the metered multi-source pipeline, and
		// "sources" pins the qatlas-search backends (null = default).
		var body struct {
			search.SearchEntry
			Agent   *bool    `json:"agent"`
			Sources []string `json:"sources"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "invalid search entry JSON: " + err.Error(),
			})
		}
		entry := body.SearchEntry
		entry.Normalize()
		agent := body.Agent == nil || *body.Agent
		sources := normalizeSources(body.Sources)

		ctx := re.Request.Context()
		limit, err := usageStore.EffectiveLimit(ctx, userID, cfg.AgenticDailyLimit)
		if err != nil {
			return agenticStoreError(re, err)
		}
		today, ok, err := usageStore.CheckAndReserve(ctx, userID, usage.MetricAgenticSearch, limit)
		if err != nil {
			return agenticStoreError(re, err)
		}
		if !ok {
			return re.JSON(http.StatusTooManyRequests, map[string]any{
				"detail": "daily agentic search limit reached",
				"usage":  map[string]int{"today": today, "limit": limit},
			})
		}

		resp, err := backend.SearchAgentic(ctx, entry, agent, sources)
		if err != nil {
			// Upstream failed: the user must not pay for a call we could
			// not fulfil — refund the reserved slot.
			if rErr := usageStore.Refund(ctx, userID, usage.MetricAgenticSearch); rErr != nil {
				slog.Error("agentic search: refund failed after upstream error",
					"user_id", userID, "refund_error", rErr, "upstream_error", err)
			}
			return re.JSON(http.StatusBadGateway, map[string]string{
				"detail": "agentic search upstream failed: " + err.Error(),
			})
		}
		if resp.Usage.LLMTokens > 0 {
			if tErr := usageStore.RecordTokens(ctx, userID, usage.MetricAgenticSearch, resp.Usage.LLMTokens); tErr != nil {
				// Best-effort: never fail a successful search over a
				// metering write.
				slog.Error("agentic search: record tokens failed",
					"user_id", userID, "error", tErr)
			}
		}

		hits := make([]search.Hit, 0, len(resp.Hits))
		for _, rh := range resp.Hits {
			hits = append(hits, rh.ToHit())
		}
		results, candidates, err := engine.MintHits(ctx, hits, entry.MaxResults)
		if err != nil {
			if errors.Is(err, registry.ErrCatalogUnavailable) {
				return re.JSON(http.StatusServiceUnavailable, map[string]string{
					"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
				})
			}
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}

		out := agenticResponseJSON{
			Results:    make([]searchResultJSON, 0, len(results)),
			Candidates: candidates,
			Conclusion: resp.Conclusion,
			Usage: agenticUsageJSON{
				Today:     today,
				Limit:     limit,
				LLMTokens: resp.Usage.LLMTokens,
			},
			Errors: resp.Errors,
		}
		if out.Candidates == nil {
			out.Candidates = []search.Hit{}
		}
		if out.Errors == nil {
			out.Errors = map[string]string{}
		}
		for _, r := range results {
			out.Results = append(out.Results, searchResultJSON{
				PaperID: r.PaperID,
				Hit:     r.Hit,
				Created: r.Created,
			})
		}
		return re.JSON(http.StatusOK, out)
	}))
}

// agenticStoreError maps a usage-store failure onto the usual catalog
// convention: 503 when Postgres is unavailable, 500 otherwise.
func agenticStoreError(re *core.RequestEvent, err error) error {
	if errors.Is(err, registry.ErrCatalogUnavailable) {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "catalog unavailable (PostgreSQL unreachable); retry shortly",
		})
	}
	return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
}
