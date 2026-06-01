// Package routes contains the QuantumAtlas HTTP handlers.
//
// authGuard returns a wrapper that gates a PocketBase route handler on
// the caller presenting a valid credential. Three credential types are
// accepted, in this order:
//
//  1. A system PAT (optional, env-loaded breaking-glass token) — the
//     plaintext from QATLAS_SYSTEM_PAT, compared constant-time.
//     Unbound to any users record (pb_data may be unavailable when
//     the operator needs this), authenticates as a synthetic system
//     identity with the scope set from QATLAS_SYSTEM_PAT_SCOPES
//     (defaults to ScopeMaster). Disabled when the env var is unset.
//
//  2. A QuantumAtlas Personal Access Token (PAT) — bearer string
//     starting with "qat_", looked up via the pat package and the
//     pat_tokens collection. Long-lived, user-managed via the SPA's
//     /pat page. The matched users record is mounted on re.Auth so
//     downstream handlers can treat the request like any other signed-
//     in user. The PAT's scope list is stashed in re via Set so the
//     scopeGuard middleware can enforce fine-grained access.
//
//  3. A PocketBase user JWT — the short-lived session token PocketBase
//     mints during the GitHub OAuth flow. The SPA holds it in
//     pb.authStore (browser localStorage) and the PocketBase middleware
//     that runs upstream of our handlers populates re.Auth from the
//     bearer; we only need to confirm the record belongs to the
//     "users" collection. Sessions implicitly get pat.ScopeMaster.
//
//     We do NOT expose a UI affordance to copy this token (there is
//     no /token page) — for non-browser callers, mint a PAT at /pat
//     (long-lived, scoped, revocable) or use the system PAT for
//     server-side ops scripts. A user JWT in a CI secret would force
//     a rotation every 14 days, which we explicitly reject.
//
// The system PAT is the only path that authenticates without a users
// record on re.Auth. sessionGuard (used by /api/pat) rejects it for
// the same reason it rejects user PATs: a leaked credential must not
// be able to mint more PAT records. Every other handler ignores
// re.Auth, so the system PAT works transparently for them.
//
// Endpoints that stay open without auth: /api/health, /api/server/info,
// /install-qatlasd.sh, /swagger/*, /api/pat/scopes (pure constant — the
// scope vocabulary), /share/{token} and /share/{token}/{path...} (the
// token IS the credential), the SPA shell at /{path...} (no data —
// data lives behind the gated APIs), and the PocketBase OAuth callback
// (/api/oauth2-redirect). Everything else — including wiki / papers /
// graph reads — requires authGuard plus the matching scopeGuard
// (wiki:read / papers:read / graph:read). The knowledge base is not
// anonymously readable; see docs/concepts/auth-model.md for the full
// rationale and the per-endpoint table.
//
// Two further wrappers layer on top of authGuard:
//
//   - sessionGuard: like authGuard but rejects PAT-authenticated
//     callers (both user and system PATs). Used by /api/pat itself
//     so a leaked PAT can't be used to mint more PATs (mirrors
//     GitHub fine-grained PAT design).
//
//   - scopeGuard: requires a specific (resource, action) scope. Used
//     by every read endpoint (*:read) and every write endpoint
//     (*:write). Session callers and system PATs holding ScopeMaster
//     bypass the check via the implicit short-circuit in pat.Allows.

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
	authSourceSession   = "session"
	authSourcePAT       = "pat"
	authSourceSystemPAT = "system-pat"
)

// systemPAT is the optional, env-loaded breaking-glass token. nil
// means the feature is disabled (the most common case). Set once
// at server startup via UseSystemPAT and never mutated afterwards.
//
// Stored as a package-level singleton (rather than threaded into
// every handler signature) because:
//
//   - It is server-wide state with a single source of truth (one
//     env var, one in-memory value).
//   - isAuthorized is the only consumer, and it cannot easily be
//     parameterised without changing the cobra-mounted middleware
//     signatures every other route depends on.
//   - Tests that need a custom system PAT call UseSystemPAT in
//     their setup and reset it with t.Cleanup; isolation is fine
//     because each test runs in a single goroutine sequence.
var systemPAT *pat.SystemPAT

