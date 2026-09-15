// Admin endpoints for the MinerU daily-conversion scheduler.
//
//	POST /api/admin/mineru/run    — adminGuard; trigger a batch run
//	                                immediately. Coalesced through the
//	                                scheduler's singleflight: a second
//	                                trigger while a run is active returns
//	                                started=false, reason=already_running
//	                                (the in-flight run is NOT disturbed).
//	GET  /api/admin/mineru/status — adminGuard; the scheduler Snapshot
//	                                (running, next_run_at, last_stop_reason,
//	                                daily_cap, converted_today, cap_day, …).
//
// Both respond 503 when the scheduler isn't wired (paper access
// disabled — same convention as db/schema with a nil pool).
//
// Token pool management (adminGuard; same 503 convention when the
// converter isn't wired):
//
//	GET    /api/admin/mineru/tokens     — masked pool listing with
//	                                     rotated_at + cooldown state.
//	POST   /api/admin/mineru/tokens     — rotate a token in (persisted
//	                                     first, then applied to the live
//	                                     ring — no restart needed).
//	DELETE /api/admin/mineru/tokens/{id} — remove by hash id; refuses
//	                                     to drop the last pool entry.
package routes

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/IAI-USTC-Quantum/QuantumAtlas/internal/mineru"

	"github.com/pocketbase/pocketbase/core"
)

// adminMineruRunHandler answers POST /api/admin/mineru/run. The run
// executes in the background on the scheduler's lifecycle context, so
// the response returns immediately with {started, reason, snapshot}
// rather than blocking for the batch.
func adminMineruRunHandler(sched *mineru.Scheduler) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if sched == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "mineru scheduler not configured (QATLAS_PAPER_ACCESS_ENABLED=false)",
			})
		}
		started, reason := sched.RunNow(re.Request.Context())
		return re.JSON(http.StatusOK, map[string]any{
			"started":  started,
			"reason":   reason,
			"snapshot": sched.Snapshot(),
		})
	}
}

// adminMineruStatusHandler answers GET /api/admin/mineru/status with
// the raw scheduler snapshot.
func adminMineruStatusHandler(sched *mineru.Scheduler) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if sched == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "mineru scheduler not configured (QATLAS_PAPER_ACCESS_ENABLED=false)",
			})
		}
		return re.JSON(http.StatusOK, sched.Snapshot())
	}
}

// mineruTokensResponse is the GET /api/admin/mineru/tokens payload.
// managed=false means mutations only touch the in-memory ring (no
// registry PostgreSQL wired) and are lost on restart — the frontend
// surfaces that as a warning.
type mineruTokensResponse struct {
	Managed bool                   `json:"managed"`
	Tokens  []mineru.TokenSnapshot `json:"tokens"`
}

// adminMineruTokensHandler answers GET /api/admin/mineru/tokens.
func adminMineruTokensHandler(conv *mineru.Converter) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if conv == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "mineru converter not configured",
			})
		}
		return re.JSON(http.StatusOK, mineruTokensResponse{
			Managed: conv.TokenStoreConfigured(),
			Tokens:  conv.TokensSnapshot(),
		})
	}
}

// adminMineruTokenAddHandler answers POST /api/admin/mineru/tokens
// with body {"token": "…"}. 201 + the new entry's snapshot on success.
func adminMineruTokenAddHandler(conv *mineru.Converter) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if conv == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "mineru converter not configured",
			})
		}
		raw, err := io.ReadAll(io.LimitReader(re.Request.Body, 1<<20))
		if err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "read body: " + err.Error()})
		}
		var body struct {
			Token string `json:"token"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return re.JSON(http.StatusBadRequest, map[string]string{"detail": "invalid JSON: " + err.Error()})
		}
		snap, err := conv.AddToken(re.Request.Context(), body.Token)
		if err != nil {
			if errors.Is(err, mineru.ErrEmptyToken) {
				return re.JSON(http.StatusBadRequest, map[string]string{"detail": "token is required"})
			}
			return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
		}
		return re.JSON(http.StatusCreated, snap)
	}
}

// adminMineruTokenDeleteHandler answers DELETE /api/admin/mineru/tokens/{id}
// (id = TokenID hash prefix from the listing). 404 unknown id; 409 when
// removing the last pool entry.
func adminMineruTokenDeleteHandler(conv *mineru.Converter) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if conv == nil {
			return re.JSON(http.StatusServiceUnavailable, map[string]string{
				"detail": "mineru converter not configured",
			})
		}
		id := re.Request.PathValue("id")
		if err := conv.RemoveToken(re.Request.Context(), id); err != nil {
			switch {
			case errors.Is(err, mineru.ErrTokenNotFound):
				return re.JSON(http.StatusNotFound, map[string]string{"detail": "token not found"})
			case errors.Is(err, mineru.ErrLastToken):
				return re.JSON(http.StatusConflict, map[string]string{"detail": err.Error()})
			default:
				return re.JSON(http.StatusInternalServerError, map[string]string{"detail": err.Error()})
			}
		}
		return re.JSON(http.StatusOK, map[string]string{"removed": id})
	}
}
