//go:build e2e

package e2e

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestProductionSmoke is deliberately opt-in. Normal go test ./tests/... only
// runs offline fixtures. Selecting this live test with -tags=e2e requires
// QATLAS_SERVER_TARGETS; missing/bad configuration FAILS instead of skipping.
//
// QATLAS_SERVER_TARGETS is comma/newline-separated:
//
//	https://edge.example|token-env=SMOKE_PAT
//	https://private.example|insecure|token=...
//
// TLS is verified unless that target explicitly opts out via |insecure.
// Tokenless targets still check anonymous health, info, lockdown, and SPA;
// authenticated subtests explicitly skip, so that is NOT full auth coverage.
// Health detail requires a system PAT or a PocketBase session JWT: the current
// IsCallerAuthenticated probe intentionally does not resolve ordinary user PATs.
// The token also needs papers:read (or master) for the authenticated stats probe.
// No deployment .env, auth store, legacy URL alias, or global token is consulted.
//
// Optional QATLAS_EXPECTED_VERSION checks the exact deployed version. Without
// that input only nonempty/non-dev is verified, NOT freshness or "latest".
func TestProductionSmoke(t *testing.T) {
	targets, err := parseSmokeTargets(os.Getenv("QATLAS_SERVER_TARGETS"), os.LookupEnv)
	if err != nil {
		t.Fatal(err)
	}
	expected := strings.TrimSpace(os.Getenv("QATLAS_EXPECTED_VERSION"))
	for i, target := range targets {
		t.Run(fmt.Sprintf("target-%d", i+1), func(t *testing.T) {
			client, err := newSmokeClient(target, smokeTimeout)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(client.CloseIdleConnections)
			t.Run("health_anonymous", func(t *testing.T) {
				response, err := smokeGet(client, target, "api/health", false)
				if err != nil {
					t.Fatal(err)
				}
				if err := checkSmokeHealth(response, false, expected); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("server_info", func(t *testing.T) {
				response, err := smokeGet(client, target, "api/server/info", false)
				if err != nil {
					t.Fatal(err)
				}
				if err := checkSmokeInfo(response, expected); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("protected_read_anonymous", func(t *testing.T) {
				response, err := smokeGet(client, target, "api/papers/stats", false)
				if err != nil {
					t.Fatal(err)
				}
				if err := checkSmokeProtected(response, false); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("spa_and_same_origin_javascript", func(t *testing.T) {
				if err := checkSmokeSPA(client, target); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("health_authenticated_detail", func(t *testing.T) {
				if target.token == "" {
					t.Skip("no target token configured: authenticated health detail was NOT checked")
				}
				response, err := smokeGet(client, target, "api/health", true)
				if err != nil {
					t.Fatal(err)
				}
				if err := checkSmokeHealth(response, true, expected); err != nil {
					t.Fatal(err)
				}
			})
			t.Run("protected_read_authenticated", func(t *testing.T) {
				if target.token == "" {
					t.Skip("no target token configured: authorized paper stats was NOT checked")
				}
				response, err := smokeGet(client, target, "api/papers/stats", true)
				if err != nil {
					t.Fatal(err)
				}
				if err := checkSmokeProtected(response, true); err != nil {
					t.Fatal(err)
				}
			})
		})
	}
}
