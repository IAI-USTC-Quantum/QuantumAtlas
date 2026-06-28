package routes

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"

	"github.com/pocketbase/pocketbase/core"
)

func TestRegisterPluginsNilRegistryIsEmpty(t *testing.T) {
	re := &core.RequestEvent{}
	re.Request = httptest.NewRequest(http.MethodGet, "/api/v1/plugins", nil)
	re.Response = httptest.NewRecorder()
	registry := &qplugin.Registry{}
	if err := func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]any{"plugins": registry.List()})
	}(re); err != nil {
		t.Fatalf("handler: %v", err)
	}
	var body map[string][]any
	if err := json.Unmarshal(re.Response.(*httptest.ResponseRecorder).Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(body["plugins"]) != 0 {
		t.Fatalf("plugins = %v, want empty", body["plugins"])
	}
}
