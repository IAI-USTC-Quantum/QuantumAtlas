package routes

// admin_usage.go: the metering admin surface — per-user daily usage,
// plan management, and per-user quota overrides for the agentic-search
// meter (internal/usage). All endpoints sit behind adminGuard (browser
// session + GitHub admin allowlist), same as the rest of /api/admin/*.
//
//	GET /api/admin/usage?day=YYYY-MM-DD   — per-user counters for one
//	    day (default: today, database CURRENT_DATE), joined with the
//	    PocketBase users collection for the GitHub login and priced via
//	    search.agentic.price_per_mtok.
//	GET /api/admin/plans                  — list the plan table.
//	PUT /api/admin/plans/{name}           — upsert one plan
//	    ({daily_agentic_search_limit, description?}).
//	PUT /api/admin/quotas/{user_id}       — upsert a user's plan binding
//	    and/or hard daily-limit override
//	    ({plan?, daily_limit_override?: int|null}).

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"

	"github.com/pocketbase/pocketbase/core"
)

// adminUsageRow is one user's (metric, day) bucket priced and annotated
// for the admin view. Cost is llm_tokens / 1e6 * price_per_mtok USD —
// display-only, it gates nothing.
type adminUsageRow struct {
	UserID         string  `json:"user_id"`
	Login          string  `json:"login"`
	Plan           string  `json:"plan"`
	EffectiveLimit int     `json:"effective_limit"`
	Metric         string  `json:"metric"`
	Count          int     `json:"count"`
	LLMTokens      int64   `json:"llm_tokens"`
	Cost           float64 `json:"cost"`
}

// registerAdminUsage mounts the metering endpoints. app resolves user
// logins from the users collection; usageStore reads the metering
// tables (nil pool → the 503 catalog-unavailable convention).
func registerAdminUsage(se *core.ServeEvent, cfg *config.Config, app core.App, usageStore *usage.Store) {
	se.Router.GET("/api/admin/usage", adminGuard(cfg, adminUsageHandler(cfg, app, usageStore)))
	se.Router.GET("/api/admin/plans", adminGuard(cfg, adminListPlansHandler(usageStore)))
	se.Router.PUT("/api/admin/plans/{name}", adminGuard(cfg, adminUpsertPlanHandler(usageStore)))
	se.Router.PUT("/api/admin/quotas/{user_id}", adminGuard(cfg, adminUpsertQuotaHandler(usageStore)))
}

// adminUsageHandler answers GET /api/admin/usage.
func adminUsageHandler(cfg *config.Config, app core.App, usageStore *usage.Store) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		day := re.Request.URL.Query().Get("day")
		if day != "" {
			if _, err := time.Parse("2006-01-02", day); err != nil {
				return re.JSON(http.StatusBadRequest, map[string]string{
					"detail": "day must be YYYY-MM-DD, got " + day,
				})
			}
		}
		rows, err := usageStore.DailyUsage(re.Request.Context(), day, cfg.AgenticDailyLimit)
		if err != nil {
			return adminUsageStoreError(re, err)
		}

		logins := resolveUserLogins(app, rows)
		out := make([]adminUsageRow, 0, len(rows))
		for _, u := range rows {
			out = append(out, adminUsageRow{
				UserID:         u.UserID,
				Login:          logins[u.UserID],
				Plan:           u.Plan,
				EffectiveLimit: u.EffectiveLimit,
				Metric:         u.Metric,
				Count:          u.Count,
				LLMTokens:      u.LLMTokens,
				Cost:           float64(u.LLMTokens) / 1e6 * cfg.AgenticPricePerMtok,
			})
		}
		return re.JSON(http.StatusOK, map[string]any{
			"day":   day,
			"users": out,
		})
	}
}

