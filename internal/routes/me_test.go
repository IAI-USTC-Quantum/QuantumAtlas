// HTTP-layer tests for the /api/me surface (user dashboard backend).
//
// What's exercised here:
//
//   - authGuard: anonymous → 401 on both endpoints
//   - sessionGuard: a user PAT is REJECTED (403, "browser session
//     token") — the dashboard surface is for browser sessions, same
//     contract as /api/pat and /api/admin/whoami
//   - /api/me: a session gets its own profile fields; github_login and
//     is_admin reflect the stamped login + the admin allowlist
//   - /api/me/usage: session + nil-pool usage store → 503 (the
//     registry-unavailable convention, same as the admin usage surface)
//
// The harness mirrors adminHarness (see admin_test.go): one mux built
// from OnServe, a do() helper for repeated requests.
package routes

import (
	"net/http"
	"strings"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/usage"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	"github.com/pocketbase/pocketbase/tools/types"
)

// meHarness is patHarness with the /api/me routes mounted behind a cfg
// that lists adminTestLogin (from admin_test.go) as the sole admin. The
// usage store gets a nil pool, so a session-cleared request exercises
// the 503 registry-unavailable path without needing a live Postgres.
type meHarness struct {
	*patHarness
	cfg *config.Config
}

func newMeHarness(t testing.TB) *meHarness {
	t.Helper()
	h := &meHarness{
		patHarness: &patHarness{t: t},
		cfg:        &config.Config{AdminGitHubLogins: []string{adminTestLogin}},
	}

	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)
	h.app = app

	baseRouter, err := apis.NewRouter(app)
	if err != nil {
		t.Fatalf("NewRouter: %v", err)
	}
	se := new(core.ServeEvent)
	se.App = app
	se.Router = baseRouter

	var built http.Handler
	err = app.OnServe().Trigger(se, func(e *core.ServeEvent) error {
		RegisterMe(e, h.cfg, usage.NewStore(nil))
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
	h.mux = built
	return h
}

// meSessionToken creates a users record with the given GitHub login
// stamped on github_login and returns a fresh session token for it —
// exactly the state the OAuth hook produces after sign-in.
func (h *meHarness) meSessionToken(login string) string {
	h.t.Helper()
	col, err := h.app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		h.t.Fatalf("find users collection: %v", err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail(login + "@example.com")
	rec.SetPassword("me-test-password")
	rec.Set(auth.GitHubLoginField, login)
	if err := h.app.Save(rec); err != nil {
		h.t.Fatalf("save user: %v", err)
	}
	token, err := rec.NewAuthToken()
	if err != nil {
		h.t.Fatalf("NewAuthToken: %v", err)
	}
	return token
}

// mintPAT mints a pat_tokens row directly (bypassing the session-gated
// /api/pat handler) bound to the record that owns the given session
// token, and returns the plaintext. Same construction as
// adminHarness.mintPATForAuth.
func (h *meHarness) mintPAT(sessionTok string) string {
	h.t.Helper()
	user, err := h.app.FindAuthRecordByToken(sessionTok, core.TokenTypeAuth)
	if err != nil {
		h.t.Fatalf("resolve session token owner: %v", err)
	}
	col, err := h.app.FindCollectionByNameOrId(pat.CollectionName)
	if err != nil {
		h.t.Fatalf("find pat_tokens collection: %v", err)
	}
	plaintext, prefix, hash, err := pat.Generate()
	if err != nil {
		h.t.Fatalf("pat.Generate: %v", err)
	}
	rec := core.NewRecord(col)
	rec.Set("user", user.Id)
	rec.Set("name", "me-pat")
	rec.Set("prefix", prefix)
	rec.Set("token_hash", hash)
	rec.Set("scopes", `["papers:read"]`)
	rec.Set("expires_at", types.NowDateTime().AddDate(0, 0, 30))
	if err := h.app.Save(rec); err != nil {
		h.t.Fatalf("save pat record: %v", err)
	}
	return plaintext
}

func TestAPI_Me_RejectsAnonymous(t *testing.T) {
	h := newMeHarness(t)
	for _, url := range []string{"/api/me", "/api/me/usage"} {
		status, _, body := h.do(http.MethodGet, url, "", nil)
		if status != http.StatusUnauthorized {
			t.Errorf("GET %s: status = %d, want 401; body=%v", url, status, body)
		}
	}
}

func TestAPI_Me_RejectsPATAuth(t *testing.T) {
	h := newMeHarness(t)
	plaintext := h.mintPAT(h.meSessionToken("someuser"))
	for _, url := range []string{"/api/me", "/api/me/usage"} {
		status, _, body := h.do(http.MethodGet, url, "", bearerHeader(plaintext))
		if status != http.StatusForbidden {
			t.Errorf("GET %s: status = %d, want 403; body=%v", url, status, body)
		}
		if !strings.Contains(asString(body["detail"]), "browser session token") {
			t.Errorf("GET %s: detail should mention 'browser session token'; got %q", url, body["detail"])
		}
	}
}

func TestAPI_Me_ProfileSession(t *testing.T) {
	h := newMeHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/me", "", rawHeader(h.meSessionToken("regularuser")))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	if got := asString(body["github_login"]); got != "regularuser" {
		t.Errorf("github_login = %q, want %q", got, "regularuser")
	}
	if got := asString(body["email"]); got != "regularuser@example.com" {
		t.Errorf("email = %q, want %q", got, "regularuser@example.com")
	}
	if body["is_admin"] != false {
		t.Errorf("is_admin = %v, want false", body["is_admin"])
	}
	if asString(body["id"]) == "" {
		t.Error("id is empty")
	}
	if asString(body["created"]) == "" {
		t.Error("created is empty")
	}
}

func TestAPI_Me_ProfileAdminSession(t *testing.T) {
	h := newMeHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/me", "", rawHeader(h.meSessionToken(adminTestLogin)))
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%v", status, body)
	}
	if body["is_admin"] != true {
		t.Errorf("is_admin = %v, want true (allowlisted login)", body)
	}
	if got := asString(body["github_login"]); got != adminTestLogin {
		t.Errorf("github_login = %q, want %q", got, adminTestLogin)
	}
}

// TestAPI_Me_UsageNilPool: a session clears the gate; with the harness's
// nil-pool usage store the endpoint reports the registry-unavailable
// 503 convention (same as the admin usage surface).
func TestAPI_Me_UsageNilPool(t *testing.T) {
	h := newMeHarness(t)
	status, _, body := h.do(http.MethodGet, "/api/me/usage", "", rawHeader(h.meSessionToken("someuser")))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (nil pool); body=%v", status, body)
	}
}
