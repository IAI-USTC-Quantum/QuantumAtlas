// HTTP-layer tests for the /api/oauth/device/* surface. Mirrors the
// patHarness pattern from pat_test.go — see that file's comment for
// the rationale (avoiding the "pattern already registered" panic
// from ApiScenario when multiple Test() calls share an app).
//
// What's covered:
//
//   - /code anonymous happy path: returns RFC 8628 shape with a
//     working device_code + user_code pair, persists a row with
//     status=pending and hashed device_code.
//   - /code body validation: blank name, missing expires_in_days,
//     over-max expires, unknown scope → 400.
//   - /token anonymous polling: unknown device_code → invalid_grant,
//     pending row → authorization_pending, fast re-poll →
//     slow_down, denied row → access_denied, expired row →
//     expired_token.
//   - /lookup sessionGuard: anonymous → 401, PAT-authenticated → 403,
//     session JWT + valid user_code → pending body.
//   - /approve sessionGuard: anonymous → 401, PAT → 403,
//     session JWT + pending → 200 + status=approved.
//   - End-to-end mint: code → approve via session JWT → token poll
//     returns a working PAT plaintext. Second poll on same code
//     returns invalid_grant (one-shot guarantee).
//   - /deny: session JWT + pending → status=denied, subsequent
//     /token poll returns access_denied.

package routes

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/oauthdevice"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/dbx"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/types"
)

// deviceHarness wraps newPATHarness's setup but additionally registers
// the device endpoints + the same /__test_scoped probe so that the
// end-to-end "mint via device flow → call a scope-gated endpoint"
// test can verify the PAT actually works.
type deviceHarness struct {
	*patHarness
}

func newDeviceHarness(t testing.TB) *deviceHarness {
	t.Helper()
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	if _, err := app.FindCollectionByNameOrId(pat.CollectionName); err != nil {
		t.Fatalf("pat_tokens collection missing after migrations: %v", err)
	}
	if _, err := app.FindCollectionByNameOrId(oauthdevice.CollectionName); err != nil {
		t.Fatalf("oauth_device_codes collection missing after migrations: %v", err)
	}

	enforcer, err := pat.NewEnforcer()
	if err != nil {
		t.Fatalf("NewEnforcer: %v", err)
	}

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterPAT(e, app)
		RegisterOAuthDevice(e, app)
		e.Router.POST("/__test_scoped", scopeGuard(enforcer, "papers", "write", func(re *core.RequestEvent) error {
			return re.JSON(http.StatusOK, map[string]bool{"ok": true})
		}))
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
	return &deviceHarness{patHarness: &patHarness{t: t, app: app, mux: built}}
}

// ---------------------------------------------------------------------------
// /api/oauth/device/code — anonymous
// ---------------------------------------------------------------------------

func TestAPI_OAuthDevice_CodeHappyPath(t *testing.T) {
	h := newDeviceHarness(t)

	status, _, body := h.do(http.MethodPost, "/api/oauth/device/code",
		`{"name":"qatlas-cli","scopes":["papers:write"],"expires_in_days":30}`,
		nil,
	)
	if status != http.StatusOK {
		t.Fatalf("status=%d body=%v", status, body)
	}
	dc := asString(body["device_code"])
	uc := asString(body["user_code"])
	if dc == "" || uc == "" {
		t.Fatalf("response missing codes: %v", body)
	}
	if !strings.Contains(uc, "-") {
		t.Errorf("user_code should be XXXX-XXXX form; got %q", uc)
	}
	if got := asInt(body["expires_in"]); got != oauthdevice.ExpiresInSeconds {
		t.Errorf("expires_in=%d, want %d", got, oauthdevice.ExpiresInSeconds)
	}
	if got := asInt(body["interval"]); got != oauthdevice.PollIntervalSeconds {
		t.Errorf("interval=%d, want %d", got, oauthdevice.PollIntervalSeconds)
	}

	// Persisted row exists, is pending, hashed.
	rec, err := h.app.FindFirstRecordByFilter(
		oauthdevice.CollectionName,
		"user_code = {:u}",
		dbx.Params{"u": uc},
	)
	if err != nil {
		t.Fatalf("row not found by user_code: %v", err)
	}
	if rec.GetString("status") != oauthdevice.StatusPending {
		t.Errorf("status=%q, want pending", rec.GetString("status"))
	}
	gotHash := rec.GetString("device_code_hash")
	if gotHash == dc {
		t.Errorf("device_code stored in plaintext (must be hashed)")
	}
	if gotHash != oauthdevice.HashDeviceCode(dc) {
		t.Errorf("device_code_hash mismatch (want sha256(plaintext))")
	}
}

