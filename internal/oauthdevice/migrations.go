// oauthdevice migration: registers the oauth_device_codes collection
// on first boot. The handlers in internal/routes/oauthdevice.go are
// the only legitimate writers — the access rules below pin every
// CRUD path to "nil rule" (admin-only), which in PocketBase means
// "the framework's access-rule layer is bypassed; only code holding
// an *core.App with app.Save() can mutate the row".
//
// Schema rationale: see the package doc-comment of
// internal/oauthdevice/oauthdevice.go (storage choices for device
// vs user code, status enum semantics, atomic transitions).
//
// Indexes:
//
//	device_code_hash — UNIQUE. Every poll hits the table by hashed
//	                   device code; without the unique index a misbehaving
//	                   /code handler could insert dupes and break the
//	                   "one row per device_code" invariant.
//	user_code        — UNIQUE. Approve page looks up by user_code; we
//	                   never want two pending requests sharing the same
//	                   8-character code (it's the only thing the human
//	                   has).
//	(status, expires_at) — composite, non-unique. Used by the periodic
//	                       sweep that marks abandoned pending/approved
//	                       rows as expired.
package oauthdevice

import (
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/auth"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/pat"

	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

func init() {
	core.AppMigrations.Register(upCreateOAuthDeviceCodes, downCreateOAuthDeviceCodes, "1748600000_create_oauth_device_codes.go")
}

// upCreateOAuthDeviceCodes creates the oauth_device_codes collection.
// Idempotent like the pat_tokens migration: if the collection already
// exists we no-op.
func upCreateOAuthDeviceCodes(app core.App) error {
	if existing, _ := app.FindCollectionByNameOrId(CollectionName); existing != nil {
		return nil
	}

	usersCollection, err := app.FindCollectionByNameOrId(auth.UsersCollection)
	if err != nil {
		return err
	}

	patCollection, err := app.FindCollectionByNameOrId(pat.CollectionName)
	if err != nil {
		return err
	}

	col := core.NewBaseCollection(CollectionName)

	// All rules nil → only our handlers (using *core.App directly)
	// can mutate. /api/oauth/device/{code,token} are anonymous but
	// MUST go through our handlers, not the PocketBase REST CRUD
	// surface — the latter would let anyone read pending rows.
	col.ListRule = nil
	col.ViewRule = nil
	col.CreateRule = nil
	col.UpdateRule = nil
	col.DeleteRule = nil

	col.Fields.Add(&core.TextField{
		Name:     "device_code_hash",
		Required: true,
		Hidden:   true,
		Max:      64, // sha256 hex
	})
	col.Fields.Add(&core.TextField{
		Name:     "user_code",
		Required: true,
		Max:      32,
	})
	col.Fields.Add(&core.RelationField{
		Name:          "approved_user",
		CollectionId:  usersCollection.Id,
		CascadeDelete: true,
		MaxSelect:     1,
	})
	col.Fields.Add(&core.RelationField{
		Name:          "pat",
		CollectionId:  patCollection.Id,
		CascadeDelete: false, // revoking a PAT must not nuke its audit trail here
		MaxSelect:     1,
	})
	col.Fields.Add(&core.TextField{
		Name:     "name",
		Required: true,
		Max:      80,
	})
	col.Fields.Add(&core.TextField{
		Name: "description",
		Max:  200,
	})
	col.Fields.Add(&core.TextField{
		Name: "scopes",
		Max:  500, // JSON-encoded []string, same shape as pat_tokens.scopes
	})
	col.Fields.Add(&core.NumberField{
		Name:     "expires_in_days",
		Required: true,
		OnlyInt:  true,
		Min:      types.Pointer(1.0),
		Max:      types.Pointer(365.0),
	})
	col.Fields.Add(&core.SelectField{
		Name:      "status",
		Required:  true,
		MaxSelect: 1,
		Values: []string{
			StatusPending,
			StatusApproved,
			StatusConsumed,
			StatusDenied,
			StatusExpired,
		},
	})
	col.Fields.Add(&core.NumberField{
		Name:    "poll_count",
		OnlyInt: true,
		Min:     types.Pointer(0.0),
	})
	col.Fields.Add(&core.DateField{
		Name: "last_polled_at",
	})
	col.Fields.Add(&core.DateField{
		Name: "approved_at",
	})
	col.Fields.Add(&core.DateField{
		Name:     "expires_at",
		Required: true,
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

	col.AddIndex("idx_oauth_device_codes_hash", true, "device_code_hash", "")
	col.AddIndex("idx_oauth_device_codes_user_code", true, "user_code", "")
	col.AddIndex("idx_oauth_device_codes_status_expires", false, "status, expires_at", "")

	return app.Save(col)
}

// downCreateOAuthDeviceCodes deletes the collection so `migrate down`
// is a real inverse. PocketBase enforces double-sided migrations.
func downCreateOAuthDeviceCodes(app core.App) error {
	existing, err := app.FindCollectionByNameOrId(CollectionName)
	if err != nil {
		return nil // already absent — treat as success
	}
	return app.Delete(existing)
}
