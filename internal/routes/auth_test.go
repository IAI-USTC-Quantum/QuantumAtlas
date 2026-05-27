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

// TestDecodeScopes pins down the fail-closed contract that the rest of
// the auth stack assumes:
//
//   - Empty-ish inputs ("", "null", whitespace) collapse to a nil slice.
//   - Well-formed JSON arrays decode to their string contents.
//   - ANY unmarshal error (truncation, wrong type, trailing comma,
//     bare value) returns nil — never a partially-populated slice and
//     never a panic. Allows() treats nil as "no permissions", so a
//     single corrupt DB row can only ever cause 403, never elevate
//     a PAT to unintended access.
//
// If this regresses (e.g. someone "improves" the function to return
// []string{} on error or to log-and-pass), every PAT with corrupt
// scopes would silently get write access. Hence the painfully literal
// assertions.
func TestDecodeScopes(t *testing.T) {
	t.Run("empty representations return nil", func(t *testing.T) {
		// "", "null", and whitespace-only take the early-return path.
		for _, raw := range []string{"", "null", "  ", "\t\n", " null "} {
			got := decodeScopes(raw)
			if got != nil {
				t.Errorf("decodeScopes(%q) = %v, want nil", raw, got)
			}
		}
	})

	t.Run("empty array decodes to non-nil empty slice", func(t *testing.T) {
		// "[]" goes through json.Unmarshal which produces an empty
		// (but non-nil) slice. Functionally identical to nil for
		// Allows() — both iterate zero scopes — but we pin the
		// distinction so a refactor that changes the return shape
		// has to consciously update this test.
		got := decodeScopes("[]")
		if got == nil {
			t.Errorf("decodeScopes(\"[]\") returned nil, want non-nil empty slice")
		}
		if len(got) != 0 {
			t.Errorf("decodeScopes(\"[]\") = %v, want length 0", got)
		}
	})

	t.Run("well-formed scope lists decode in order", func(t *testing.T) {
		cases := []struct {
			raw  string
			want []string
		}{
			{`["papers:write"]`, []string{"papers:write"}},
			{`["papers:write","shares:read"]`, []string{"papers:write", "shares:read"}},
			{`  ["shares:write"]  `, []string{"shares:write"}}, // surrounding whitespace tolerated
		}
		for _, tc := range cases {
			got := decodeScopes(tc.raw)
			if len(got) != len(tc.want) {
				t.Errorf("decodeScopes(%q) = %v, want %v", tc.raw, got, tc.want)
				continue
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("decodeScopes(%q)[%d] = %q, want %q", tc.raw, i, got[i], tc.want[i])
				}
			}
		}
	})

	t.Run("corrupt JSON fails closed to nil", func(t *testing.T) {
		// Each of these would either panic, return partial data, or
		// (worst case) decode to a slice containing string-cast
		// numbers under a more permissive parser. We require nil.
		corrupt := []string{
			`["papers:write",]`, // trailing comma — invalid JSON
			`{"foo":"bar"}`,     // object, not array
			`["papers:wr`,       // truncated mid-token
			`[1, 2, 3]`,         // numeric elements can't unmarshal into []string
			`"papers:write"`,    // bare string, not an array
			`garbage`,           // not JSON at all
			`[true]`,            // boolean element
		}
		for _, raw := range corrupt {
			got := decodeScopes(raw)
			if got != nil {
				t.Errorf("decodeScopes(%q) = %v, want nil (fail-closed)", raw, got)
			}
		}
	})
}