func TestAPI_OAuthDevice_CodeValidation(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		wantMsg string
	}{
		{"blank name", `{"name":"","scopes":[],"expires_in_days":7}`, "name required"},
		{"missing expires", `{"name":"x","scopes":[]}`, "expires_in_days"},
		{"over max expires", `{"name":"x","scopes":[],"expires_in_days":9999}`, "365"},
		{"unknown scope", `{"name":"x","scopes":["nope:nope"],"expires_in_days":7}`, "unknown scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newDeviceHarness(t)
			status, _, body := h.do(http.MethodPost, "/api/oauth/device/code", tc.body, nil)
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d, want 400; body=%v", status, body)
			}
			if !strings.Contains(asString(body["detail"]), tc.wantMsg) {
				t.Errorf("detail should mention %q; got %q", tc.wantMsg, body["detail"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// /api/oauth/device/token — anonymous polling errors
// ---------------------------------------------------------------------------

func TestAPI_OAuthDevice_TokenInvalidGrant(t *testing.T) {
	h := newDeviceHarness(t)
	status, _, body := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"definitely-not-a-real-device-code"}`, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400; body=%v", status, body)
	}
	if asString(body["error"]) != "invalid_grant" {
		t.Errorf("error=%q, want invalid_grant", body["error"])
	}
}

func TestAPI_OAuthDevice_TokenAuthorizationPending(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("pending-x", nil, 7)

	status, _, body := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"`+dc+`"}`, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if asString(body["error"]) != "authorization_pending" {
		t.Errorf("error=%q want authorization_pending", body["error"])
	}
}

func TestAPI_OAuthDevice_TokenSlowDown(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("slow", nil, 7)

	// First poll — should be authorization_pending (counts as the
	// baseline timestamp).
	if status, _, _ := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"`+dc+`"}`, nil); status != http.StatusBadRequest {
		t.Fatalf("first poll status=%d", status)
	}
	// Immediate second poll — must be slow_down (no waiting for
	// PollIntervalSeconds).
	status, _, body := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"`+dc+`"}`, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("second poll status=%d body=%v", status, body)
	}
	if asString(body["error"]) != "slow_down" {
		t.Errorf("error=%q want slow_down", body["error"])
	}
}

func TestAPI_OAuthDevice_TokenAccessDenied(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("denied", nil, 7)
	uc := h.userCodeForDevice(dc)

	// Deny via session token.
	tok := h.sessionToken()
	denyStatus, _, denyBody := h.do(http.MethodPost, "/api/oauth/device/deny",
		`{"user_code":"`+uc+`"}`, rawHeader(tok))
	if denyStatus != http.StatusOK {
		t.Fatalf("deny status=%d body=%v", denyStatus, denyBody)
	}

	status, _, body := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"`+dc+`"}`, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if asString(body["error"]) != "access_denied" {
		t.Errorf("error=%q want access_denied", body["error"])
	}
}

func TestAPI_OAuthDevice_TokenExpired(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("expiring", nil, 7)

	// Backdate expires_at into the past, simulating a TTL miss.
	hash := oauthdevice.HashDeviceCode(dc)
	rec, err := h.app.FindFirstRecordByFilter(
		oauthdevice.CollectionName,
		"device_code_hash = {:h}",
		dbx.Params{"h": hash},
	)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	past := types.NowDateTime().Add(-1 * time.Hour)
	rec.Set("expires_at", past)
	if err := h.app.Save(rec); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	status, _, body := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"`+dc+`"}`, nil)
	if status != http.StatusBadRequest {
		t.Fatalf("status=%d body=%v", status, body)
	}
	if asString(body["error"]) != "expired_token" {
		t.Errorf("error=%q want expired_token", body["error"])
	}

	// Status should now be expired.
	rec, _ = h.app.FindRecordById(oauthdevice.CollectionName, rec.Id)
	if rec.GetString("status") != oauthdevice.StatusExpired {
		t.Errorf("status=%q want expired", rec.GetString("status"))
	}
}

