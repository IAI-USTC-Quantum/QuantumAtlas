package routes

import (
	"net/http"

	qplugin "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/plugin"

	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/core"
)

func RegisterPlugins(se *core.ServeEvent, registry *qplugin.Registry, enforcer *casbin.Enforcer) {
	if registry == nil {
		registry = &qplugin.Registry{}
	}
	se.Router.GET("/api/v1/plugins", scopeGuard(enforcer, "plugins", "read", func(re *core.RequestEvent) error {
		return re.JSON(http.StatusOK, map[string]any{
			"plugins": registry.List(),
		})
	}))
	se.Router.POST("/api/v1/plugins/{id}/enable", scopeGuard(enforcer, "plugins", "write", func(re *core.RequestEvent) error {
		summary, ok := registry.Enable(re.Request.PathValue("id"))
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "plugin not found"})
		}
		return re.JSON(http.StatusOK, summary)
	}))
	se.Router.POST("/api/v1/plugins/{id}/disable", scopeGuard(enforcer, "plugins", "write", func(re *core.RequestEvent) error {
		summary, ok := registry.Disable(re.Request.PathValue("id"))
		if !ok {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "plugin not found"})
		}
		return re.JSON(http.StatusOK, summary)
	}))
}
