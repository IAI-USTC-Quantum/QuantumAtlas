package routes

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestAdminAcquisitionFailuresUnavailable(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/admin/acquisition/failures", nil)
	rec := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rec
	if err := adminAcquisitionFailuresHandler(nil)(re); err != nil {
		t.Fatalf("handler: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body=%s", rec.Code, rec.Body.String())
	}
}