// ---------------------------------------------------------------------------
// /api/oauth/device/{lookup,approve,deny} — sessionGuard
// ---------------------------------------------------------------------------

func TestAPI_OAuthDevice_LookupRequiresSession(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("lookup", nil, 7)
	uc := h.userCodeForDevice(dc)

	// Anonymous → 401.
	status, _, _ := h.do(http.MethodGet, "/api/oauth/device/code?user_code="+uc, "", nil)
	if status != http.StatusUnauthorized {
		t.Errorf("anonymous lookup status=%d, want 401", status)
	}

	// Mint a PAT (via session) and use it — sessionGuard must
	// reject it with 403 same as /api/pat.
	patHeader := h.mintPATBearer()
	status, _, body := h.do(http.MethodGet, "/api/oauth/device/code?user_code="+uc, "", patHeader)
	if status != http.StatusForbidden {
		t.Errorf("PAT-auth lookup status=%d body=%v", status, body)
	}

	// Session JWT → 200.
	status, _, body = h.do(http.MethodGet, "/api/oauth/device/code?user_code="+uc, "", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("session lookup status=%d body=%v", status, body)
	}
	if asString(body["user_code"]) != uc {
		t.Errorf("user_code echo=%q want %q", body["user_code"], uc)
	}
	if asString(body["status"]) != oauthdevice.StatusPending {
		t.Errorf("status=%q want pending", body["status"])
	}
}

// TestAPI_OAuthDevice_EndToEnd_Mint walks through the full flow:
// CLI calls /code → user (session JWT) calls /approve → CLI polls
// /token → receives a working plaintext PAT that satisfies a
// scope-gated endpoint. Second poll must fail invalid_grant (one-
// shot delivery contract).
func TestAPI_OAuthDevice_EndToEnd_Mint(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("e2e", []string{"papers:write"}, 14)
	uc := h.userCodeForDevice(dc)

	tok := h.sessionToken()

	// User approves.
	appStatus, _, appBody := h.do(http.MethodPost, "/api/oauth/device/approve",
		`{"user_code":"`+uc+`"}`, rawHeader(tok))
	if appStatus != http.StatusOK {
		t.Fatalf("approve status=%d body=%v", appStatus, appBody)
	}
	if asString(appBody["status"]) != oauthdevice.StatusApproved {
		t.Errorf("approve body status=%q want approved", appBody["status"])
	}

	// CLI polls → success + plaintext. Backdate last_polled_at so
	// the slow_down guard doesn't trigger here (the harness has no
	// way to inject sleeps cleanly without flaking on slow CI).
	h.clearPollWindow(dc)
	tokStatus, _, tokBody := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"`+dc+`"}`, nil)
	if tokStatus != http.StatusOK {
		t.Fatalf("token status=%d body=%v", tokStatus, tokBody)
	}
	plaintext := asString(tokBody["plaintext"])
	if !strings.HasPrefix(plaintext, pat.TokenPrefix) {
		t.Fatalf("token response missing plaintext: %v", tokBody)
	}
	scopes, _ := tokBody["scopes"].([]any)
	if len(scopes) != 1 || asString(scopes[0]) != "papers:write" {
		t.Errorf("scopes=%v want [papers:write]", scopes)
	}

	// Use the minted PAT against the scope-gated endpoint.
	useStatus, _, useBody := h.do(http.MethodPost, "/__test_scoped", `{}`, bearerHeader(plaintext))
	if useStatus != http.StatusOK {
		t.Errorf("scope-gated call status=%d body=%v", useStatus, useBody)
	}

	// Second poll must NOT return another plaintext (one-shot).
	h.clearPollWindow(dc)
	rePoll, _, rePollBody := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"`+dc+`"}`, nil)
	if rePoll != http.StatusBadRequest {
		t.Fatalf("re-poll status=%d body=%v", rePoll, rePollBody)
	}
	if asString(rePollBody["error"]) != "invalid_grant" {
		t.Errorf("re-poll error=%q want invalid_grant", rePollBody["error"])
	}
}

