// pat migration: registers the pat_tokens collection on first boot.
//
// Why a Go-side AppMigration instead of a JSON snapshot? The collection
// has access-rule expressions tied to the live `users` collection,
// which means the migration needs to look up the users collection ID
// at apply time — easy to do in Go, awkward in the JSON exporter.
// Authentic-style "owner-only" rules use `@request.auth.id` against
// the indexed `user` field; the user's bcrypt hash never leaves the
// server side (`token_hash` is Hidden=true so even PocketBase admin UI
// hides it).
//
// The migration is registered via core.AppMigrations.Register (called
// from init() below). main.go imports this package for side effects so
// the migration runs as part of normal PocketBase serve/migrate boot.
//
// Schema:
//
//	user          Relation(users)  required, indexed, cascadeDelete
//	name          Text              required, max=80
//	prefix        Text              required, max=24   (display only)
//	token_hash    Text              required, hidden=true
//	description   Text              optional, max=200
//	expires_at    Date              optional (zero = never expires)
//	last_used_at  Date              optional (bumped by authGuard)
//
// Indexes:
//
//	(user, prefix) — speeds up Lookup's prefix-scoped scan and gives
//	                 uniqueness defense (two PATs for one user with the
//	                 same 12-char prefix is a ~1-in-2^47 fluke worth
//	                 surfacing via collision).
package pat

import (
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

func init() {
	core.AppMigrations.Register(upCreatePATTokens, downCreatePATTokens, "1748400000_create_pat_tokens.go")
}

// upCreatePATTokens creates the pat_tokens collection. Idempotent: if
// the collection already exists (e.g. operator hand-created it via the
// admin UI), we no-op rather than fail. PocketBase migrations are
// recorded by file name so re-running this up function is unusual but
// possible on a manually edited _migrations table.
func upCreatePATTokens(app core.App) error {
	if existing, _ := app.FindCollectionByNameOrId(CollectionName); existing != nil {
		return nil
	}

	usersCollection, err := app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		return err
	}

	col := core.NewBaseCollection(CollectionName)

	// ListRule / ViewRule: each user only sees their own PATs.
	// CreateRule / UpdateRule: nil — clients can't bypass our /api/pat
	// handlers to inject arbitrary hashes or change ownership. The
	// only legitimate creator is the PAT handler running with the
	// app-level Save (which bypasses the access rule).
	// DeleteRule: owners may revoke their own.
	ownerRule := "user = @request.auth.id"
	col.ListRule = types.Pointer(ownerRule)
	col.ViewRule = types.Pointer(ownerRule)
	col.CreateRule = nil
	col.UpdateRule = nil
	col.DeleteRule = types.Pointer(ownerRule)

	col.Fields.Add(&core.RelationField{
		Name:          "user",
		Required:      true,
		CollectionId:  usersCollection.Id,
		CascadeDelete: true,
		MaxSelect:     1,
	})
	col.Fields.Add(&core.TextField{
		Name:     "name",
		Required: true,
		Max:      80,
	})
	col.Fields.Add(&core.TextField{
		Name:     "prefix",
		Required: true,
		Max:      24,
	})
	col.Fields.Add(&core.TextField{
		Name:     "token_hash",
		Required: true,
		Hidden:   true, // PocketBase admin UI and API responses both hide this
		Max:      120, // bcrypt hashes are 60 chars; budget headroom for future algorithms
	})
	col.Fields.Add(&core.TextField{
		Name: "description",
		Max:  200,
	})
	col.Fields.Add(&core.DateField{
		Name: "expires_at",
	})
	col.Fields.Add(&core.DateField{
		Name: "last_used_at",
	})
	col.Fields.Add(&core.AutodateField{
		Name:     "created",
		OnCreate: true,
	})
	col.Fields.Add(&core.AutodateField{
		Name:     "updated",
		OnCreate: true,
		OnUpdate: true,
	})

	// (user, prefix) composite — Lookup's hot path queries on prefix
	// alone but having user first matches PocketBase's own access-rule
	// query pattern (`user = @request.auth.id`) and lets the same
	// index serve both list-mine and bcrypt-scan workloads.
	col.AddIndex("idx_pat_tokens_user_prefix", false, "user, prefix", "")

	return app.Save(col)
}

// downCreatePATTokens removes the collection so `migrate down` is a
// real inverse. PocketBase enforces double-sided migrations.
func downCreatePATTokens(app core.App) error {
	existing, err := app.FindCollectionByNameOrId(CollectionName)
	if err != nil {
		return nil // already absent — treat as success
	}
	return app.Delete(existing)
}
