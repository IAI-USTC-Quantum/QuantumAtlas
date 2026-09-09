package routes

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/config"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloader"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/downloadfleet"
	wp "github.com/IAI-USTC-Quantum/QuantumAtlas/internal/workerprotocol"
	"github.com/casbin/casbin/v2"
	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/tools/hook"
	"github.com/pocketbase/pocketbase/tools/router"
)

// RegisterDownloadFleet keeps machine authentication separate from human admin
// sessions. Nodes cannot use their bearer credentials to approve themselves.
func RegisterDownloadFleet(se *core.ServeEvent, cfg *config.Config, fleet *downloadfleet.Service, enforcer *casbin.Enforcer) {
	se.Router.GET("/api/downloader/remote-jobs", scopeGuard(enforcer, "papers", "read", func(re *core.RequestEvent) error {
		if fleet == nil || !fleet.Enabled() {
			return re.JSON(http.StatusOK, map[string]any{"enabled": false, "jobs": []downloadfleet.Job{}})
		}
		snapshot, err := fleet.Snapshot(re.Request.Context())
		if err != nil {
			return fleetRouteError(re, err)
		}
		return re.JSON(http.StatusOK, map[string]any{"enabled": true, "jobs": snapshot.Jobs})
	}))
	worker := func(re *core.RequestEvent) error {
		if fleet == nil || !fleet.Enabled() {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{"error": "download fleet disabled"})
		}
		fleet.WorkerHandler().ServeHTTP(re.Response, re.Request)
		return nil
	}
	se.Router.POST(wp.RegisterPath, worker)
	se.Router.GET(wp.StatusPath, worker)
	se.Router.POST(wp.HeartbeatPath, worker)
	se.Router.POST(wp.ClaimPath, worker)
	se.Router.POST(wp.ReportPath, worker)
	se.Router.PUT(wp.UploadPath+"{attempt}", worker).Bind(
		&hook.Handler[*core.RequestEvent]{Id: "downloaderStreamingUpload", Priority: -1000000, Func: streamWorkerUpload},
		apis.BodyLimit(downloader.DefaultMaxPDFBytes),
	)
	se.Router.GET(wp.ReceiptPath+"{attempt}", worker)
	se.Router.GET("/api/admin/downloader/workers", adminGuard(cfg, func(re *core.RequestEvent) error {
		if fleet == nil {
			return fleetRouteError(re, downloadfleet.ErrDisabled)
		}
		snapshot, err := fleet.Snapshot(re.Request.Context())
		if err != nil {
			return fleetRouteError(re, err)
		}
		re.Response.Header().Set("Cache-Control", "no-store")
		return re.JSON(http.StatusOK, snapshot)
	}))
	se.Router.POST("/api/admin/downloader/enrollment", adminGuard(cfg, func(re *core.RequestEvent) error {
		if fleet == nil {
			return fleetRouteError(re, downloadfleet.ErrDisabled)
		}
		enrollment, err := fleet.Enrollment(re.Request.Context())
		if err != nil {
			return fleetRouteError(re, err)
		}
		re.Response.Header().Set("Cache-Control", "no-store")
		return re.JSON(http.StatusCreated, enrollment)
	}))
	se.Router.POST("/api/admin/downloader/workers/{id}/{action}", adminGuard(cfg, func(re *core.RequestEvent) error {
		if fleet == nil {
			return fleetRouteError(re, downloadfleet.ErrDisabled)
		}
		id, action := re.Request.PathValue("id"), re.Request.PathValue("action")
		switch action {
		case "approve", "reject", "drain", "enable", "revoke":
		default:
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "unknown worker action"})
		}
		if err := fleet.Action(re.Request.Context(), id, action); err != nil {
			return fleetRouteError(re, err)
		}
		snapshot, err := fleet.Snapshot(re.Request.Context())
		if err != nil {
			return fleetRouteError(re, err)
		}
		for _, node := range snapshot.Workers {
			if node.ID == id {
				return re.JSON(http.StatusOK, node)
			}
		}
		return re.JSON(http.StatusOK, map[string]string{"id": id})
	}))
}

// PocketBase normally retains the whole body for rereading. Raw PDF transfers
// never need replay; unwrap BEFORE body-limit middleware so uploads stay streamed
// while the route and fleet still enforce independent 100 MiB bounds.
func streamWorkerUpload(re *core.RequestEvent) error {
	if body, ok := re.Request.Body.(*router.RereadableReadCloser); ok {
		re.Request.Body = body.ReadCloser
	}
	return re.Next()
}

func fleetRouteError(re *core.RequestEvent, err error) error {
	code, detail := http.StatusInternalServerError, "download fleet operation failed"
	switch {
	case errors.Is(err, downloadfleet.ErrDisabled):
		code, detail = http.StatusServiceUnavailable, "download fleet disabled"
	case errors.Is(err, downloadfleet.ErrNotFound):
		code, detail = http.StatusNotFound, "worker not found"
	case errors.Is(err, downloadfleet.ErrConflict):
		code, detail = http.StatusConflict, "worker state changed; refresh and retry"
	case errors.Is(err, downloadfleet.ErrForbidden):
		code, detail = http.StatusForbidden, "worker action not allowed"
	default:
		slog.Error("download fleet admin operation", "error", err)
	}
	return re.JSON(code, map[string]string{"detail": detail})
}
