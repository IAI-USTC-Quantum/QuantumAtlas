package auth

import (
	"regexp"
	"testing"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tests"
	pbauth "github.com/pocketbase/pocketbase/tools/auth"
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