// UseSystemPAT mounts the loaded SystemPAT for authGuard to
// consult. Call once at server startup, before any request is
// served. Passing nil disables the feature explicitly (the
// default zero value also disables it; UseSystemPAT(nil) is for
// tests that want to clear a previously-set value).
func UseSystemPAT(s *pat.SystemPAT) {
	systemPAT = s
}

// authGuard wraps a handler so it is only invoked for authenticated
// callers. Returns 401 otherwise. The caller's auth state is
// established either by PocketBase's own auth middleware (for JWTs)
// or by us, in-place, after a successful PAT lookup (for "qat_"
// bearers).
func authGuard(handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if !isAuthorized(re) {
			return re.JSON(http.StatusUnauthorized, map[string]string{
				"detail": "authentication required (sign in at /login then mint a PAT at /pat, or set QATLAS_SYSTEM_PAT on the server for ops scripts)",
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
				"detail": "this endpoint requires a browser session token (PAT auth is not accepted for PAT management — open the SPA and sign in at /login)",
			})
		}
		return handler(re)
	})
}

// isAuthorized returns true when the request carries an acceptable
// credential. It checks three paths, in this order:
//
//  1. System PAT path (optional, env-configured): if a SystemPAT is
//     mounted via UseSystemPAT and the bearer matches it bit-for-bit
//     (constant-time), we mark the request as system-sourced and
//     stash the configured scope list. re.Auth STAYS nil — system
//     PATs are not tied to any users row; handlers that need a
//     user record (currently only /api/pat itself) are gated by
//     sessionGuard which rejects this source.
//
//  2. User PAT path: if the Authorization header is "Bearer qat_..."
//     we resolve it against the pat_tokens collection. On success
//     we mount the linked users record on re.Auth (so downstream
//     handlers behave the same as for a JWT-authed request), stash
//     the granted scope list under authScopesKey, mark the request
//     as PAT-sourced, and fire-and-forget a last_used_at bump.
//
//  3. Session fallback: trust whatever PocketBase's own middleware
//     put on re.Auth — must be a record in the "users" collection.
//     We mark the request as session-sourced and grant the master
//     scope so downstream scopeGuard checks are no-ops.
//
// Admin allowlist gating remains a separate concern handled by a
// future requireAdmin wrapper.
func isAuthorized(re *core.RequestEvent) bool {
	token := bearerToken(re)

	// System PAT: cheap constant-time check; falls through on miss
	// (or when the feature is disabled / token shape doesn't fit).
	// Runs first so it works even when pb_data is unavailable
	// (the breaking-glass case the feature exists for).
	if scopes, ok := systemPAT.Match(token); ok {
		re.Auth = nil
		re.Set(authSourceKey, authSourceSystemPAT)
		re.Set(authScopesKey, scopes)
		return true
	}

	if pat.Looks(token) {
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

// IsCallerAuthenticated is a side-effect-free authentication probe
// for handlers that want to vary the response shape based on
// credentials but don't want to use authGuard (which always 401s on
// the unauthenticated path) and don't want isAuthorized's side
// effects (mutating re.Auth / re.Set / fire-and-forget DB writes).
//
// Recognises two of the three credential types isAuthorized accepts:
//
//   - system PAT: constant-time bearer match. No DB hit. No state
//     mutated. Returns true on match.
//   - PocketBase session JWT: same check as isAuthorized — re.Auth
//     populated by PocketBase's own middleware AND belongs to the
//     users collection. Returns true if both.
//
// User PATs ("qat_...") are NOT recognised here, by design. The only
// caller is /api/health, which is hit by liveness monitors at high
// frequency; resolving a user PAT would mean a SQLite lookup +
// async last_used_at bump on every probe — turning the health route
// into a side-effecting write path is exactly the wrong shape for
// a probe endpoint. User PAT holders that want detailed health can
// either browse the SPA (the in-browser pb.authStore session is
// recognised here) or use the system PAT for ops scripts.
//
// Returns true iff one of the recognised credentials checks out,
// false otherwise. Never returns an error: missing / malformed /
// expired credentials all collapse to false so callers can use a
// plain if-statement.
func IsCallerAuthenticated(re *core.RequestEvent) bool {
	if re == nil {
		return false
	}
	if token := bearerToken(re); token != "" {
		if _, ok := systemPAT.Match(token); ok {
			return true
		}
	}
	if re.Auth == nil || re.Auth.Collection() == nil {
		return false
	}
	return re.Auth.Collection().Name == "users"
}
