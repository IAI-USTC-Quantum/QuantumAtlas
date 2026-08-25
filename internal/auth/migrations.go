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

func init() {
	core.AppMigrations.Register(upAddGitHubLoginField, downAddGitHubLoginField, "1787000000_add_github_login_to_users.go")
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