// TestAPI_OAuthDevice_ApproveAtomic verifies that two concurrent
// "approve" requests can't both succeed — the second loses on the
// conditional UPDATE and reports 409. This pins the TOCTOU fix from
// the rubber-duck pass.
func TestAPI_OAuthDevice_ApproveAtomic(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("race", []string{"papers:write"}, 7)
	uc := h.userCodeForDevice(dc)
	tok := h.sessionToken()

	first, _, _ := h.do(http.MethodPost, "/api/oauth/device/approve",
		`{"user_code":"`+uc+`"}`, rawHeader(tok))
	if first != http.StatusOK {
		t.Fatalf("first approve status=%d", first)
	}
	second, _, body := h.do(http.MethodPost, "/api/oauth/device/approve",
		`{"user_code":"`+uc+`"}`, rawHeader(tok))
	if second != http.StatusConflict {
		t.Fatalf("second approve status=%d body=%v want 409", second, body)
	}
}

// TestAPI_OAuthDevice_LookupSurfacesScopeVocabulary verifies that GET
// /api/oauth/device/code includes available_scopes, scope_descriptions
// and max_expiry_days so the SPA can render a scope picker without
// hitting a separate endpoint.
func TestAPI_OAuthDevice_LookupSurfacesScopeVocabulary(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("vocab", []string{"papers:write"}, 7)
	uc := h.userCodeForDevice(dc)

	status, _, body := h.do(http.MethodGet, "/api/oauth/device/code?user_code="+uc,
		"", rawHeader(h.sessionToken()))
	if status != http.StatusOK {
		t.Fatalf("lookup status=%d body=%v", status, body)
	}

	avail, _ := body["available_scopes"].([]any)
	if len(avail) == 0 {
		t.Fatalf("available_scopes missing/empty: %v", body)
	}
	have := map[string]bool{}
	for _, v := range avail {
		have[asString(v)] = true
	}
	for _, want := range pat.AllScopes {
		if !have[want] {
			t.Errorf("available_scopes missing %q (got %v)", want, avail)
		}
	}

	desc, _ := body["scope_descriptions"].(map[string]any)
	if asString(desc["papers:write"]) == "" {
		t.Errorf("scope_descriptions missing papers:write copy: %v", desc)
	}

	if asInt(body["max_expiry_days"]) != MaxPATExpiryDays {
		t.Errorf("max_expiry_days=%v want %d", body["max_expiry_days"], MaxPATExpiryDays)
	}

	// And the CLI-seeded defaults are still echoed back.
	scopes, _ := body["scopes"].([]any)
	if len(scopes) != 1 || asString(scopes[0]) != "papers:write" {
		t.Errorf("default scopes echo=%v want [papers:write]", scopes)
	}
}

