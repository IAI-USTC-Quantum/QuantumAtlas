package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	pbauth "github.com/pocketbase/pocketbase/tools/auth"
	"github.com/pocketbase/pocketbase/tools/router"
)

func TestDeriveStableUserID_DeterministicAndProperShape(t *testing.T) {
	got := deriveStableUserID("github", "12345")
	if len(got) != stableUserIDLength {
		t.Errorf("len = %d, want %d", len(got), stableUserIDLength)
	}
	if got[:len(StableUserIDPrefix)] != StableUserIDPrefix {
		t.Errorf("missing prefix %q in %q", StableUserIDPrefix, got)
	}
	// Default PocketBase id charset is alphanumeric.
	if !regexp.MustCompile(`^[a-zA-Z0-9]+$`).MatchString(got) {
		t.Errorf("id %q is not pure alphanumeric (would fail PB id validator)", got)
	}
	// Determinism: same inputs → same id, every call.
	for i := 0; i < 5; i++ {
		if deriveStableUserID("github", "12345") != got {
			t.Errorf("deriveStableUserID is not deterministic (call %d)", i)
		}
	}
}

// Cross-provider collision protection: different providers with the
// same provider-user-id MUST produce different qatlas ids.
func TestDeriveStableUserID_ProviderSeparation(t *testing.T) {
	gh := deriveStableUserID("github", "12345")
	g := deriveStableUserID("google", "12345")
	if gh == g {
		t.Errorf("github and google with same provider-user-id collided: %s", gh)
	}
}

// Different provider-user-ids → different qatlas ids (no obvious
// truncation collision in the 14-hex-char window).
func TestDeriveStableUserID_UserSeparation(t *testing.T) {
	a := deriveStableUserID("github", "12345")
	b := deriveStableUserID("github", "12346")
	if a == b {
		t.Errorf("adjacent provider-user-ids collided: %s", a)
	}
}

// Empty inputs are still hashed — we don't treat them specially. (The
// caller in syncStableUserID short-circuits before reaching us when
// provider or user id is blank, but deriveStableUserID itself is pure
// and predictable for any input.)
func TestDeriveStableUserID_EmptyInputsStillHash(t *testing.T) {
	got := deriveStableUserID("", "")
	if len(got) != stableUserIDLength {
		t.Errorf("len = %d, want %d", len(got), stableUserIDLength)
	}
}

// ---------------------------------------------------------------------------
// stampGitHubLogin — persists the GitHub login onto the users record
// ---------------------------------------------------------------------------

func newStampEvent(app core.App, provider, login string, rec *core.Record) *core.RecordAuthWithOAuth2RequestEvent {
	return &core.RecordAuthWithOAuth2RequestEvent{
		RequestEvent: &core.RequestEvent{App: app},
		ProviderName: provider,
		OAuth2User:   &pbauth.AuthUser{Username: login},
		Record:       rec,
	}
}

func TestStampGitHubLogin_StampsAndPersists(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail("stamp@example.com")
	rec.SetPassword("stamp-test-password")
	if err := app.Save(rec); err != nil {
		t.Fatalf("save user: %v", err)
	}

	stampGitHubLogin(newStampEvent(app, pbauth.NameGithub, "Agony5757", rec))

	persisted, err := app.FindRecordById(UsersCollection, rec.Id)
	if err != nil {
		t.Fatalf("refetch user: %v", err)
	}
	if got := persisted.GetString(GitHubLoginField); got != "Agony5757" {
		t.Errorf("github_login = %q, want %q", got, "Agony5757")
	}
}

func TestStampGitHubLogin_SkipsNonGitHubAndBlank(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}

	cases := []struct {
		name     string
		provider string
		login    string
		email    string
	}{
		{"non-github provider", "google", "someone", "stamp-google@example.com"},
		{"blank login", pbauth.NameGithub, "", "stamp-blank@example.com"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := core.NewRecord(col)
			rec.SetEmail(tc.email)
			rec.SetPassword("stamp-test-password")
			if err := app.Save(rec); err != nil {
				t.Fatalf("save user: %v", err)
			}
			stampGitHubLogin(newStampEvent(app, tc.provider, tc.login, rec))
			if got := rec.GetString(GitHubLoginField); got != "" {
				t.Errorf("github_login = %q, want empty", got)
			}
		})
	}
}

