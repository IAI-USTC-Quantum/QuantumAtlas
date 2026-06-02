// scopeGuard wires per-endpoint scope enforcement on top of authGuard.
//
// The two-layer design keeps authentication (who you are) and
// authorization (what you can do) cleanly separated:
//
//   authGuard      → resolves credential, mounts re.Auth + auth source
//                    + scope list. Returns 401 if unauthenticated.
//   scopeGuard     → requires a specific (obj, act) scope. Calls
//                    pat.Allows against the request's stashed scope
//                    list. Returns 403 if the scope is missing.
//                    Session callers bypass this check (their
//                    implicit ScopeMaster from isAuthorized makes
//                    Allows short-circuit to true).
//
// Putting the scope label on the endpoint keeps the policy table next
// to the wire surface (easy to audit "what does papers:write actually
// grant?" by grepping for scopeGuard calls). The casbin enforcer is
// just the rules engine — the *mapping* of endpoints to scope labels
// lives in plain Go.

package routes

import (
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

// scopeGuard returns a handler that first runs authGuard, then
// asserts the authenticated caller holds a scope covering (obj, act).
// The casbin enforcer is injected (rather than read from a package
// global) so tests can construct their own and main.go retains
// single-source-of-truth over startup-time wiring.
//
// On scope-deny, the response is a 403 with the requested (obj, act)
// echoed in the detail so the CLI user knows exactly which scope to
// add to their PAT.
func scopeGuard(enforcer *casbin.Enforcer, obj, act string, handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	if enforcer == nil {
		// Wire-time misconfiguration — fail loud on the very first
		// request rather than silently allowing every PAT through.
		return func(re *core.RequestEvent) error {
			return re.JSON(http.StatusInternalServerError, map[string]string{
				"detail": "scopeGuard: enforcer not configured (server wiring bug)",
			})
		}
	}
	return authGuard(func(re *core.RequestEvent) error {
		held, _ := re.Get(authScopesKey).([]string)
		ok, err := pat.Allows(enforcer, held, obj, act)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{
				"detail": "scope check failed: " + err.Error(),
			})
		}
		if !ok {
			return re.JSON(http.StatusForbidden, map[string]string{
				"detail": "insufficient scope: this token lacks " + obj + ":" + act + " (mint a new PAT at /pat with the required scope)",
				"obj":    obj,
				"act":    act,
			})
		}
		return handler(re)
	})
}
