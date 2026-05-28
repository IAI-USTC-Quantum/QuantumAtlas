// HTTP-layer tests for the /api/pat surface and the auth gates that
// protect it. These complement (and partially replace) the live-server
// scenarios in tests/integration/test_production_smoke.py — they prove
// the same contracts but run offline in CI on every push, so the
// nightly e2e suite can stay focused on "production reachability"
// rather than "PAT contracts as such".
//
// What's exercised here:
//
//   * authGuard: anonymous + bogus bearer → 401
//   * sessionGuard: PAT-authenticated caller is REJECTED from
//     /api/pat (the headline "leaked PAT can't mint more PATs"
//     contract that drives the whole sessionGuard design)
//   * patCreateHandler validation: missing/over-max expires_in_days,
//     unknown scope, blank name — all 400
//   * Full lifecycle: mint via session JWT → use plaintext against a
//     scope-gated endpoint (200) → revoke via session JWT → reuse
//     plaintext (401)
//   * Scope enforcement: a scope-less PAT hitting a scope-gated
//     endpoint → 403, with the missing scope echoed in the body
//
// Why we register a private /__test_scoped endpoint instead of
// reusing /api/shares/: the production shares handler depends on
// ShareStore + RawStore + config, none of which are interesting for
// proving the auth+scope path. A 4-line passthrough endpoint with
// scopeGuard wrapped around it is more self-contained.

package routes

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/router"
)

// patHarness is a lightweight wrapper around tests.NewTestApp that
// builds the request mux exactly once, then exposes a do() helper
// that runs as many requests as needed against the same mux. This
// avoids the "pattern already registered" panic you get from
// reusing tests.ApiScenario across multiple Test() calls on the same
// app (every Test() rebuilds the mux from the Router's accumulated
// route table, so two ApiScenarios on one app blow up on the second
// route registration).
//
// Internally the harness mirrors the small slice of ApiScenario's
// internals (fire OnServe, populate ServeEvent, BuildMux once, retain
// the result) so it stays correct even when PocketBase's request
// pipeline grows new hooks.
type patHarness struct {
	t     testing.TB
	app   *tests.TestApp
	mux   http.Handler
}

func newPATHarness(t testing.TB) *patHarness {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	if _, err := app.FindCollectionByNameOrId(pat.CollectionName); err != nil {
		t.Fatalf("pat_tokens collection missing after migrations: %v", err)
	}

	enforcer, err := pat.NewEnforcer()
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	// Build a serve event ourselves and trigger OnServe so the
	// PocketBase-internal middleware stack is layered on. This is
	// the same shape ApiScenario assembles internally — see
	// /tools/router + tests/api.go.
	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		// Routes under test.
		RegisterPAT(e, app)
		e.Router.POST("/__test_scoped", scopeGuard(enforcer, "shares", "write", func(re *core.RequestEvent) error {
			return re.JSON(http.StatusOK, map[string]bool{"ok": true})
		}))

		// Long-test logger middleware (matches ApiScenario for parity).
		e.Router.Bind(&hook.Handler[*core.RequestEvent]{
			Func:     func(re *core.RequestEvent) error { return re.Next() },
			Priority: -9999,
		})

		m, mErr := e.Router.BuildMux()
		if mErr != nil {
			return mErr
		}
		built = m
		return nil
	})
	if err != nil {
		t.Fatalf("OnServe trigger: %v", err)
	}
	if built == nil {
		t.Fatal("mux not built by OnServe trigger")
	}
	// Silence the unused-import warning if router gets dropped.
	_ = router.Router[*core.RequestEvent]{}

	return &patHarness{t: t, app: app, mux: built}
}

// do issues a single request through the cached mux and returns the
// (status, body) pair, body decoded as best-effort JSON. headers may be
// nil; "Content-Type: application/json" is set automatically when body
// is non-empty.
func (h *patHarness) do(method, url, body string, headers map[string]string) (int, []byte, map[string]any) {
	h.t.Helper()
	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, url, bodyReader)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.mux.ServeHTTP(rec, req)

	raw, _ := io.ReadAll(rec.Result().Body)
	rec.Result().Body.Close()
	var decoded map[string]any
	_ = json.Unmarshal(raw, &decoded) // OK to swallow — caller checks rec.Code first.
	return rec.Code, raw, decoded
}

// sessionToken returns a freshly-issued PocketBase auth token for the
// seeded test@example.com user. Convenience helper for tests that
// need a "session-flavoured" Bearer to drive /api/pat as a logged-in
// browser would.
func (h *patHarness) sessionToken() string {
	h.t.Helper()
	user, err := h.app.FindAuthRecordByEmail(auth.UsersCollection, "test@example.com")
	if err != nil {
		h.t.Fatalf("seed user lookup: %v", err)
	}
	token, err := user.NewAuthToken()
	if err != nil {
		h.t.Fatalf("NewAuthToken: %v", err)
	}
	return token
}