// stampGiteaLogin mirrors stampGitHubLogin: stamps gitea_login for the
// gitea provider only, and never touches github_login (or vice versa —
// a GitHub sign-in must not stamp gitea_login).
func TestStampGiteaLogin_ProviderIsolation(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail("stamp-gitea@example.com")
	rec.SetPassword("stamp-gitea-password")
	if err := app.Save(rec); err != nil {
		t.Fatalf("save user: %v", err)
	}

	stampGiteaLogin(newStampEvent(app, pbauth.NameGitea, "gitea-user", rec))
	persisted, err := app.FindRecordById(UsersCollection, rec.Id)
	if err != nil {
		t.Fatalf("refetch user: %v", err)
	}
	if got := persisted.GetString(GiteaLoginField); got != "gitea-user" {
		t.Errorf("gitea_login = %q, want %q", got, "gitea-user")
	}
	if got := persisted.GetString(GitHubLoginField); got != "" {
		t.Errorf("github_login = %q after gitea stamp, want empty", got)
	}

	// A GitHub sign-in on the same record must stamp github_login but
	// leave gitea_login untouched.
	stampGitHubLogin(newStampEvent(app, pbauth.NameGithub, "gh-user", persisted))
	if got := persisted.GetString(GitHubLoginField); got != "gh-user" {
		t.Errorf("github_login = %q, want %q", got, "gh-user")
	}
	if got := persisted.GetString(GiteaLoginField); got != "gitea-user" {
		t.Errorf("gitea_login = %q after github stamp, want %q", got, "gitea-user")
	}

	// Non-gitea provider never stamps gitea_login.
	stampGiteaLogin(newStampEvent(app, "google", "someone", persisted))
	if got := persisted.GetString(GiteaLoginField); got != "gitea-user" {
		t.Errorf("gitea_login = %q after google stamp, want unchanged", got)
	}
}

// ---------------------------------------------------------------------------
// rejectDisabledUser — OAuth front door for the availability flag
// ---------------------------------------------------------------------------

func TestRejectDisabledUser(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}

	// Fresh signup shape: e.Record nil (PB hasn't matched an existing
	// record yet) — must pass, new users can't have been disabled.
	if err := rejectDisabledUser(newStampEvent(app, pbauth.NameGithub, "someone", nil)); err != nil {
		t.Errorf("nil record: err = %v, want nil", err)
	}

	enabled := core.NewRecord(col)
	enabled.SetEmail("enabled@example.com")
	enabled.SetPassword("enabled-test-password")
	if err := app.Save(enabled); err != nil {
		t.Fatalf("save enabled: %v", err)
	}
	if err := rejectDisabledUser(newStampEvent(app, pbauth.NameGithub, "someone", enabled)); err != nil {
		t.Errorf("enabled record: err = %v, want nil", err)
	}

	disabled := core.NewRecord(col)
	disabled.SetEmail("disabled@example.com")
	disabled.SetPassword("disabled-test-password")
	disabled.Set(DisabledField, true)
	if err := app.Save(disabled); err != nil {
		t.Fatalf("save disabled: %v", err)
	}
	if err := rejectDisabledUser(newStampEvent(app, pbauth.NameGithub, "someone", disabled)); err == nil {
		t.Error("disabled record: err = nil, want forbidden error")
	}
}

// ---------------------------------------------------------------------------
// promoteRoleFlags — bootstrap seeding from the config lists
// ---------------------------------------------------------------------------