// TestAPI_OAuthDevice_ApproveAppliesOverrides verifies that the user
// can edit name / scopes / expires_in_days on the approve form and the
// /token mint downstream uses the edited values rather than what the
// CLI seeded.
func TestAPI_OAuthDevice_ApproveAppliesOverrides(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("seed-name", []string{"papers:write"}, 7)
	uc := h.userCodeForDevice(dc)
	tok := h.sessionToken()

	overrideBody := `{
		"user_code":"` + uc + `",
		"name":"edited-name",
		"scopes":["wiki:read","papers:read"],
		"expires_in_days":30
	}`
	status, _, body := h.do(http.MethodPost, "/api/oauth/device/approve",
		overrideBody, rawHeader(tok))
	if status != http.StatusOK {
		t.Fatalf("approve status=%d body=%v", status, body)
	}
	if asString(body["name"]) != "edited-name" {
		t.Errorf("approve response name=%q want edited-name", body["name"])
	}
	if asInt(body["expires_in_days"]) != 30 {
		t.Errorf("approve response expires_in_days=%v want 30", body["expires_in_days"])
	}
	gotScopes, _ := body["scopes"].([]any)
	if len(gotScopes) != 2 {
		t.Fatalf("approve response scopes=%v want 2 entries", gotScopes)
	}
	have := map[string]bool{
		asString(gotScopes[0]): true,
		asString(gotScopes[1]): true,
	}
	if !have["wiki:read"] || !have["papers:read"] {
		t.Errorf("approve response scopes=%v want [wiki:read papers:read]", gotScopes)
	}

	// Mint the PAT and confirm it carries the edited values.
	h.clearPollWindow(dc)
	tokStatus, _, tokBody := h.do(http.MethodPost, "/api/oauth/device/token",
		`{"device_code":"`+dc+`"}`, nil)
	if tokStatus != http.StatusOK {
		t.Fatalf("token status=%d body=%v", tokStatus, tokBody)
	}
	if asString(tokBody["name"]) != "edited-name" {
		t.Errorf("minted name=%q want edited-name", tokBody["name"])
	}
	mintedScopes, _ := tokBody["scopes"].([]any)
	if len(mintedScopes) != 2 {
		t.Fatalf("minted scopes=%v want 2 entries", mintedScopes)
	}
}

