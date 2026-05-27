// Package routes contains the QuantumAtlas HTTP handlers.
//
// authGuard returns a wrapper that gates a PocketBase route handler on the
// caller presenting a valid PocketBase user auth token. There is no
// shared-secret fallback by design — every CLI / browser caller goes
// through the same OAuth flow and ends up with a per-user, time-bounded
// PocketBase JWT in their Authorization header.
//
// Get a token from the SPA Token page at https://<server>/token after
// signing in with GitHub. PocketBase user tokens default to a 14-day
// lifetime; refresh by re-visiting /token (or calling
// /api/collections/users/auth-refresh).
//
// Read endpoints (wiki, pages, stats, search, graph, /api/server/info,
// /api/oauth2-redirect, /share/{token}, /health) stay open without auth.
// The wiki repo is public so no sensitive data leaks; only write-side
// abuse of the public surface needs blocking.
//
// Future hardening: admin-only endpoints additionally require the
// authenticated user's GitHub login to appear in
// QATLAS_ADMIN_GITHUB_LOGINS (handler not yet wired; see TODO in
// internal/auth/oauth.go).

package routes

import (
	"net/http"

	"github.com/pocketbase/pocketbase/core"
)

// authGuard wraps a handler so it is only invoked for authenticated
// callers. Returns 401 otherwise. The caller's auth state is established
// by PocketBase's own auth middleware on re.Auth.
func authGuard(handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if !isAuthorized(re) {
			return re.JSON(http.StatusUnauthorized, map[string]string{
				"detail": "authentication required (sign in at /login, then send 'Authorization: Bearer <token>' from /token)",
			})
		}
		return handler(re)
	}
}

// isAuthorized returns true when the request carries a valid PocketBase
// user auth token (any record in the "users" collection counts). Admin
// allowlist gating is a separate concern handled by a future
// requireAdmin wrapper.
func isAuthorized(re *core.RequestEvent) bool {
	if re.Auth == nil || re.Auth.Collection() == nil {
		return false
	}
	return re.Auth.Collection().Name == "users"
}