func TestPromoteRoleFlags(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	cfg := &config.Config{
		AdminGitHubLogins:      []string{"Boss1"},
		SuperadminGitHubLogins: []string{"Root2"},
	}

	mk := func(email, login string, isAdmin, isSuper bool) *core.Record {
		col, err := app.FindCollectionByNameOrId(UsersCollection)
		if err != nil {
			t.Fatalf("users collection: %v", err)
		}
		rec := core.NewRecord(col)
		rec.SetEmail(email)
		rec.SetPassword("promote-test-password")
		rec.Set(GitHubLoginField, login)
		rec.Set(IsAdminField, isAdmin)
		rec.Set(IsSuperadminField, isSuper)
		if err := app.Save(rec); err != nil {
			t.Fatalf("save %s: %v", email, err)
		}
		return rec
	}

	plain := mk("plain@example.com", "nobody", false, false)
	boss := mk("boss@example.com", "boss1", false, false) // case-insensitive match
	root := mk("root@example.com", "Root2", false, false)
	preSet := mk("preset@example.com", "nobody2", true, false) // is_admin already on, login unlisted

	if err := promoteRoleFlags(app, cfg); err != nil {
		t.Fatalf("promoteRoleFlags: %v", err)
	}

	refetch := func(id string) *core.Record {
		rec, err := app.FindRecordById(UsersCollection, id)
		if err != nil {
			t.Fatalf("refetch %s: %v", id, err)
		}
		return rec
	}

	if refetch(plain.Id).GetBool(IsAdminField) || refetch(plain.Id).GetBool(IsSuperadminField) {
		t.Error("unlisted login got promoted")
	}
	if !refetch(boss.Id).GetBool(IsAdminField) {
		t.Error("admin_listed login (case-insensitive) not promoted to is_admin")
	}
	if refetch(boss.Id).GetBool(IsSuperadminField) {
		t.Error("admin-only login should not be superadmin")
	}
	if !refetch(root.Id).GetBool(IsSuperadminField) {
		t.Error("superadmin_listed login not promoted to is_superadmin")
	}
	// Promotion must not DEMOTE pre-existing flags for unlisted logins.
	if !refetch(preSet.Id).GetBool(IsAdminField) {
		t.Error("pre-existing is_admin was demoted — promotion must be monotonic")
	}
}

// ---------------------------------------------------------------------------
// enforceLoginAllowlist — github allowlist, gitea open door
// ---------------------------------------------------------------------------

func TestEnforceLoginAllowlist_ProviderBranches(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	cfg := &config.Config{
		AllowedGitHubLogins: []string{"gh-user"},
		AdminGiteaLogins:    []string{"gitea-user"},
	}

	if err := enforceLoginAllowlist(newStampEvent(app, pbauth.NameGithub, "gh-user", nil), cfg); err != nil {
		t.Errorf("github allowlisted login rejected: %v", err)
	}
	// Gitea is deliberately NOT allowlist-gated: every account on the
	// configured instance may sign in, listed anywhere or not.
	for _, login := range []string{"gitea-user", "total-stranger", ""} {
		if err := enforceLoginAllowlist(newStampEvent(app, pbauth.NameGitea, login, nil), cfg); err != nil {
			t.Errorf("gitea login %q rejected: %v", login, err)
		}
	}

	// GitHub keeps the fail-closed allowlist; unknown providers too.
	if err := enforceLoginAllowlist(newStampEvent(app, pbauth.NameGithub, "stranger", nil), cfg); err == nil {
		t.Error("unlisted github login admitted")
	}
	if err := enforceLoginAllowlist(newStampEvent(app, "google", "anyone", nil), cfg); err == nil {
		t.Error("unknown provider admitted")
	}
	// Nil OAuth2User (no identity) is rejected even when the list would
	// match an empty login — never let an unidentified caller through.
	// (Gitea's open door still implies an identity; nil OAuth2User cannot
	// reach the hook in the real flow.)
	e := newStampEvent(app, pbauth.NameGithub, "", nil)
	e.OAuth2User = nil
	if err := enforceLoginAllowlist(e, cfg); err == nil {
		t.Error("nil OAuth2User admitted")
	}
}

// ---------------------------------------------------------------------------
// syncGiteaProvider — endpoint overrides + idempotency
// ---------------------------------------------------------------------------

func findProviderCfg(col *core.Collection, name string) *core.OAuth2ProviderConfig {
	for i, p := range col.OAuth2.Providers {
		if p.Name == name {
			return &col.OAuth2.Providers[i]
		}
	}
	return nil
}

