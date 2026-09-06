package routes

// me_search_keys.go: the per-user third-party search API key CRUD
// behind the SPA dashboard.
//
//	GET    /api/me/search-keys           — sessionGuard; the caller's key
//	                                     inventory (backend, masked hint,
//	                                     updated_at — never the key).
//	PUT    /api/me/search-keys/{backend} — sessionGuard; upsert the key
//	                                     (body {"key": "..."}). backend
//	                                     must be a catalog backend with a
//	                                     user-key slot.
//	DELETE /api/me/search-keys/{backend} — sessionGuard; remove (opaque
//	                                     404 when absent).
//
// sessionGuard (not scopeGuard), same reasoning as /api/pat: these are
// browser-dashboard credentials, and a leaked PAT must not be able to
// read (GET) or replace (PUT) the owner's third-party keys.
//
// Storage is AES-256-GCM encrypted (internal/userkeys); the encryption
// key is derived from the server's system PAT token, so the feature is
// disabled (503 on writes) when no secret is configured.

import (
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/search"
	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/userkeys"

	"github.com/pocketbase/pocketbase/core"
)

// RegisterMeSearchKeys wires the /api/me/search-keys surface. keys may
// be a disabled store (no server secret): GET then reports an empty
// inventory with enabled=false, writes answer 503.
func RegisterMeSearchKeys(se *core.ServeEvent, keys *userkeys.Store) {
	se.Router.GET("/api/me/search-keys", sessionGuard(meSearchKeysListHandler(keys)))
	se.Router.PUT("/api/me/search-keys/{backend}", sessionGuard(meSearchKeysPutHandler(keys)))
	se.Router.DELETE("/api/me/search-keys/{backend}", sessionGuard(meSearchKeysDeleteHandler(keys)))
}

// meSearchKeysListResponse is the GET wire shape. enabled=false means
// the server has no secret to encrypt with — the panel degrades to a
// hint instead of failing.
type meSearchKeysListResponse struct {
	Enabled bool               `json:"enabled"`
	Keys    []userkeys.KeyMeta `json:"keys"`
}

func meSearchKeysListHandler(keys *userkeys.Store) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if keys == nil || !keys.Enabled() {
			return re.JSON(http.StatusOK, meSearchKeysListResponse{Enabled: false, Keys: []userkeys.KeyMeta{}})
		}
		entries, err := keys.ListMeta(re.Auth.Id)
		if err != nil {
			slog.Error("me search-keys: list failed", "user_id", re.Auth.Id, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}
		if entries == nil {
			entries = []userkeys.KeyMeta{}
		}
		return re.JSON(http.StatusOK, meSearchKeysListResponse{Enabled: true, Keys: entries})
	}
}

// meSearchKeyPutRequest is the PUT body shape.
type meSearchKeyPutRequest struct {
	Key string `json:"key"`
}

func meSearchKeysPutHandler(keys *userkeys.Store) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		backend := strings.TrimSpace(re.Request.PathValue("backend"))
		meta, ok := search.LookupBackendMeta(backend)
		if !ok || !meta.UserKey {
			return re.JSON(http.StatusBadRequest, map[string]string{
				"detail": "unknown backend or backend does not accept a user API key: " + backend,
			})
		}
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, userkeys.MaxKeyLen+1024))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var body meSearchKeyPutRequest
		if len(raw) > 0 {
			if err := json.Unmarshal(raw, &body); err != nil {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON: " + err.Error()})
			}
		}
		if strings.TrimSpace(body.Key) == "" {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "key is required"})
		}
		if err := keys.SetKey(re.Auth.Id, backend, body.Key); err != nil {
			if errors.Is(err, userkeys.ErrDisabled) {
				return re.JSON(http.StatusServiceUnavailable, map[string]string{
					"detail": "user search API keys are disabled on this server (no encryption secret configured)",
				})
			}
			slog.Error("me search-keys: save failed", "user_id", re.Auth.Id, "backend", backend, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}
		return re.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}

func meSearchKeysDeleteHandler(keys *userkeys.Store) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		backend := strings.TrimSpace(re.Request.PathValue("backend"))
		if _, ok := search.LookupBackendMeta(backend); !ok {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "key not found"})
		}
		found, err := keys.DeleteKey(re.Auth.Id, backend)
		if err != nil {
			if errors.Is(err, userkeys.ErrDisabled) {
				return re.JSON(http.StatusServiceUnavailable, map[string]string{
					"detail": "user search API keys are disabled on this server (no encryption secret configured)",
				})
			}
			slog.Error("me search-keys: delete failed", "user_id", re.Auth.Id, "backend", backend, "error", err)
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": "internal server error"})
		}
		if !found {
			return re.JSON(http.StatusNotFound, map[string]string{"detail": "key not found"})
		}
		return re.JSON(http.StatusOK, map[string]bool{"ok": true})
	}
}