// bearerHeader is the canonical Authorization map for a Bearer token.
func bearerHeader(tok string) map[string]string {
	return map[string]string{"Authorization": "Bearer " + tok}
}

// rawHeader bypasses the "Bearer " prefix so we can test the PocketBase
// case where the JWT is supplied without the "Bearer " scheme (which
// the upstream middleware still accepts).
func rawHeader(tok string) map[string]string {
	return map[string]string{"Authorization": tok}
}

// ---------------------------------------------------------------------------
// authGuard / sessionGuard
// ---------------------------------------------------------------------------

func TestAPI_PAT_RejectsAnonymous(t *testing.T) {
	h := newPATHarness(t)
	status, _, body := h.do(http.MethodPost, "/api/pat",
		`{"name":"x","scopes":[],"expires_in_days":30}`, nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", status)
	}
	if !strings.Contains(asString(body["detail"]), "authentication required") {
		t.Errorf("detail %q should mention 'authentication required'", body["detail"])
	}
}

func TestAPI_PAT_RejectsBogusBearer(t *testing.T) {
	h := newPATHarness(t)
	status, _, body := h.do(http.MethodPost, "/api/pat",
		`{"name":"x","scopes":[],"expires_in_days":30}`,
		bearerHeader("not-a-real-token-zzz"),
	)
	if status != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401, body=%v", status, body)
	}
}

// TestAPI_PAT_SessionGuardRejectsPATAuth is the headline contract:
// a leaked PAT must NOT be able to mint or revoke other PATs, even
// if it carries every scope. Reject path returns 403 (NOT 401)
// because the caller IS authenticated — it just isn't allowed to
// hit /api/pat regardless of scope. This is the safety net that
// makes PAT-as-credential safe to ship in CI secrets.
func TestAPI_PAT_SessionGuardRejectsPATAuth(t *testing.T) {
	h := newPATHarness(t)

	// Bootstrap: mint a PAT through the session-token path so we
	// have a real qat_... plaintext to attack with.
	bootstrapStatus, _, mintBody := h.do(http.MethodPost, "/api/pat",
		`{"name":"bootstrap","scopes":["shares:write"],"expires_in_days":30}`,
		rawHeader(h.sessionToken()),
	)
	if bootstrapStatus != http.StatusOK {
		t.Fatalf("bootstrap mint failed: status=%d body=%v", bootstrapStatus, mintBody)
	}
	plaintext := asString(mintBody["plaintext"])
	if !strings.HasPrefix(plaintext, pat.TokenPrefix) {
		t.Fatalf("bootstrap mint returned no plaintext; body=%v", mintBody)
	}

	// Now try to mint a SECOND PAT using the first PAT as auth.
	// MUST get 403 sessionGuard, not 200 happy path, not 401 auth fail.
	status, _, body := h.do(http.MethodPost, "/api/pat",
		`{"name":"should-not-exist","scopes":[],"expires_in_days":7}`,
		bearerHeader(plaintext),
	)
	if status != http.StatusForbidden {
		t.Errorf("status = %d, want 403; body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "browser session token") {
		t.Errorf("detail should mention 'browser session token'; got %q", body["detail"])
	}

	// Sanity: the bogus second PAT must NOT have been persisted.
	records, _ := h.app.FindAllRecords(pat.CollectionName)
	if len(records) != 1 {
		t.Errorf("expected exactly 1 PAT record (bootstrap only), got %d", len(records))
	}
}

// ---------------------------------------------------------------------------
// patCreateHandler body validation
// ---------------------------------------------------------------------------

func TestAPI_PAT_CreateRequiresExpiry(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"missing expires_in_days", `{"name":"x","scopes":[]}`, "expires_in_days"},
		{"zero expires_in_days", `{"name":"x","scopes":[],"expires_in_days":0}`, "expires_in_days"},
		{"negative expires_in_days", `{"name":"x","scopes":[],"expires_in_days":-1}`, "expires_in_days"},
		{"over max expires_in_days", `{"name":"x","scopes":[],"expires_in_days":9999}`, "365"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newPATHarness(t)
			tok := h.sessionToken()

			status, _, body := h.do(http.MethodPost, "/api/pat", tc.body, rawHeader(tok))
			if status != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400; body=%v", status, body)
			}
			if !strings.Contains(asString(body["detail"]), tc.wantMsg) {
				t.Errorf("detail should mention %q; got %q", tc.wantMsg, body["detail"])
			}

			records, _ := h.app.FindAllRecords(pat.CollectionName)
			if len(records) != 0 {
				t.Errorf("rejection path persisted %d records", len(records))
			}
		})
	}
}