func TestSyncGiteaProvider(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	cfg := &config.Config{
		GiteaURL:          "https://git.example.com",
		GiteaClientID:     "gitea-client",
		GiteaClientSecret: "gitea-secret",
	}
	if err := syncGiteaProvider(app, cfg); err != nil {
		t.Fatalf("syncGiteaProvider: %v", err)
	}

	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	if !col.OAuth2.Enabled {
		t.Error("collection OAuth2 not enabled after sync")
	}
	p := findProviderCfg(col, pbauth.NameGitea)
	if p == nil {
		t.Fatal("gitea provider not present after sync")
	}
	if p.ClientId != "gitea-client" || p.ClientSecret != "gitea-secret" {
		t.Errorf("client creds = %q/%q", p.ClientId, p.ClientSecret)
	}
	// The stock PB gitea provider points at gitea.com — the sync must
	// override all three endpoints from GiteaURL.
	if p.AuthURL != "https://git.example.com/login/oauth/authorize" {
		t.Errorf("AuthURL = %q", p.AuthURL)
	}
	if p.TokenURL != "https://git.example.com/login/oauth/access_token" {
		t.Errorf("TokenURL = %q", p.TokenURL)
	}
	if p.UserInfoURL != "https://git.example.com/api/v1/user" {
		t.Errorf("UserInfoURL = %q", p.UserInfoURL)
	}

	// Re-sync with rotated credentials: exactly one gitea entry, updated
	// in place, other fields preserved.
	cfg.GiteaClientSecret = "gitea-secret-2"
	if err := syncGiteaProvider(app, cfg); err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	col, err = app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		t.Fatalf("refetch collection: %v", err)
	}
	count := 0
	for _, q := range col.OAuth2.Providers {
		if q.Name == pbauth.NameGitea {
			count++
			if q.ClientSecret != "gitea-secret-2" {
				t.Errorf("ClientSecret = %q after re-sync", q.ClientSecret)
			}
		}
	}
	if count != 1 {
		t.Errorf("gitea provider entries = %d after re-sync, want 1", count)
	}
}

func TestSyncGiteaProvider_Guards(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	// Empty creds: no-op, no error.
	if err := syncGiteaProvider(app, &config.Config{}); err != nil {
		t.Errorf("empty config: err = %v, want nil", err)
	}
	col, _ := app.FindCollectionByNameOrId(UsersCollection)
	if findProviderCfg(col, pbauth.NameGitea) != nil {
		t.Error("gitea provider added with empty config")
	}

	// Creds set but no gitea_url: hard error, provider NOT added (never
	// silently fall back to the public gitea.com defaults).
	cfg := &config.Config{GiteaClientID: "c", GiteaClientSecret: "s"}
	if err := syncGiteaProvider(app, cfg); err == nil {
		t.Error("missing gitea_url: err = nil, want error")
	}
	col, _ = app.FindCollectionByNameOrId(UsersCollection)
	if findProviderCfg(col, pbauth.NameGitea) != nil {
		t.Error("gitea provider added despite missing gitea_url")
	}
}

// ---------------------------------------------------------------------------
// promoteRoleFlags — gitea seed lists
// ---------------------------------------------------------------------------

func TestPromoteRoleFlags_GiteaLists(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	// GitHub lists empty; only the Gitea lists drive promotion here.
	cfg := &config.Config{
		AdminGiteaLogins:      []string{"GiteaBoss"},
		SuperadminGiteaLogins: []string{"GiteaRoot"},
	}

	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	mk := func(email, giteaLogin string) *core.Record {
		rec := core.NewRecord(col)
		rec.SetEmail(email)
		rec.SetPassword("promote-gitea-password")
		rec.Set(GiteaLoginField, giteaLogin)
		if err := app.Save(rec); err != nil {
			t.Fatalf("save %s: %v", email, err)
		}
		return rec
	}
	boss := mk("gitea-boss@example.com", "giteaboss") // case-insensitive match
	root := mk("gitea-root@example.com", "GiteaRoot")
	plain := mk("gitea-plain@example.com", "nobody")

	if err := promoteRoleFlags(app, cfg); err != nil {
		t.Fatalf("promoteRoleFlags: %v", err)
	}

	refetch := func(id string) *core.Record {
		rec, err := app.FindRecordById(UsersCollection, id)
		if err != nil {
			t.Fatalf("refetch %s: %v", id, err)
		}
		return rec
	}
	if !refetch(boss.Id).GetBool(IsAdminField) {
		t.Error("gitea-admin-listed login not promoted to is_admin")
	}
	if !refetch(root.Id).GetBool(IsSuperadminField) {
		t.Error("gitea-superadmin-listed login not promoted to is_superadmin")
	}
	if refetch(plain.Id).GetBool(IsAdminField) || refetch(plain.Id).GetBool(IsSuperadminField) {
		t.Error("unlisted gitea login got promoted")
	}
}

