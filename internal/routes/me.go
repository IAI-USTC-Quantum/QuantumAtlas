// "Me" API — the read-only self-service surface behind the SPA's user
// dashboard.
//
//	GET /api/me        — sessionGuard; the caller's own profile: id,
//	                     email, name, avatar (PocketBase file name — the
//	                     SPA builds the URL via pb.files.getURL),
//	                     github_login, gitea_login, github_bound,
//	                     gitea_bound, is_admin, is_superadmin, created.
//	GET /api/me/usage  — sessionGuard; the caller's metering state for
//	                     the agentic-search surface: today's call count,
//	                     the effective daily limit (per-user override >
//	                     plan > search.agentic.daily_limit) and the LLM
//	                     tokens consumed today. 503 when the registry
//	                     Postgres is unavailable (same convention as the
//	                     admin usage endpoints).
//
// Both endpoints are session-token-only (PAT auth refused by
// sessionGuard, same as /api/pat): the dashboard is a browser feature.
package routes

import (
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/core"
	pbauth "github.com/pocketbase/pocketbase/tools/auth"
)

// RegisterMe wires the /api/me surface. usageStore may have a nil pool —
// the usage handler then reports 503 while the profile handler keeps
// working (profile data lives in PocketBase, not Postgres).
func RegisterMe(se *core.ServeEvent, cfg *config.Config, usageStore *usage.Store) {
	se.Router.GET("/api/me", sessionGuard(meProfileHandler(cfg)))
	se.Router.GET("/api/me/usage", sessionGuard(meUsageHandler(cfg, usageStore)))
}

// meProfileHandler returns the caller's own users-record fields. Unlike
// /api/admin/whoami (login + admin flag only) this is the full profile
// the dashboard renders. is_admin / is_superadmin consult both
// providers' allowlists (GitHub against github_login, Gitea against
// gitea_login), and *_bound reports which OAuth2 identities are linked
// to the record (drives the dashboard's 账号绑定 panels).
func meProfileHandler(cfg *config.Config) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		user := re.Auth // sessionGuard guarantees non-nil + browser-sourced
		login := user.GetString(auth.GitHubLoginField)
		giteaLogin := user.GetString(auth.GiteaLoginField)
		return re.JSON(http.StatusOK, map[string]any{
			"id":            user.Id,
			"email":         user.GetString("email"),
			"name":          user.GetString("name"),
			"avatar":        user.GetString("avatar"),
			"github_login":  login,
			"gitea_login":   giteaLogin,
			"github_bound":  providerBound(re.App, user, pbauth.NameGithub),
			"gitea_bound":   providerBound(re.App, user, pbauth.NameGitea),
			"is_admin":      cfg.IsGitHubAdmin(login) || cfg.IsGiteaAdmin(giteaLogin),
			"is_superadmin": user.GetBool(auth.IsSuperadminField) || cfg.IsGitHubSuperadmin(login) || cfg.IsGiteaSuperadmin(giteaLogin),
			"created":       user.GetDateTime("created").String(),
		})
	}
}

// providerBound reports whether the users record has an _externalAuths
// relation for the given OAuth2 provider — the authoritative "this
// identity can sign in as this account" signal (the *_login fields are
// display-only and are stamped independently of the link).
func providerBound(app core.App, user *core.Record, provider string) bool {
	rel, err := app.FindFirstExternalAuthByExpr(dbx.HashExp{
		"collectionRef": user.Collection().Id,
		"recordRef":     user.Id,
		"provider":      provider,
	})
	return err == nil && rel != nil
}

// meUsageResponse is the wire shape of GET /api/me/usage — deliberately
// mirroring agenticUsageJSON (search_agentic.go) so the SPA handles one
// metering shape everywhere.
type meUsageResponse struct {
	Metric    string `json:"metric"`
	Today     int    `json:"today"`
	Limit     int    `json:"limit"`
	LLMTokens int64  `json:"llm_tokens"`
}

func meUsageHandler(cfg *config.Config, usageStore *usage.Store) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		userID := re.Auth.Id
		ctx := re.Request.Context()
		limit, err := usageStore.EffectiveLimit(ctx, userID, cfg.AgenticDailyLimit)
		if err != nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "usage store unavailable (postgres registry not configured)",
			})
		}
		today, llmTokens, err := usageStore.UserDailyUsage(ctx, userID, usage.MetricAgenticSearch)
		if err != nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "usage store unavailable (postgres registry not configured)",
			})
		}
		return re.JSON(http.StatusOK, meUsageResponse{
			Metric:    usage.MetricAgenticSearch,
			Today:     today,
			Limit:     limit,
			LLMTokens: llmTokens,
		})
	}
}
