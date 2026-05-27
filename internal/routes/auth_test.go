package routes

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pocketbase/pocketbase/core"
)

// makeRE wraps an httptest.Request in a minimal core.RequestEvent. We
// only need Auth + Request, the rest of the PocketBase context is unused
// by isAuthorized.
func makeRE(authHeader string) *core.RequestEvent {
	req := httptest.NewRequest(http.MethodPost, "/api/papers/foo/upload-pdf", nil)
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	rw := httptest.NewRecorder()
	re := &core.RequestEvent{}
	re.Request = req
	re.Response = rw
	return re
}

func TestIsAuthorized_NoAuthRejected(t *testing.T) {
	if isAuthorized(makeRE("")) {
		t.Fatal("expected rejection with no Authorization header")
	}
	if isAuthorized(makeRE("Bearer something")) {
		t.Fatal("expected rejection when re.Auth is nil even with a bearer")
	}
	if isAuthorized(makeRE("Basic abcdef==")) {
		t.Fatal("expected rejection of non-bearer scheme")
	}
}

func TestAuthGuard_RejectAndPass(t *testing.T) {
	called := false
	inner := func(re *core.RequestEvent) error {
		called = true
		return nil
	}
	wrapped := authGuard(inner)

	re := makeRE("")
	if err := wrapped(re); err != nil {
		t.Fatalf("wrapped() returned err on reject path: %v", err)
	}
	if called {
		t.Fatal("inner handler was called on unauthorized request")
	}
	rec := re.Response.(*httptest.ResponseRecorder)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("expected 401, got %d", rec.Code)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "authentication required") {
		t.Errorf("expected error body to mention authentication, got %q", body)
	}
	if !strings.Contains(body, "/login") || !strings.Contains(body, "/token") {
		t.Errorf("expected error body to point caller at /login and /token, got %q", body)
	}

	// The accept-path needs a real PocketBase auth record on re.Auth,
	// which requires more setup than this unit test buys us. The
	// happy-path is covered end-to-end by
	// tests/integration/test_production_smoke.py.
}
