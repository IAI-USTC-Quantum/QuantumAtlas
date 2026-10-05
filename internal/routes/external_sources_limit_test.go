package routes

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

func TestExternalSourceLimitRejectsBeforeAcquisitionAndRecovers(t *testing.T) {
	slots := make(chan struct{}, 2)
	started, release := make(chan struct{}, 2), make(chan struct{})
	var acquisitions atomic.Int32
	limited := externalSourceLimit(slots, func(re *core.RequestEvent) error {
		acquisitions.Add(1)
		started <- struct{}{}
		<-release
		return re.JSON(http.StatusOK, map[string]bool{"done": true})
	})
	request := func() *core.RequestEvent {
		re := &core.RequestEvent{}
		re.Request = httptest.NewRequest(http.MethodPost, "/api/papers/source-register", nil)
		re.Response = httptest.NewRecorder()
		return re
	}
	done := make(chan error, 2)
	for range 2 {
		go func() { done <- limited(request()) }()
	}
	<-started
	<-started
	excess := request()
	if err := limited(excess); err != nil {
		t.Fatal(err)
	}
	recorder := excess.Response.(*httptest.ResponseRecorder)
	if recorder.Code != http.StatusServiceUnavailable || recorder.Header().Get("Retry-After") != "5" || acquisitions.Load() != 2 {
		t.Fatalf("busy response=%d headers=%v acquisitions=%d", recorder.Code, recorder.Header(), acquisitions.Load())
	}
	close(release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if err := limited(request()); err != nil || acquisitions.Load() != 3 || len(slots) != 0 {
		t.Fatalf("capacity did not recover: err=%v acquisitions=%d slots=%d", err, acquisitions.Load(), len(slots))
	}
}

func TestExternalSourceLimitReleasesSlotAfterHandlerFailure(t *testing.T) {
	slots := make(chan struct{}, 1)
	want := errors.New("fixture registration failure")
	calls := 0
	limited := externalSourceLimit(slots, func(*core.RequestEvent) error { calls++; return want })
	for range 2 {
		if err := limited(&core.RequestEvent{}); !errors.Is(err, want) {
			t.Fatalf("handler error changed: %v", err)
		}
	}
	if calls != 2 || len(slots) != 0 {
		t.Fatalf("failed registration retained slot: calls=%d slots=%d", calls, len(slots))
	}
}
