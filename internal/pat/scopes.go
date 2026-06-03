// PAT scope vocabulary + casbin-backed enforcement.
//
// Modelled on GitHub's fine-grained Personal Access Tokens (the new
// kind, not the legacy "classic" ones): a PAT carries an explicit
// allow-list of scopes; an empty list means "this token can call
// nothing"; there is no implicit master-grant. Sessions tokens (the
// PocketBase user JWT issued by browser OAuth and held by the SPA
// in pb.authStore) keep their full permissions and bypass scope
// checks entirely — what the user can do in the SPA, anything
// authenticated as that session can do too.
//
// Why bring in casbin instead of `slices.Contains`? Two reasons:
//
//  1. Policy / code separation. The (scope, obj, act) authorization
//     table lives in one centralised data structure, not scattered
//     across endpoint handlers. Adding a new scope = appending a row
//     to scopePolicies, not editing N handlers.
//
//  2. Future-proofing for path-pattern scopes. If a future scope
//     needs to be "papers:write but only for /api/papers/quant-ph/*",
//     we replace the equality matcher with casbin's keyMatch / glob
//     without rewriting any call sites.
//
// The model is intentionally stateless: each scope is treated as its
// own subject. Holding multiple scopes = iterating the held list and
// asking the enforcer about each. This avoids the
// add-grouping-then-remove dance that the conventional casbin RBAC
// model would require for per-request user→role bindings.

package pat

import (
	"errors"
	"fmt"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
)

// Scope vocabulary. Add new entries here; the SPA discovers them via
// AllScopes (sent down with the create-PAT page). Keep the
// "<resource>:<action>" naming convention so future tooling (rate
// limits, audit logs) can group by resource easily.
const (
	ScopeWikiRead    = "wiki:read"    // GET /api/pages*, /api/stats, /api/search, /api/wiki/sync/status
	ScopePapersRead  = "papers:read"  // GET /api/papers/{path...} (stats / needs-mineru only in OSS edition)
	ScopePapersWrite = "papers:write" // upload-pdf / upload-mineru / mineru-claim CRUD (implies papers:read)
	ScopeGraphRead   = "graph:read"   // GET /api/graph/stats, GET /api/graph/schema, POST /api/graph/query
	ScopeWikiWrite   = "wiki:write"   // POST /api/wiki/sync/pull (server-side git fast-forward; implies wiki:read)

	// ScopeMaster is the wildcard internal-only scope assigned to
	// PocketBase session tokens (browser users). Never accepted as
	// user input — ValidateScopes rejects it — but used as a
	// short-circuit inside Allows so the casbin enforcer doesn't
	// have to enumerate every (obj, act) pair when a session-token
	// caller is making the request.
	ScopeMaster = "*"
)

// ScopeDescription supplies one-line human-readable copy for the SPA
// scope picker. Keep these short — they appear next to a checkbox.
var ScopeDescription = map[string]string{
	ScopeWikiRead:    "Read wiki pages, stats, search and sync status",
	ScopePapersRead:  "Read paper catalog (stats, needs-mineru)",
	ScopePapersWrite: "Upload paper PDFs and submit MinerU markdown (includes read)",
	ScopeGraphRead:   "Read the knowledge graph: stats, schema and read-only Cypher",
	ScopeWikiWrite:   "Trigger server-side wiki git sync (fast-forward pull; includes read)",
}

// AllScopes is the canonical vocabulary surfaced to clients. Keep it
// in the order you want users to see in the SPA (most common first).
var AllScopes = []string{ScopeWikiRead, ScopePapersRead, ScopePapersWrite, ScopeGraphRead, ScopeWikiWrite}

// casbinModel is the in-memory casbin model. Each scope acts as its
// own subject — the matcher just checks (scope, obj, act) equality
// against the policy table. Path-pattern matching can be added later
// by swapping `==` for `keyMatch(...)`.
const casbinModel = `
[request_definition]
r = scope, obj, act

[policy_definition]
p = scope, obj, act

[policy_effect]
e = some(where (p.eft == allow))

[matchers]
m = r.scope == p.scope && r.obj == p.obj && r.act == p.act
`

