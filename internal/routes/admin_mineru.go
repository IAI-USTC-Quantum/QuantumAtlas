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
package routes

import (
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
