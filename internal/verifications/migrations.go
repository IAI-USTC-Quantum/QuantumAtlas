package verifications

import "github.com/pocketbase/pocketbase/core"

const CollectionName = "verifications"

func init() {
	core.AppMigrations.Register(upCreateVerifications, downCreateVerifications, "1760000010_create_verifications.go")
}

func upCreateVerifications(app core.App) error {
	if existing, _ := app.FindCollectionByNameOrId(CollectionName); existing != nil {
		return nil
	}
	col := core.NewBaseCollection(CollectionName)
	col.ListRule = nil
	col.ViewRule = nil
	col.CreateRule = nil
	col.UpdateRule = nil
	col.DeleteRule = nil
	col.Fields.Add(&core.TextField{Name: "verification_id", Required: true, Max: 120})
	col.Fields.Add(&core.TextField{Name: "theorem_id", Required: true, Max: 120})
	col.Fields.Add(&core.TextField{Name: "plugin_id", Required: true, Max: 80})
	col.Fields.Add(&core.SelectField{
		Name:      "verdict",
		Required:  true,
		MaxSelect: 1,
		Values:    []string{"verified", "refuted", "disputed", "intractable", "pending", "failed"},
	})
	col.Fields.Add(&core.TextField{Name: "evidence_url", Max: 1000})
	col.Fields.Add(&core.TextField{Name: "payload", Max: 20000})
	col.Fields.Add(&core.AutodateField{Name: "created", OnCreate: true})
	col.Fields.Add(&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true})
	col.AddIndex("idx_verifications_verification_id", true, "verification_id", "")
	col.AddIndex("idx_verifications_theorem_id", false, "theorem_id", "")
	return app.Save(col)
}

func downCreateVerifications(app core.App) error {
	existing, err := app.FindCollectionByNameOrId(CollectionName)
	if err != nil {
		return err
	}
	return app.Delete(existing)
}
