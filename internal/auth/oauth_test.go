package auth

import (
	"regexp"
	"testing"

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
