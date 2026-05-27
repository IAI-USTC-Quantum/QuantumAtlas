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

func TestBearerToken(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"Bearer", ""},
		{"Bearer abc", "abc"},
		{"bearer xyz", "xyz"},
		{"BEARER  trim  ", "trim"},
		{"Basic abcdef==", ""},
	}
	for _, tc := range cases {
		if got := bearerToken(tc.in); got != tc.want {
			t.Errorf("bearerToken(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestConstantTimeEq(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"", "", true},
		{"abc", "abc", true},
		{"abc", "abd", false},
		{"abc", "ab", false}, // length mismatch short-circuit
		{"", "abc", false},
	}
	for _, tc := range cases {
		if got := constantTimeEq(tc.a, tc.b); got != tc.want {
			t.Errorf("constantTimeEq(%q,%q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestIsAuthorized_WriteTokenFallback(t *testing.T) {
	t.Setenv("QATLAS_WRITE_TOKEN", "shared-secret-xyz")

	t.Run("missing header rejected", func(t *testing.T) {
		if isAuthorized(makeRE("")) {
			t.Fatal("expected rejection with no Authorization header")
		}
	})

	t.Run("wrong token rejected", func(t *testing.T) {
		if isAuthorized(makeRE("Bearer not-the-token")) {
			t.Fatal("expected rejection with mismatched token")
		}
	})

	t.Run("correct token accepted", func(t *testing.T) {
		if !isAuthorized(makeRE("Bearer shared-secret-xyz")) {
			t.Fatal("expected accept with matching write token")
		}
	})

	t.Run("non-bearer scheme rejected", func(t *testing.T) {
		if isAuthorized(makeRE("Basic shared-secret-xyz")) {
			t.Fatal("expected rejection of Basic scheme")
		}
	})

	t.Run("scheme case-insensitive", func(t *testing.T) {
		if !isAuthorized(makeRE("bearer shared-secret-xyz")) {
			t.Fatal("expected accept regardless of scheme case")
		}
	})
}

func TestIsAuthorized_WriteTokenEmpty(t *testing.T) {
	t.Setenv("QATLAS_WRITE_TOKEN", "")

	if isAuthorized(makeRE("Bearer anything")) {
		t.Fatal("with QATLAS_WRITE_TOKEN unset, any bearer must be rejected")
	}
}

func TestIsAuthorized_TrimsWhitespace(t *testing.T) {
	t.Setenv("QATLAS_WRITE_TOKEN", "  trimmed-secret  ")

	if !isAuthorized(makeRE("Bearer trimmed-secret")) {
		t.Fatal("expected env value to be trimmed before comparison")
	}
}

func TestAuthGuard_RejectsAndPassesThrough(t *testing.T) {
	t.Setenv("QATLAS_WRITE_TOKEN", "guard-secret")
	called := false
	inner := func(re *core.RequestEvent) error {
		called = true
		return nil
	}
	wrapped := authGuard(inner)

	// reject path
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
	if !strings.Contains(rec.Body.String(), "authentication required") {
		t.Errorf("expected error body to mention authentication, got %q", rec.Body.String())
	}

	// accept path
	called = false
	re2 := makeRE("Bearer guard-secret")
	if err := wrapped(re2); err != nil {
		t.Fatalf("wrapped() returned err on accept path: %v", err)
	}
	if !called {
		t.Fatal("inner handler was not called on authorized request")
	}
}