// resolveUserLogins maps users-record ids to their github_login. Users
// that no longer resolve (deleted account, lookup failure) are left
// empty — usage rows outlive the accounts that produced them.
func resolveUserLogins(app core.App, rows []usage.UserUsage) map[string]string {
	ids := make([]string, 0, len(rows))
	seen := map[string]bool{}
	for _, u := range rows {
		if !seen[u.UserID] {
			seen[u.UserID] = true
			ids = append(ids, u.UserID)
		}
	}
	out := map[string]string{}
	if len(ids) == 0 {
		return out
	}
	recs, err := app.FindRecordsByIds(auth.UsersCollection, ids)
	if err != nil {
		return out
	}
	for _, rec := range recs {
		out[rec.Id] = rec.GetString(auth.GitHubLoginField)
	}
	return out
}

// adminListPlansHandler answers GET /api/admin/plans.
func adminListPlansHandler(usageStore *usage.Store) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		plans, err := usageStore.ListPlans(re.Request.Context())
		if err != nil {
			return adminUsageStoreError(re, err)
		}
		return re.JSON(http.StatusOK, map[string]any{"plans": plans})
	}
}

// adminPlanBody is the PUT /api/admin/plans/{name} request body.
type adminPlanBody struct {
	DailyLimit  *int   `json:"daily_agentic_search_limit"`
	Description string `json:"description"`
}

// adminUpsertPlanHandler answers PUT /api/admin/plans/{name}.
func adminUpsertPlanHandler(usageStore *usage.Store) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		name := re.Request.PathValue("name")
		if name == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "plan name required"})
		}
		var body adminPlanBody
		if err := decodeAdminBody(re, &body); err != nil {
			return err
		}
		if body.DailyLimit == nil || *body.DailyLimit < 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "daily_agentic_search_limit must be a non-negative integer",
			})
		}
		err := usageStore.UpsertPlan(re.Request.Context(), usage.Plan{
			Name:        name,
			DailyLimit:  *body.DailyLimit,
			Description: body.Description,
		})
		if err != nil {
			return adminUsageStoreError(re, err)
		}
		return re.JSON(http.StatusOK, map[string]any{"ok": true, "plan": name})
	}
}

// adminQuotaBody is the PUT /api/admin/quotas/{user_id} request body.
// Both fields optional; daily_limit_override accepts null to clear.
type adminQuotaBody struct {
	Plan     *string `json:"plan"`
	Override *int    `json:"daily_limit_override"`
}

// adminUpsertQuotaHandler answers PUT /api/admin/quotas/{user_id}.
func adminUpsertQuotaHandler(usageStore *usage.Store) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		userID := re.Request.PathValue("user_id")
		if userID == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "user id required"})
		}
		var body adminQuotaBody
		if err := decodeAdminBody(re, &body); err != nil {
			return err
		}
		if body.Plan != nil {
			exists, err := usageStore.PlanExists(re.Request.Context(), *body.Plan)
			if err != nil {
				return adminUsageStoreError(re, err)
			}
			if !exists {
				return re.JSON(http.StatusBadRequest, map[string]string{
					"detail": "unknown plan: " + *body.Plan,
				})
			}
		}
		if body.Override != nil && *body.Override < 0 {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "daily_limit_override must be >= 0 or null",
			})
		}
		if err := usageStore.UpsertUserQuota(re.Request.Context(), userID, body.Plan, body.Override); err != nil {
			return adminUsageStoreError(re, err)
		}
		return re.JSON(http.StatusOK, map[string]any{"ok": true, "user_id": userID})
	}
}

// decodeAdminBody reads and decodes a JSON request body, writing the
// 400 response itself on failure.
func decodeAdminBody(re *core.RequestEvent, dst any) error {
	raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
	if err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON body: " + err.Error()})
	}
	return nil
}

// adminUsageStoreError maps metering-store failures onto the catalog
// convention: 503 when Postgres is unavailable, 500 otherwise.
func adminUsageStoreError(re *core.RequestEvent, err error) error {
	if errors.Is(err, registry.ErrCatalogUnavailable) {
		return re.JSON(http.StatusServiceUnavailable, map[string]string{
			"detail": "postgres registry unavailable (usage metering tables unreachable); retry shortly",
		})
	}
	return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
}
