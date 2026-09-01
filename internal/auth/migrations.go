// auth migrations: users-collection schema changes owned by the auth
// package. Registered via core.AppMigrations.Register (init below), so
// they run as part of the normal PocketBase serve/migrate boot on both
// fresh and existing pb_data directories.
package auth

import (
	"github.com/pocketbase/pocketbase/core"
)

// GitHubLoginField is the users-collection field that stores the
// GitHub account login (the `login` property of the GitHub profile —
// e.g. "Agony5757"). PocketBase's default OAuth2 mapped fields cover
// only name/avatar (see the PB init migration: MappedFields.Name =
// "name", MappedFields.AvatarURL = "avatar"; MappedFields.Username is
// left empty), so without this field the login — the identifier every
// QATLAS_*_GITHUB_LOGINS allowlist matches against — would never be
// persisted. The admin gate (internal/routes/admin.go) reads it off
// re.Auth.
//
// Populated lazily by the OnRecordAuthWithOAuth2Request hook in
// oauth.go: stamped on signup and backfilled on the next OAuth login
// for users who registered before this field existed. The PocketBase
// superuser can also set it manually via the admin UI (/_/) as a
// recovery path.
const GitHubLoginField = "github_login"

// GiteaLoginField is the gitea twin of GitHubLoginField: the users-
// collection field storing the Gitea account login (the `login` property
// of the Gitea user API response) after an OAuth sign-in via the gitea
// provider. Every gitea_* config allowlist matches against this value,
// exactly like the github lists match github_login. Stamped lazily by
// the same OnRecordAuthWithOAuth2Request hook (stampGiteaLogin).
const GiteaLoginField = "gitea_login"

// Role / availability fields on the users collection (see
// 1788100000_add_role_flags_to_users.go). The env-derived admin gate
// (Config.IsGitHubAdmin) stays authoritative for the /api/admin ops
// surface; these flags back the user-management surface
// (internal/routes/admin_users.go):
//
//   - IsAdminField: the holder may list all users and toggle their
//     availability (disabled), but not their admin flags.
//   - IsSuperadminField: the holder additionally may toggle other
//     users' IsAdminField.
//   - DisabledField: availability. true = the account is deactivated:
//     isAuthorized rejects its sessions and PATs, and the OAuth hook
//     refuses to mint a new session. Note the INVERTED polarity: PB
//     bool fields have no Default option, so the zero value must be
//     the common case (an enabled account) — hence "disabled", not
//     "active".
const (
	IsAdminField      = "is_admin"
	IsSuperadminField = "is_superadmin"
	DisabledField     = "disabled"
)

func init() {
	core.AppMigrations.Register(upAddGitHubLoginField, downAddGitHubLoginField, "1787000000_add_github_login_to_users.go")
	core.AppMigrations.Register(upAddRoleFlags, downAddRoleFlags, "1788100000_add_role_flags_to_users.go")
	core.AppMigrations.Register(upAddGiteaLoginField, downAddGiteaLoginField, "1789000000_add_gitea_login_to_users.go")
}

// upAddGitHubLoginField adds the github_login text column to the users
// collection. Idempotent: skipped when the field already exists
// (operator may have added it manually before this migration ran).
func upAddGitHubLoginField(app core.App) error {
	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		return err
	}
	if col.Fields.GetByName(GitHubLoginField) != nil {
		return nil
	}
	col.Fields.Add(&core.TextField{
		Name: GitHubLoginField,
		Max:  100, // GitHub logins cap at 39 chars; headroom is free
	})
	return app.Save(col)
}

// downAddGitHubLoginField removes the column added by the up. PocketBase
// requires symmetric migrations; the stored logins are dropped with the
// field, which is the right inverse for a schema rollback.
func downAddGitHubLoginField(app core.App) error {
	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		return nil // collection absent — treat as success
	}
	field := col.Fields.GetByName(GitHubLoginField)
	if field == nil {
		return nil
	}
	col.Fields.RemoveById(field.GetId())
	return app.Save(col)
}

// upAddGiteaLoginField adds the gitea_login text column to the users
// collection — the gitea twin of upAddGitHubLoginField, idempotent the
// same way.
func upAddGiteaLoginField(app core.App) error {
	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		return err
	}
	if col.Fields.GetByName(GiteaLoginField) != nil {
		return nil
	}
	col.Fields.Add(&core.TextField{
		Name: GiteaLoginField,
		Max:  100, // Gitea logins cap at 40 chars; headroom is free
	})
	return app.Save(col)
}

// downAddGiteaLoginField removes the column added by the up.
func downAddGiteaLoginField(app core.App) error {
	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		return nil // collection absent — treat as success
	}
	field := col.Fields.GetByName(GiteaLoginField)
	if field == nil {
		return nil
	}
	col.Fields.RemoveById(field.GetId())
	return app.Save(col)
}

// upAddRoleFlags adds the three role/availability bools to the users
// collection. Idempotent per-field (operator may have added some by hand
// via the PocketBase admin UI before this migration ran). No backfill
// happens here — see auth.Register's bootstrap promotion for how
// allowlisted logins get their flags stamped.
func upAddRoleFlags(app core.App) error {
	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		return err
	}
	want := []*core.BoolField{
		{Name: IsAdminField},
		{Name: IsSuperadminField},
		{Name: DisabledField},
	}
	changed := false
	for _, f := range want {
		if col.Fields.GetByName(f.Name) != nil {
			continue
		}
		col.Fields.Add(f)
		changed = true
	}
	if !changed {
		return nil
	}
	return app.Save(col)
}

// downAddRoleFlags removes the columns added by the up. Stored flags are
// dropped with the fields — the correct inverse for a schema rollback
// (the env allowlist remains the recovery path for admin access).
func downAddRoleFlags(app core.App) error {
	col, err := app.FindCollectionByNameOrId(UsersCollection)
	if err != nil {
		return nil // collection absent — treat as success
	}
	changed := false
	for _, name := range []string{IsAdminField, IsSuperadminField, DisabledField} {
		field := col.Fields.GetByName(name)
		if field == nil {
			continue
		}
		col.Fields.RemoveById(field.GetId())
		changed = true
	}
	if !changed {
		return nil
	}
	return app.Save(col)
}