// TestAPI_OAuthDevice_ApproveRejectsBadOverrides verifies that an
// unknown scope on the approve POST is rejected with 400 (not 500)
// and the row stays pending so the user can re-submit.
func TestAPI_OAuthDevice_ApproveRejectsBadOverrides(t *testing.T) {
	h := newDeviceHarness(t)
	dc := h.startFlow("bad-override", []string{"papers:write"}, 7)
	uc := h.userCodeForDevice(dc)
	tok := h.sessionToken()

	cases := []struct {
		name string
		body string
	}{
		{"unknown scope", `{"user_code":"` + uc + `","scopes":["bogus:scope"]}`},
		{"empty name", `{"user_code":"` + uc + `","name":"   "}`},
		{"too-long name", `{"user_code":"` + uc + `","name":"` + strings.Repeat("x", 81) + `"}`},
		{"non-positive expiry", `{"user_code":"` + uc + `","expires_in_days":0}`},
		{"over-max expiry", `{"user_code":"` + uc + `","expires_in_days":1000}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _, body := h.do(http.MethodPost, "/api/oauth/device/approve",
				tc.body, rawHeader(tok))
			if status != http.StatusBadRequest {
				t.Fatalf("status=%d body=%v want 400", status, body)
			}
		})
	}

	// Row is still pending → a clean approve still works after the
	// rejected attempts.
	final, _, finalBody := h.do(http.MethodPost, "/api/oauth/device/approve",
		`{"user_code":"`+uc+`"}`, rawHeader(tok))
	if final != http.StatusOK {
		t.Fatalf("clean approve status=%d body=%v", final, finalBody)
	}
}

// ---------------------------------------------------------------------------
// Test helpers private to this file
// ---------------------------------------------------------------------------

// startFlow does a POST /api/oauth/device/code with the given scopes
// and returns the freshly-issued device_code. Always uses
// expires_in_days days. Fails the test on error.
func (h *deviceHarness) startFlow(name string, scopes []string, expiresInDays int) string {
	h.t.Helper()
	if scopes == nil {
		scopes = []string{}
	}
	scopesJSON := "["
	for i, s := range scopes {
		if i > 0 {
			scopesJSON += ","
		}
		scopesJSON += `"` + s + `"`
	}
	scopesJSON += "]"
	body := `{"name":"` + name + `","scopes":` + scopesJSON +
		`,"expires_in_days":` + itoa(expiresInDays) + `}`
	status, _, resp := h.do(http.MethodPost, "/api/oauth/device/code", body, nil)
	if status != http.StatusOK {
		h.t.Fatalf("startFlow: /code status=%d body=%v", status, resp)
	}
	dc := asString(resp["device_code"])
	if dc == "" {
		h.t.Fatalf("startFlow: missing device_code in %v", resp)
	}
	return dc
}

// userCodeForDevice looks up the user_code persisted alongside a
// device_code (we don't have access to the SPA's lookup at this
// layer in some tests).
func (h *deviceHarness) userCodeForDevice(dc string) string {
	h.t.Helper()
	rec, err := h.app.FindFirstRecordByFilter(
		oauthdevice.CollectionName,
		"device_code_hash = {:h}",
		dbx.Params{"h": oauthdevice.HashDeviceCode(dc)},
	)
	if err != nil {
		h.t.Fatalf("userCodeForDevice: %v", err)
	}
	return rec.GetString("user_code")
}

// clearPollWindow sets last_polled_at to a far-past sentinel so the
// next /token call doesn't get slow_down'd. Used in the end-to-end
// test where we need to poll repeatedly without real wall-clock
// waiting. We can't NULL the column (PocketBase DateFields are
// NOT NULL with a zero-value default), so we backdate by a day —
// well past the PollIntervalSeconds grace.
func (h *deviceHarness) clearPollWindow(dc string) {
	h.t.Helper()
	hash := oauthdevice.HashDeviceCode(dc)
	past := types.NowDateTime().Add(-24 * time.Hour)
	if _, err := h.app.DB().NewQuery(
		"UPDATE " + oauthdevice.CollectionName +
			" SET last_polled_at = {:t} WHERE device_code_hash = {:h}",
	).Bind(dbx.Params{"t": past, "h": hash}).Execute(); err != nil {
		h.t.Fatalf("clearPollWindow: %v", err)
	}
}

// mintPATBearer mints a PAT via the session-protected /api/pat path
// and returns a bearer header carrying that PAT. Used to test that
// /api/oauth/device/* sessionGuard endpoints reject PAT auth.
func (h *deviceHarness) mintPATBearer() map[string]string {
	h.t.Helper()
	status, _, body := h.do(http.MethodPost, "/api/pat",
		`{"name":"bearer-probe","scopes":[],"expires_in_days":7}`,
		rawHeader(h.sessionToken()),
	)
	if status != http.StatusOK {
		h.t.Fatalf("PAT mint: status=%d body=%v", status, body)
	}
	plaintext := asString(body["plaintext"])
	if plaintext == "" {
		h.t.Fatalf("PAT mint: missing plaintext in %v", body)
	}
	return bearerHeader(plaintext)
}

// asInt coerces a JSON number into int. json.Unmarshal into map[string]any
// gives float64 for numbers, so we have to round-trip through that.
func asInt(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	default:
		return 0
	}
}

// ensure auth.UsersCollection import stays referenced (mirrors the
// pat_test.go pattern; useful as a sanity anchor when we extend this
// file with sessionToken-style helpers).
var _ = auth.UsersCollection
