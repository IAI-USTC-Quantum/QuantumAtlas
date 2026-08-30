package routes

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/registry"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pocketbase/pocketbase/core"
)

// adminAcquisitionFailuresHandler lists failed papers with the latest
// durable PDF acquisition error. Historical failures created before the
// acquisition log migration remain visible with an empty reason.
func adminAcquisitionFailuresHandler(pool *pgxpool.Pool) func(*core.RequestEvent) error {
	store := registry.NewStore(pool)
	return func(re *core.RequestEvent) error {
		limit := 100
		if raw := re.Request.URL.Query().Get("limit"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 {
				limit = parsed
			}
		}
		items, err := store.ListAcquisitionFailures(re.Request.Context(), limit)
		if err != nil {
			if errors.Is(err, registry.ErrCatalogUnavailable) {
				return re.JSON(http.StatusServiceUnavailable, map[string]string{"detail": "registry unavailable"})
			}
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		return re.JSON(http.StatusOK, map[string]any{"items": items})
	}
}
