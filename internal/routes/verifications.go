package routes

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/verifications"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

type verificationRequest struct {
	ID          string         `json:"id"`
	TheoremID   string         `json:"theorem_id"`
	PluginID    string         `json:"plugin_id"`
	Verdict     string         `json:"verdict"`
	EvidenceURL string         `json:"evidence_url"`
	Payload     map[string]any `json:"payload"`
}

func RegisterVerifications(se *core.ServeEvent, app core.App, enforcer *casbin.Enforcer) {
	se.Router.GET("/api/v1/verifications", scopeGuard(enforcer, "verifications", "read", func(re *core.RequestEvent) error {
		filter := ""
		params := map[string]any{}
		if theoremID := strings.TrimSpace(re.Request.URL.Query().Get("theorem_id")); theoremID != "" {
			filter = "theorem_id = {:theorem_id}"
			params["theorem_id"] = theoremID
		}
		var (
			records []*core.Record
			err     error
		)
		if filter == "" {
			records, err = app.FindAllRecords(verifications.CollectionName)
		} else {
			records, err = app.FindRecordsByFilter(verifications.CollectionName, filter, "-created", 200, 0, params)
		}
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		out := make([]verifications.Verification, 0, len(records))
		for _, rec := range records {
			out = append(out, verifications.FromRecord(rec))
		}
		return re.JSON(http.StatusOK, map[string]any{"verifications": out})
	}))
	se.Router.POST("/api/v1/verifications", scopeGuard(enforcer, "verifications", "write", func(re *core.RequestEvent) error {
		var body verificationRequest
		if err := json.NewDecoder(re.Request.Body).Decode(&body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "parse body: " + err.Error()})
		}
		body.ID = strings.TrimSpace(body.ID)
		body.TheoremID = strings.TrimSpace(body.TheoremID)
		body.PluginID = strings.TrimSpace(body.PluginID)
		body.Verdict = strings.TrimSpace(body.Verdict)
		if body.ID == "" || body.TheoremID == "" || body.PluginID == "" || body.Verdict == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "id, theorem_id, plugin_id and verdict are required"})
		}
		collection, err := app.FindCollectionByNameOrId(verifications.CollectionName)
		if err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		rec, err := app.FindFirstRecordByFilter(verifications.CollectionName, "verification_id = {:id}", map[string]any{"id": body.ID})
		status := http.StatusOK
		if err != nil {
			rec = core.NewRecord(collection)
			status = http.StatusCreated
		}
		rec.Set("verification_id", body.ID)
		rec.Set("theorem_id", body.TheoremID)
		rec.Set("plugin_id", body.PluginID)
		rec.Set("verdict", body.Verdict)
		rec.Set("evidence_url", body.EvidenceURL)
		rec.Set("payload", verifications.EncodePayload(body.Payload))
		if err := app.Save(rec); err != nil {
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": fmt.Sprintf("save verification: %v", err)})
		}
		return re.JSON(status, verifications.FromRecord(rec))
	}))
}