// scopePolicies is the authoritative authorization table. The casbin
// enforcer is seeded from this slice at startup; nothing else mutates
// the policy set. Encode "write implies read" by adding two rows for
// the write scope.
var scopePolicies = [][3]string{
	{ScopeWikiRead, "wiki", "read"},
	{ScopePapersRead, "papers", "read"},
	{ScopePapersWrite, "papers", "read"}, // write implies read
	{ScopePapersWrite, "papers", "write"},
	{ScopeGraphRead, "graph", "read"},
	{ScopeWikiWrite, "wiki", "read"}, // write implies read
	{ScopeWikiWrite, "wiki", "write"},
}

// NewEnforcer constructs a fresh in-memory casbin enforcer pre-loaded
// with scopePolicies. Call once at server startup and reuse the
// returned enforcer across requests — it is safe for concurrent
// Enforce() calls without explicit synchronisation (the underlying
// model is read-only after NewEnforcer returns since we never call
// AddPolicy / RemovePolicy at runtime).
func NewEnforcer() (*casbin.Enforcer, error) {
	m, err := model.NewModelFromString(casbinModel)
	if err != nil {
		return nil, fmt.Errorf("pat: parse casbin model: %w", err)
	}
	e, err := casbin.NewEnforcer(m)
	if err != nil {
		return nil, fmt.Errorf("pat: build enforcer: %w", err)
	}
	// Disable automatic save — there is no adapter, but the API still
	// asks. This is purely defensive.
	e.EnableAutoSave(false)

	for _, p := range scopePolicies {
		if _, err := e.AddPolicy(p[0], p[1], p[2]); err != nil {
			return nil, fmt.Errorf("pat: seed policy %v: %w", p, err)
		}
	}
	return e, nil
}

// Allows returns true when any of the supplied held scopes covers the
// requested (obj, act) pair. The wildcard ScopeMaster short-circuits
// to true before the enforcer is consulted.
//
// Held is typically the scopes column of a PAT record (already
// JSON-decoded into a string slice). Pass nil / empty to mean "this
// token holds nothing" — which always denies.
func Allows(enforcer *casbin.Enforcer, held []string, obj, act string) (bool, error) {
	if enforcer == nil {
		return false, errors.New("pat: nil enforcer")
	}
	for _, s := range held {
		if s == ScopeMaster {
			return true, nil
		}
	}
	for _, s := range held {
		ok, err := enforcer.Enforce(s, obj, act)
		if err != nil {
			return false, fmt.Errorf("pat: enforce %s on (%s, %s): %w", s, obj, act, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// ValidateScopes verifies every entry is a known scope name from
// AllScopes. The wildcard ScopeMaster is rejected here so external
// callers (REST API) can never grant themselves master access via
// JSON input — it is settable only by the routes layer when it
// detects a session-token caller.
//
// Returns nil for an empty slice (a deliberately-no-permissions PAT
// is a valid intermediate state; the create handler may refuse it
// separately if we want to require at least one scope).
func ValidateScopes(scopes []string) error {
	return validateScopes(scopes, false)
}

// ValidateScopesIncludingMaster is the operator-trusted variant of
// ValidateScopes: it accepts ScopeMaster ("*") as a valid entry, in
// addition to everything in AllScopes. Used for credentials loaded
// from the server's own environment (system PAT) — those are not
// "user input" in the same sense as JSON posted to /api/pat; the
// operator who set the env var already controls the box and could
// just write to the DB directly.
func ValidateScopesIncludingMaster(scopes []string) error {
	return validateScopes(scopes, true)
}

func validateScopes(scopes []string, allowMaster bool) error {
	known := make(map[string]struct{}, len(AllScopes))
	for _, s := range AllScopes {
		known[s] = struct{}{}
	}
	for _, s := range scopes {
		if s == ScopeMaster {
			if allowMaster {
				continue
			}
			return errors.New("pat: wildcard scope is not allowed in user input")
		}
		if _, ok := known[s]; !ok {
			return fmt.Errorf("pat: unknown scope %q", s)
		}
	}
	return nil
}
