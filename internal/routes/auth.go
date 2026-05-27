// Package routes contains the QuantumAtlas HTTP handlers.
//
// authGuard returns a wrapper that gates a PocketBase route handler on the
// caller presenting an acceptable bearer credential.
//
// Migration phases:
//
//   Phase A (now): accept either
//     1. a static QATLAS_WRITE_TOKEN shared secret (CLI uses it via
//        --token or QATLAS_TOKEN env), OR
//     2. a valid PocketBase user auth token (browser SPA after OAuth
//        login).
//
//   Phase B (Step 7 done): drop (1) and require (2) only. CLI starts
//     pasting its PocketBase token from the /token page.
//
//   Phase C (admin allowlist plumbed in): additionally require the
//     authenticated user's GitHub login be in QATLAS_ADMIN_GITHUB_LOGINS
//     for write endpoints.
//
// Read endpoints (wiki, pages, stats, search, graph, /api/server/info,
// /api/oauth2-redirect, /share/{token}, /health) stay open in all phases.
// The wiki repo is public so no sensitive data leaks; writes are what
// must stay protected.

package routes

import (
	"net/http"
	"os"
	"strings"

	"github.com/pocketbase/pocketbase/core"
)

// authGuard wraps a handler so it is only invoked for authenticated
// callers. Returns 401 otherwise. Reads QATLAS_WRITE_TOKEN at every call
// (not memoized) so the operator can rotate the secret without a restart.
func authGuard(handler func(re *core.RequestEvent) error) func(re *core.RequestEvent) error {
	return func(re *core.RequestEvent) error {
		if !isAuthorized(re) {
			return re.JSON(http.StatusUnauthorized, map[string]string{
				"detail": "authentication required (PocketBase user token or QATLAS_WRITE_TOKEN)",
			})
		}
		return handler(re)
	}
}

// isAuthorized returns true when the request carries either a valid
// PocketBase user auth token (set on re.Auth by PocketBase's own auth
// middleware) or the shared QATLAS_WRITE_TOKEN bearer.
func isAuthorized(re *core.RequestEvent) bool {
	// (2) PocketBase-recognized auth record. Any users-collection token
	// counts as authenticated for now; admin allowlist gating is Phase C.
	if re.Auth != nil && re.Auth.Collection() != nil && re.Auth.Collection().Name == "users" {
		return true
	}

	// (1) shared write-token fallback. The CLI uses this until Step 7
	// rewires it to consume PocketBase user tokens.
	expected := strings.TrimSpace(os.Getenv("QATLAS_WRITE_TOKEN"))
	if expected == "" {
		return false
	}
	supplied := bearerToken(re.Request.Header.Get("Authorization"))
	if supplied == "" {
		return false
	}
	return constantTimeEq(supplied, expected)
}

// bearerToken extracts the credential from "Bearer <token>". Case
// matching follows RFC 6750 (the scheme is case-insensitive).
func bearerToken(header string) string {
	if header == "" {
		return ""
	}
	const prefix = "bearer "
	if len(header) < len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return ""
	}
	return strings.TrimSpace(header[len(prefix):])
}

// constantTimeEq compares two strings in length-stable time to avoid
// timing oracles on the shared write token. Length-mismatch shortcut is
// fine because writeTokens are configured length, not user input.
func constantTimeEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := 0; i < len(a); i++ {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}
