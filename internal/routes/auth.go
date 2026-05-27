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
//     in user. The PAT's scope list is stashed in re via Set so the
//     scopeGuard middleware can enforce fine-grained access.
//
//  2. A PocketBase user JWT — short-lived (default 14d), issued by the
//     GitHub OAuth flow and surfaced on the SPA's /token page. The
//     PocketBase middleware that runs upstream of our handlers already
//     populates re.Auth from this header; we only need to confirm the
//     record belongs to the "users" collection. Sessions implicitly
//     get pat.ScopeMaster — what the user can do in the SPA, the
//     token they copy from /token can do too.
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
// Two further wrappers layer on top of authGuard:
//
//   - sessionGuard: like authGuard but rejects PAT-authenticated
//     callers. Used by /api/pat itself so a leaked PAT can't be used
//     to mint more PATs (mirrors GitHub fine-grained PAT design).
//
//   - scopeGuard: requires a specific (resource, action) scope. Used
//     by every write endpoint. Session callers bypass the check.

package routes

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/core"
)

// Request-event store keys for auth state populated by isAuthorized.
// String values, not types, so the keys are robust against package
// reorgs and are inspectable from any handler.
const (
	authSourceKey = "qatlas.auth.source"
	authScopesKey = "qatlas.auth.scopes"
)

// authSource discriminates how the current request was authenticated.
// Used by sessionGuard (to refuse PAT-auth on PAT-management
// endpoints) and by scopeGuard (to bypass scope checks for session
// callers). Stored as a string in the request event store so callers
// don't need to know about this type alias.
const (
	authSourceSession = "session"
	authSourcePAT     = "pat"
)

// authGuard wraps a handler so it is only invoked for authenticated
// callers. Returns 401 otherwise. The caller's auth state is
// established either by PocketBase's own auth middleware (for JWTs)
// or by us, in-place, after a successful PAT lookup (for "qat_"
// bearers).
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

// sessionGuard is a stricter variant of authGuard that ALSO rejects
// PAT-authenticated callers. Used on /api/pat — a PAT must not be
// usable to mint more PATs (a leaked PAT would otherwise replicate
// itself). Mirrors GitHub fine-grained PAT design: PAT management
// requires the interactive web UI session, not a long-lived token.
func sessionGuard(handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return authGuard(func(re *core.RequestEvent) error {
		if source, _ := re.Get(authSourceKey).(string); source != authSourceSession {
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "this endpoint requires a browser session token (PAT auth is not accepted for PAT management — sign in at /login and use the token from /token)",
			})
		}
		return handler(re)
	})
}

// isAuthorized returns true when the request carries an acceptable
// credential. It does two things, in order:
//
//  1. PAT path: if the Authorization header is "Bearer qat_..." we
//     resolve it against the pat_tokens collection. On success we
//     mount the linked users record on re.Auth (so downstream
//     handlers behave the same as for a JWT-authed request), stash
//     the granted scope list under authScopesKey, mark the request
//     as PAT-sourced, and fire-and-forget a last_used_at bump.
//
//  2. Fallback: trust whatever PocketBase's own middleware put on
//     re.Auth — must be a record in the "users" collection. We mark
//     the request as session-sourced and grant the master scope so
//     downstream scopeGuard checks are no-ops.
//
// Admin allowlist gating remains a separate concern handled by a
// future requireAdmin wrapper.
func isAuthorized(re *core.RequestEvent) bool {
	if token := bearerToken(re); pat.Looks(token) {
		patRec, userRec, err := pat.Lookup(re.App, token)
		if err != nil {
			return false
		}
		re.Auth = userRec
		re.Set(authSourceKey, authSourcePAT)
		re.Set(authScopesKey, decodeScopes(patRec.GetString("scopes")))
		markPATUsed(re.App, patRec)
		return true
	}

	if re.Auth == nil || re.Auth.Collection() == nil {
		return false
	}
	if re.Auth.Collection().Name != "users" {
		return false
	}
	re.Set(authSourceKey, authSourceSession)
	re.Set(authScopesKey, []string{pat.ScopeMaster})
	return true
}

// bearerToken extracts the bearer value from the Authorization
// header, or "" if absent / malformed. Case-insensitive on the
// "Bearer " scheme prefix for compatibility with curl/CLI quirks.
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

// decodeScopes parses the JSON-encoded scopes column on a pat_tokens
// record. Tolerant of all the "empty" representations (literal empty
// string, "null", "[]", whitespace) — all collapse to a nil slice
// which Allows() correctly treats as "no permissions".
func decodeScopes(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var out []string
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		// Corrupt JSON in the DB is exceptional; fail closed (no
		// permissions) rather than fail open. The PAT's prefix + ID
		// will surface in audit logs from MarkUsed even on denied
		// requests, so the operator can investigate.
		return nil
	}
	return out
}
