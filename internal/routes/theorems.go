package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/theorems"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

type theoremRequest struct {
	ID          string `json:"id"`
	PageID      string `json:"page_id"`
	PaperID     string `json:"paper_id"`
	Section     string `json:"section"`
	MDLines     []int  `json:"md_lines"`
	StatementNL string `json:"statement_nl"`
}

func RegisterTheorems(se *core.ServeEvent, app core.App, enforcer *casbin.Enforcer) {
	se.Router.GET("/api/v1/theorems", scopeGuard(enforcer, "theorems", "read", func(re *core.RequestEvent) error {
		filter := ""
		params := map[string]any{}
		if paperID := strings.TrimSpace(re.Request.URL.Query().Get("paper_id")); paperID != "" {
			filter = "paper_id = {:paper_id}"
			params["paper_id"] = paperID
		}
		var (
			records []*core.Record
			err     error
		)
		if filter == "" {
			records, err = app.FindAllRecords(theorems.CollectionName)
		} else {
			records, err = app.FindRecordsByFilter(theorems.CollectionName, filter, "-created", 200, 0, params)
		}
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		out := make([]theorems.Theorem, 0, len(records))
		for _, rec := range records {
			out = append(out, theorems.FromRecord(rec))
		}
		return re.JSON(http.StatusOK, map[string]any{"theorems": out})
	}))
	se.Router.GET("/api/v1/theorems/{id}", scopeGuard(enforcer, "theorems", "read", func(re *core.RequestEvent) error {
		rec, err := app.FindFirstRecordByFilter(theorems.CollectionName, "theorem_id = {:id}", map[string]any{"id": re.Request.PathValue("id")})
		if err != nil {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "theorem not found"})
		}
		return re.JSON(http.StatusOK, theorems.FromRecord(rec))
	}))
	se.Router.POST("/api/v1/theorems", scopeGuard(enforcer, "theorems", "write", func(re *core.RequestEvent) error {
		var body theoremRequest
		if err := json.NewDecoder(re.Request.Body).Decode(&body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse body: " + err.Error()})
		}
		body.ID = strings.TrimSpace(body.ID)
		if body.ID == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "id is required"})
		}
		collection, err := app.FindCollectionByNameOrId(theorems.CollectionName)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		rec, err := app.FindFirstRecordByFilter(theorems.CollectionName, "theorem_id = {:id}", map[string]any{"id": body.ID})
		status := http.StatusOK
		if err != nil {
			rec = core.NewRecord(collection)
			status = http.StatusCreated
		}
		rec.Set("theorem_id", body.ID)
		rec.Set("page_id", body.PageID)
		rec.Set("paper_id", body.PaperID)
		rec.Set("section", body.Section)
		rec.Set("md_lines", theorems.EncodeLines(body.MDLines))
		rec.Set("statement_nl", body.StatementNL)
		if err := app.Save(rec); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": fmt.Sprintf("save theorem: %v", err)})
		}
		return re.JSON(status, theorems.FromRecord(rec))
	}))
}
