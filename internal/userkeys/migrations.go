// userkeys migration: registers the search_api_keys collection on first
// boot. Same shape and rationale as pat_tokens (internal/pat/migrations.go):
// a Go-side AppMigration because the owner-only access rules need the
// live users collection ID at apply time, and the only legitimate writer
// is the server-side /api/me/search-keys handler (Create/Update rules
// nil, so clients cannot inject records through the generic collection
// API).
//
// Schema:
//
//	user           Relation(users)  required, indexed, cascadeDelete
//	backend        Text              required, max=40 (backend name)
//	key_encrypted  Text              required, hidden=true, max=1024
//	created        Autodate          on create
//	updated        Autodate          on create + update
//
// Indexes:
//
//	(user, backend) UNIQUE — one key per user per backend; also the hot
//	                   path for the per-(user, backend) lookups.
package userkeys

import (
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

func init() {
	core.AppMigrations.Register(upCreateSearchAPIKeys, downCreateSearchAPIKeys, "1791000000_create_search_api_keys.go")
}

func upCreateSearchAPIKeys(app core.App) error {
	if existing, _ := app.FindCollectionByNameOrId(CollectionName); existing != nil {
		return nil // idempotent
	}

	usersCollection, err := app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		return err
	}

	col := core.NewBaseCollection(CollectionName)

	// Owner-only read/delete; writes only through the server-side
	// handlers (nil rules), mirroring pat_tokens.
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
		Name:     "backend",
		Required: true,
		Max:      MaxBackendLen,
	})
	col.Fields.Add(&core.TextField{
		Name:     "key_encrypted",
		Required: true,
		Hidden:   true, // base64 AES-256-GCM ciphertext; never leaves the server
		Max:      1024, // base64 of (12B nonce + tag + <=512B plaintext) ≈ 580 chars
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

	col.AddIndex("idx_search_api_keys_user_backend", true, "user, backend", "")

	return app.Save(col)
}

func downCreateSearchAPIKeys(app core.App) error {
	existing, err := app.FindCollectionByNameOrId(CollectionName)
	if err != nil {
		return nil // already absent
	}
	return app.Delete(existing)
}
