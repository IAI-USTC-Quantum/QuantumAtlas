package routes

import (
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"
	"github.com/pocketbase/pocketbase/core"
)

// Old internal handler signatures are retained for integrations, but they no
// longer read mutable markdown or infer readiness from historical outputs.
func legacyContentCatalog(re *core.RequestEvent, cfg *config.Config, converter *mineru.Converter) (contentCatalog, bool, error) {
	if !contentAccessEnabled(cfg) {
		return nil, false, re.JSON(http.StatusNotFound, map[string]string{"detail": "paper access disabled"})
	}
	if converter != nil {
		if catalog, ok := converter.SourceCatalog().(contentCatalog); ok {
			return catalog, true, nil
		}
	}
	return nil, false, re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "content catalog unavailable"})
}
