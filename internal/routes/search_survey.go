package routes

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/userkeys"
	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

type surveyBackend interface {
	SearchSurvey(context.Context, search.SurveyRequest, map[string]string) (search.SurveyResponse, error)
}

type SurveyOptions struct {
	Config *config.Config
	Usage  *usage.Store
}

// RegisterSearchSurvey exposes bounded rule-based retrieval; no proof scheduling.
func RegisterSearchSurvey(se *core.ServeEvent, keys *userkeys.Store, backend MultiBackend, engine *search.Engine, catalog *registry.Store, enforcer *casbin.Enforcer, options ...SurveyOptions) {
	se.Router.POST("/api/search/survey", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		provider, ok := backend.(surveyBackend)
		if !ok {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "survey search backend unavailable"})
		}
		var req search.SurveyRequest
		// Read the body into memory first: PocketBase v0.38 wraps every
		// request body in a RereadableReadCloser that *rewinds* on EOF, so a
		// trailing-garbage check via a second Decode on the raw request body
		// would see the replayed body as a "second JSON value" (works over
		// mux-direct tests, fails over real TCP). A bytes.Reader has no such
		// rewind, so the EOF check below is reliable.
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid survey request"})
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid survey request"})
		}
		var trailing any
		if decoder.Decode(&trailing) != io.EOF {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "multiple JSON values"})
		}
		req.Sources = normalizeSources(req.Sources)
		if strings.TrimSpace(req.Goal) == "" || len(req.Goal) > 16000 || len(req.Queries) > 6 || len(req.Sources) == 0 || req.MaxResults < 0 || req.MaxResults > 100 {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "goal, sources and bounded query/result counts required"})
		}
		apiKeys := map[string]string{}
		if re.Auth != nil && keys != nil && keys.Enabled() {
			available, err := keys.GetKeys(re.Auth.Id)
			if err != nil {
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "cannot load search keys"})
			}
			for _, name := range req.Sources {
				if key := available[name]; key != "" {
					apiKeys[name] = key
				}
			}
		}
		var meter *usage.Store
		var today, limit int
		if req.Agentic {
			if re.Auth == nil {
				return re.JSON(http.StatusForbidden, map[string]string{"detail": "agentic survey requires a user-bound credential"})
			}
			if len(options) == 0 || options[0].Config == nil || options[0].Usage == nil {
				return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "agentic metering unavailable"})
			}
			meter = options[0].Usage
			var err error
			limit, err = meter.EffectiveLimit(re.Request.Context(), re.Auth.Id, options[0].Config.AgenticDailyLimit)
			if err != nil {
				return agenticStoreError(re, err)
			}
			var allowed bool
			today, allowed, err = meter.CheckAndReserve(re.Request.Context(), re.Auth.Id, usage.MetricAgenticSearch, limit)
			if err != nil {
				return agenticStoreError(re, err)
			}
			if !allowed {
				return re.JSON(http.StatusTooManyRequests, map[string]any{"detail": "daily agentic search limit reached"})
			}
		}
		out, err := provider.SearchSurvey(re.Request.Context(), req, apiKeys)
		if err != nil {
			if meter != nil {
				_ = meter.Refund(re.Request.Context(), re.Auth.Id, usage.MetricAgenticSearch)
			}
			return re.JSON(http.StatusBadGateway, map[string]string{"detail": "survey search upstream failed"})
		}
		if meter != nil && out.Usage.LLMTokens > 0 {
			_ = meter.RecordTokens(re.Request.Context(), re.Auth.Id, usage.MetricAgenticSearch, out.Usage.LLMTokens)
		}
		if req.Agentic {
			out.Metering = map[string]int{"today": today, "limit": limit}
		}
		// Only filtered results are minted; irrelevant candidates remain upstream.
		if engine != nil {
			hits := []search.Hit{}
			for _, hit := range out.Hits {
				if hit.DOI != "" || hit.ArxivID != "" {
					hits = append(hits, hit.ToHit())
				}
			}
			if len(hits) > 0 {
				minted, _, _ := engine.MintHits(re.Request.Context(), hits, len(hits))
				anchors := multiAnchors(minted)
				ids := []string{}
				for _, a := range anchors {
					ids = append(ids, a.PaperID)
				}
				attachMultiAnchors(map[string][]search.RemoteHit{"survey": out.Hits}, anchors, paperSummaries(re.Request.Context(), catalog, ids))
			}
		}
		return re.JSON(http.StatusOK, out)
	}))
}