// ---------------------------------------------------------------------------
// checkOAuthConflicts — the sign-in match prompt gate
// ---------------------------------------------------------------------------

// newConflictEvent builds a hook event whose response can actually be
// written (checkOAuthConflicts answers interruptions with e.JSON(409, ...)),
// plus the recorder to inspect it.
func newConflictEvent(
	app core.App,
	provider, providerUserID, login string,
	rec *core.Record,
	auth *core.Record,
) (*core.RecordAuthWithOAuth2RequestEvent, *httptest.ResponseRecorder) {
	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		panic(err)
	}
	recorder := httptest.NewRecorder()
	e := &core.RecordAuthWithOAuth2RequestEvent{
		RequestEvent: &core.RequestEvent{
			App:  app,
			Auth: auth,
			Event: router.Event{
				Response: recorder,
				Request:  httptest.NewRequest(http.MethodPost, "/api/collections/users/auth-with-oauth2", nil),
			},
		},
		ProviderName: provider,
		OAuth2User:   &pbauth.AuthUser{Id: providerUserID, Username: login},
		Record:       rec,
	}
	// Collection lives on an unexported embedded type — settable via its
	// promoted exported field, just not in the composite literal.
	e.Collection = col
	return e, recorder
}

// conflictBody decodes the 409 body the hook writes and returns the inner
// data map (code/provider/login/conflict/existing).
func conflictBody(t *testing.T, recorder *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409; body=%s", recorder.Code, recorder.Body.String())
	}
	var parsed struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &parsed); err != nil {
		t.Fatalf("decode 409 body: %v (%s)", err, recorder.Body.String())
	}
	return parsed.Data
}

func mkUser(t *testing.T, app core.App, email string, fields map[string]string) *core.Record {
	t.Helper()
	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		t.Fatalf("users collection: %v", err)
	}
	rec := core.NewRecord(col)
	rec.SetEmail(email)
	rec.SetPassword("conflict-test-password")
	for k, v := range fields {
		rec.Set(k, v)
	}
	if err := app.Save(rec); err != nil {
		t.Fatalf("save user %s: %v", email, err)
	}
	return rec
}

// Email match: PocketBase resolved e.Record via the same-email lookup —
// the hook must interrupt with conflict:"email" instead of letting the
// identity silently graft onto that record.
func TestCheckOAuthConflicts_EmailMatchInterrupts(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	existing := mkUser(t, app, "shared@example.com", map[string]string{GitHubLoginField: "alice"})
	e, rec := newConflictEvent(app, pbauth.NameGitea, "777", "alice-on-gitea", existing, nil)
	if err := checkOAuthConflicts(e); err != nil {
		t.Fatalf("checkOAuthConflicts: %v", err)
	}
	data := conflictBody(t, rec)
	if data["code"] != OAuthConflictCode {
		t.Errorf("code = %v, want %q", data["code"], OAuthConflictCode)
	}
	if data["conflict"] != "email" {
		t.Errorf("conflict = %v, want email", data["conflict"])
	}
	if data["provider"] != pbauth.NameGitea {
		t.Errorf("provider = %v", data["provider"])
	}
	if !strings.Contains(data["existing"].(string), "alice") ||
		!strings.Contains(data["existing"].(string), "shared@example.com") {
		t.Errorf("existing = %v, want alice + email", data["existing"])
	}
}