func TestAPI_PAT_CreateRejectsUnknownScope(t *testing.T) {
	h := newPATHarness(t)
	tok := h.sessionToken()

	status, _, body := h.do(http.MethodPost, "/api/pat",
		`{"name":"x","scopes":["definitely:nope"],"expires_in_days":30}`,
		rawHeader(tok),
	)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "unknown scope") {
		t.Errorf("detail should mention 'unknown scope'; got %q", body["detail"])
	}
}

func TestAPI_PAT_CreateRejectsBlankName(t *testing.T) {
	h := newPATHarness(t)
	tok := h.sessionToken()

	status, _, body := h.do(http.MethodPost, "/api/pat",
		`{"name":"","scopes":["shares:write"],"expires_in_days":30}`,
		rawHeader(tok),
	)
	if status != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%v", status, body)
	}
	if !strings.Contains(asString(body["detail"]), "name required") {
		t.Errorf("detail should mention 'name required'; got %q", body["detail"])
	}
}

// ---------------------------------------------------------------------------
// Full lifecycle: mint → use → revoke → reuse must fail
// ---------------------------------------------------------------------------

func TestAPI_PAT_LifecycleMintUseRevoke(t *testing.T) {
	h := newPATHarness(t)
	tok := h.sessionToken()

	// Step 1: mint with shares:write scope.
	status, _, mintBody := h.do(http.MethodPost, "/api/pat",
		`{"name":"lifecycle","scopes":["shares:write"],"expires_in_days":30}`,
		rawHeader(tok),
	)
	if status != http.StatusOK {
		t.Fatalf("mint status=%d body=%v", status, mintBody)
	}
	plaintext := asString(mintBody["plaintext"])
	id := asString(mintBody["id"])
	if plaintext == "" || id == "" {
		t.Fatalf("mint response missing plaintext/id: %v", mintBody)
	}

	// Step 2: use the PAT against the scope-gated test endpoint.
	useStatus, _, useBody := h.do(http.MethodPost, "/__test_scoped", `{}`, bearerHeader(plaintext))
	if useStatus != http.StatusOK {
		t.Errorf("PAT call status=%d, want 200; body=%v", useStatus, useBody)
	}

	// Step 3: revoke via session token.
	revStatus, _, revBody := h.do(http.MethodDelete, "/api/pat/"+id, "", rawHeader(tok))
	if revStatus != http.StatusOK {
		t.Fatalf("revoke status=%d body=%v", revStatus, revBody)
	}

	// Step 4: reusing the revoked PAT must now fail at authGuard (401).
	reuseStatus, _, reuseBody := h.do(http.MethodPost, "/__test_scoped", `{}`, bearerHeader(plaintext))
	if reuseStatus != http.StatusUnauthorized {
		t.Errorf("revoked PAT status=%d, want 401; body=%v", reuseStatus, reuseBody)
	}
}

// ---------------------------------------------------------------------------
// Scope enforcement
// ---------------------------------------------------------------------------

func TestAPI_PAT_ScopeEnforcement(t *testing.T) {
	h := newPATHarness(t)
	tok := h.sessionToken()

	// Mint a scope-less PAT (allowed at the API surface: see scope
	// picker UX in pat.tsx — a no-scope PAT is a valid placeholder).
	status, _, mintBody := h.do(http.MethodPost, "/api/pat",
		`{"name":"scopeless","scopes":[],"expires_in_days":30}`,
		rawHeader(tok),
	)
	if status != http.StatusOK {
		t.Fatalf("mint status=%d body=%v", status, mintBody)
	}
	plaintext := asString(mintBody["plaintext"])
	if plaintext == "" {
		t.Fatalf("mint missing plaintext: %v", mintBody)
	}

	// Scope-gated endpoint → 403, detail must name the missing scope
	// so an operator can fix the PAT without guessing.
	useStatus, _, useBody := h.do(http.MethodPost, "/__test_scoped", `{}`, bearerHeader(plaintext))
	if useStatus != http.StatusForbidden {
		t.Fatalf("scope-less PAT status=%d, want 403; body=%v", useStatus, useBody)
	}
	detail := asString(useBody["detail"])
	if !strings.Contains(detail, "shares") || !strings.Contains(detail, "write") {
		t.Errorf("403 detail should name (shares, write); got %q", detail)
	}
}

// asString coerces a json-decoded map value to its string form, or
// "" for nil / wrong type. Saves an "ok-check" boilerplate per
// assertion.
func asString(v any) string {
	s, _ := v.(string)
	return s
}

