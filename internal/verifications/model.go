package verifications

import (
	"encoding/json"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/theorems"
	"github.com/pocketbase/pocketbase/core"
)

type Verification struct {
	ID          string         `json:"id"`
	TheoremID   string         `json:"theorem_id"`
	PluginID    string         `json:"plugin_id"`
	Verdict     string         `json:"verdict"`
	EvidenceURL string         `json:"evidence_url,omitempty"`
	Payload     map[string]any `json:"payload,omitempty"`
	Created     string         `json:"created_at,omitempty"`
	Updated     string         `json:"updated_at,omitempty"`
}

func FromRecord(rec *core.Record) Verification {
	payload := map[string]any{}
	_ = json.Unmarshal([]byte(rec.GetString("payload")), &payload)
	if len(payload) == 0 {
		payload = nil
	}
	return Verification{
		ID:          rec.GetString("verification_id"),
		TheoremID:   rec.GetString("theorem_id"),
		PluginID:    rec.GetString("plugin_id"),
		Verdict:     rec.GetString("verdict"),
		EvidenceURL: rec.GetString("evidence_url"),
		Payload:     payload,
		Created:     theorems.DateString(rec.GetDateTime("created")),
		Updated:     theorems.DateString(rec.GetDateTime("updated")),
	}
}

func EncodePayload(payload map[string]any) string {
	if len(payload) == 0 {
		return ""
	}
	data, _ := json.Marshal(payload)
	return string(data)
}