// Returning identity (externalAuth row exists): plain re-login, never a
// conflict — even when the record has the same email the OAuth user
// would present.
func TestCheckOAuthConflicts_ReturningIdentityPasses(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	existing := mkUser(t, app, "back@example.com", map[string]string{GiteaLoginField: "back"})
	rel := core.NewExternalAuth(app)
	rel.SetCollectionRef(existing.Collection().Id)
	rel.SetRecordRef(existing.Id)
	rel.SetProvider(pbauth.NameGitea)
	rel.SetProviderId("42")
	if err := app.Save(rel); err != nil {
		t.Fatalf("save external auth: %v", err)
	}

	e, rec := newConflictEvent(app, pbauth.NameGitea, "42", "back", existing, nil)
	if err := checkOAuthConflicts(e); err != nil {
		t.Fatalf("checkOAuthConflicts: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("returning identity was interrupted (status %d, body=%s)", rec.Code, rec.Body.String())
	}
}

// Bind flow: an authenticated caller whose e.Record is themselves never
// triggers the prompt — that's the dashboard 账号绑定 exchange.
func TestCheckOAuthConflicts_BindFlowPasses(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	caller := mkUser(t, app, "caller@example.com", map[string]string{GitHubLoginField: "caller"})
	// Same-email match shape (e.Record == the caller) — must pass.
	e, rec := newConflictEvent(app, pbauth.NameGitea, "9", "caller", caller, caller)
	if err := checkOAuthConflicts(e); err != nil {
		t.Fatalf("checkOAuthConflicts: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("bind flow was interrupted (status %d, body=%s)", rec.Code, rec.Body.String())
	}
	// Fresh-signup shape for the caller linking a same-named identity —
	// also fine (e.Record nil but e.Auth is set; PB would resolve the
	// record to the caller before our hook anyway).
	e2, rec2 := newConflictEvent(app, pbauth.NameGitea, "9", "caller", nil, caller)
	if err := checkOAuthConflicts(e2); err != nil {
		t.Fatalf("checkOAuthConflicts (fresh shape): %v", err)
	}
	_ = rec2
}

// Username conflict on a fresh sign-up: the first attempt is interrupted
// with conflict:"username"; the retry (the ack recorded by the 409) is
// allowed through to create the independent account the user chose.
func TestCheckOAuthConflicts_UsernameConflictAckRetry(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	// Reset the process-global ack store so the test is hermetic.
	t.Cleanup(func() { conflictAcks = &ackStore{seen: map[string]time.Time{}} })
	conflictAcks = &ackStore{seen: map[string]time.Time{}}

	mkUser(t, app, "squat@example.com", map[string]string{GitHubLoginField: "Alice"})

	// First attempt: interrupted.
	e, rec := newConflictEvent(app, pbauth.NameGitea, "5", "ALICE", nil, nil)
	if err := checkOAuthConflicts(e); err != nil {
		t.Fatalf("checkOAuthConflicts: %v", err)
	}
	data := conflictBody(t, rec)
	if data["conflict"] != "username" {
		t.Errorf("conflict = %v, want username", data["conflict"])
	}
	if data["login"] != "ALICE" {
		t.Errorf("login = %v", data["login"])
	}
	if !strings.Contains(data["existing"].(string), "Alice") {
		t.Errorf("existing = %v, want the github: Alice match", data["existing"])
	}

	// Retry with the same identity: ack lets it through.
	e2, rec2 := newConflictEvent(app, pbauth.NameGitea, "5", "ALICE", nil, nil)
	if err := checkOAuthConflicts(e2); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if rec2.Code != http.StatusOK {
		t.Errorf("acknowledged retry was interrupted (status %d, body=%s)", rec2.Code, rec2.Body.String())
	}

	// A DIFFERENT identity with the same conflicting login is still
	// interrupted — the ack is per provider identity, not per login.
	e3, rec3 := newConflictEvent(app, pbauth.NameGitea, "6", "ALICE", nil, nil)
	if err := checkOAuthConflicts(e3); err != nil {
		t.Fatalf("other identity: %v", err)
	}
	if rec3.Code != http.StatusConflict {
		t.Errorf("other identity not interrupted (status %d)", rec3.Code)
	}
}

// No conflict at all: a fresh sign-up whose login matches nothing passes.
func TestCheckOAuthConflicts_NoConflictPasses(t *testing.T) {
	app, err := tests.NewTestApp()
	if err != nil {
		t.Fatalf("NewTestApp: %v", err)
	}
	t.Cleanup(app.Cleanup)

	mkUser(t, app, "someone@example.com", map[string]string{GitHubLoginField: "someone"})

	e, rec := newConflictEvent(app, pbauth.NameGitea, "8", "brand-new-user", nil, nil)
	if err := checkOAuthConflicts(e); err != nil {
		t.Fatalf("checkOAuthConflicts: %v", err)
	}
	if rec.Code != http.StatusOK {
		t.Errorf("unconflicted sign-in interrupted (status %d, body=%s)", rec.Code, rec.Body.String())
	}
}
