package theorems

import (
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/types"
)

const CollectionName = "theorems"

func init() {
	core.AppMigrations.Register(upCreateTheorems, downCreateTheorems, "1760000000_create_theorems.go")
}

func upCreateTheorems(app core.App) error {
	if existing, _ := app.FindCollectionByNameOrId(CollectionName); existing != nil {
		return nil
	}
	col := core.NewBaseCollection(CollectionName)
	col.ListRule = nil
	col.ViewRule = nil
	col.CreateRule = nil
	col.UpdateRule = nil
	col.DeleteRule = nil
	col.Fields.Add(&core.TextField{Name: "theorem_id", Required: true, Max: 120})
	col.Fields.Add(&core.TextField{Name: "page_id", Max: 160})
	col.Fields.Add(&core.TextField{Name: "paper_id", Max: 200})
	col.Fields.Add(&core.TextField{Name: "section", Max: 200})
	col.Fields.Add(&core.TextField{Name: "md_lines", Max: 500})
	col.Fields.Add(&core.TextField{Name: "statement_nl", Max: 20000})
	col.Fields.Add(&core.AutodateField{Name: "created", OnCreate: true})
	col.Fields.Add(&core.AutodateField{Name: "updated", OnCreate: true, OnUpdate: true})
	col.AddIndex("idx_theorems_theorem_id", true, "theorem_id", "")
	col.AddIndex("idx_theorems_paper_id", false, "paper_id", "")
	return app.Save(col)
}

func downCreateTheorems(app core.App) error {
	existing, err := app.FindCollectionByNameOrId(CollectionName)
	if err != nil {
		return err
	}
	return app.Delete(existing)
}

func DateString(dt types.DateTime) string {
	if dt.IsZero() {
		return ""
	}
	return dt.String()
}
