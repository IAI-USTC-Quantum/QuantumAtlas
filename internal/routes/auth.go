// Package routes contains the QuantumAtlas HTTP handlers.
//
// authGuard returns a wrapper that gates a PocketBase route handler on
// the caller presenting a valid credential. Two credential types are
// accepted, in this order:
//
//  1. A QuantumAtlas Personal Access Token (PAT) — bearer string
//     starting with "qat_", looked up via the pat package and the
//     pat_tokens collection. Long-lived, user-managed via the SPA's
//     /pat page. The matched users record is mounted on re.Auth so
//     downstream handlers can treat the request like any other signed-
//     in user.
//
//  2. A PocketBase user JWT — short-lived (default 14d), issued by the
//     GitHub OAuth flow and surfaced on the SPA's /token page. The
//     PocketBase middleware that runs upstream of our handlers already
//     populates re.Auth from this header; we only need to confirm the
//     record belongs to the "users" collection.
//
// There is no shared-secret fallback by design — every CLI / browser
// caller goes through the same auth surface and ends up with a per-
// user record on re.Auth.
//
// Read endpoints (wiki, pages, stats, search, graph, /api/server/info,
// /api/oauth2-redirect, /share/{token}, /health) stay open without
// auth. The wiki repo is public so no sensitive data leaks; only
// write-side abuse of the public surface needs blocking.
//
// Future hardening: admin-only endpoints additionally require the
// authenticated user's GitHub login to appear in
// QATLAS_ADMIN_GITHUB_LOGINS (handler not yet wired; see TODO in
// internal/auth/oauth.go).

package routes

import (
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/core"
)

// authGuard wraps a handler so it is only invoked for authenticated
// callers. Returns 401 otherwise. The caller's auth state is
// established either by PocketBase's own auth middleware (for JWTs) or
// by us, in-place, after a successful PAT lookup (for "qat_" bearers).
func authGuard(handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if !isAuthorized(re) {
			return re.JSON(http.StatusUnauthorized, map[string]string{
				"detail": "authentication required (sign in at /login, then send 'Authorization: Bearer <token>' from /token, or mint a long-lived PAT at /pat)",
			})
		}
		return handler(re)
	}
}

// isAuthorized returns true when the request carries an acceptable
// credential. It does two things, in order:
//
//  1. PAT path: if the Authorization header is "Bearer qat_..." we
//     resolve it against the pat_tokens collection. On success we
//     mount the linked users record on re.Auth (so downstream handlers
//     behave the same as for a JWT-authed request) and fire-and-forget
//     a last_used_at bump in the background.
//
//  2. Fallback: trust whatever PocketBase's own middleware put on
//     re.Auth — must be a record in the "users" collection.
//
// Admin allowlist gating remains a separate concern handled by a
// future requireAdmin wrapper.
func isAuthorized(re *core.RequestEvent) bool {
	if token := bearerToken(re); pat.Looks(token) {
		// Authoritative PAT path: even if PocketBase already attached
		// a stale re.Auth (shouldn't happen for "qat_" headers since
		// they're not valid JWTs), we re-resolve and overwrite.
		patRec, userRec, err := pat.Lookup(re.App, token)
		if err != nil {
			return false
		}
		re.Auth = userRec
		markPATUsed(re.App, patRec)
		return true
	}

	if re.Auth == nil || re.Auth.Collection() == nil {
		return false
	}
	return re.Auth.Collection().Name == "users"
}

// bearerToken extracts the bearer value from the Authorization header,
// or "" if absent / malformed. Case-insensitive on the "Bearer "
// scheme prefix for compatibility with curl/CLI quirks.
func bearerToken(re *core.RequestEvent) string {
	if re.Request == nil {
		return ""
	}
	raw := re.Request.Header.Get("Authorization")
	if raw == "" {
		return ""
	}
	const prefix = "bearer "
	if len(raw) <= len(prefix) || !strings.EqualFold(raw[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(raw[len(prefix):])
}
